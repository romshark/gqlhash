package proxyfast_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2/internal/app/proxy"
	"github.com/romshark/gqlhash/v2/internal/app/proxyfast"
)

// commands are the two binaries, as the tests below drive them. Each starts the
// same proxy on a different HTTP implementation, which is the whole of the
// difference this file exists to bound.
var commands = []struct {
	name string
	run  func(ctx context.Context, args []string, stderr io.Writer) int
}{
	{"nethttp", func(ctx context.Context, args []string, stderr io.Writer) int {
		return proxy.Run(ctx, "gqlhash-proxy", "dev", args, io.Discard, stderr)
	}},
	{"fasthttp", func(ctx context.Context, args []string, stderr io.Writer) int {
		return proxy.RunWith(ctx, "gqlhash-proxy-fhttp", "dev", args,
			io.Discard, stderr, proxyfast.Underlay)
	}},
}

// logs collects a run's output from the goroutine serving it.
type logs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// serve starts one of the commands and returns the address it reports, which is
// the only way to learn the port behind :0.
func serve(
	t *testing.T,
	run func(context.Context, []string, io.Writer) int,
	args ...string,
) string {
	t.Helper()
	out := new(logs)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan int, 1)
	full := append([]string{"gqlhash-proxy",
		"-server.listen", "127.0.0.1:0", "-control.listen", "127.0.0.1:0"}, args...)
	go func() { stopped <- run(ctx, full, out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-stopped:
			if code != 0 {
				t.Errorf("expected a clean stop; received %d: %s", code, out.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("the proxy didn't stop")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.SplitSeq(out.String(), "\n") {
			var event map[string]any
			if line == "" || json.Unmarshal([]byte(line), &event) != nil {
				continue
			}
			if event["message"] == "listening" {
				if a, ok := event["address"].(string); ok {
					return a
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the proxy didn't report an address: %s", out.String())
	return ""
}

func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

func post(t *testing.T, address, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"http://"+address+"/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return send(t, req)
}

const (
	upstreamAnswer = `{"data":{"user":{"name":"Ada"}}}`
	docAllowed     = `{"query":"query GetUser{user(id:1){name}}"}`
	docRejected    = `{"query":"query GetUser{user(id:1){secret}}"}`
)

// spy is an upstream that records what reached it, headers included.
type spy struct {
	mu       sync.Mutex
	requests int
	body     string
	path     string
	header   http.Header
}

func (s *spy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests++
	s.body, s.path = string(body), r.URL.Path
	s.header = r.Header.Clone()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

func (s *spy) snapshot() (int, string, string, http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.body, s.path, s.header
}

// upstreamFor starts an upstream and writes the allowlist that goes with it.
func upstreamFor(t *testing.T) (dir, target string, sp *spy) {
	t.Helper()
	dir = t.TempDir()
	doc := "query GetUser {\n  user(id: 1) {\n    name\n  }\n}"
	if err := os.WriteFile(filepath.Join(dir, "get-user.graphql"),
		[]byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	sp = new(spy)
	mux := http.NewServeMux()
	mux.Handle("/graphql", sp)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return dir, server.URL + "/graphql", sp
}

// TestParity drives both commands over real HTTP through the same assertions.
//
// What a request is answered with is the decision, and both reach the same
// [proxy.Core]. Anything that differs here is the machinery around it having
// drifted apart, which is what this test exists to catch.
func TestParity(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			dir, target, sp := upstreamFor(t)
			address := serve(t, c.run, "-upstream.url", target,
				"-allowlist", dir, "-server.max-body", "2048")

			// An allowed document is forwarded and the answer comes back whole.
			code, body := post(t, address, docAllowed)
			if code != http.StatusOK {
				t.Fatalf("allowed: expected 200; received %d: %s", code, body)
			}
			if body != upstreamAnswer {
				t.Errorf("allowed: expected the upstream answer; received %q", body)
			}
			n, gotBody, gotPath, _ := sp.snapshot()
			if n != 1 {
				t.Errorf("expected one request upstream; received %d", n)
			}
			if gotBody != docAllowed {
				t.Errorf("expected the same bytes upstream; received %q", gotBody)
			}
			if gotPath != "/graphql" {
				t.Errorf("expected the upstream path to replace the request's; "+
					"received %q", gotPath)
			}

			// A document that isn't on the list never reaches the upstream.
			code, body = post(t, address, docRejected)
			if code != http.StatusForbidden {
				t.Fatalf("rejected: expected 403; received %d: %s", code, body)
			}
			if !strings.Contains(body, "OPERATION_NOT_ALLOWED") {
				t.Errorf("rejected: expected the error shape; received %q", body)
			}
			if n, _, _, _ := sp.snapshot(); n != 1 {
				t.Errorf("expected a rejection to stop here; upstream saw %d", n)
			}

			// A body that isn't a GraphQL request is malformed, not rejected.
			code, body = post(t, address, `{"query":`)
			if code != http.StatusBadRequest {
				t.Fatalf("malformed: expected 400; received %d: %s", code, body)
			}
			if !strings.Contains(body, "BAD_REQUEST") {
				t.Errorf("malformed: expected the error shape; received %q", body)
			}

			// Past -server.max-body the answer says so.
			code, body = post(t, address,
				`{"query":"`+strings.Repeat("x", 4096)+`"}`)
			if code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized: expected 413; received %d: %s", code, body)
			}

			// A GET carries the document in the query string.
			req, err := http.NewRequest(http.MethodGet,
				"http://"+address+"/graphql?query="+
					url.QueryEscape("query GetUser{user(id:1){name}}"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if code, body = send(t, req); code != http.StatusOK {
				t.Fatalf("GET: expected 200; received %d: %s", code, body)
			}
		})
	}
}

// TestParityForwardedHeaders covers the headers a proxy is trusted to get
// right. Without -trust-forwarded neither command may pass on what a client
// claimed about where it came from.
func TestParityForwardedHeaders(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			dir, target, sp := upstreamFor(t)
			address := serve(t, c.run, "-upstream.url", target, "-allowlist", dir)

			req, err := http.NewRequest(http.MethodPost,
				"http://"+address+"/graphql", strings.NewReader(docAllowed))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			// What a client would claim if it could.
			req.Header.Set("X-Forwarded-For", "203.0.113.9")
			req.Header.Set("Forwarded", "for=203.0.113.9;proto=https")
			req.Header.Set("X-Forwarded-Proto", "https")
			if code, body := send(t, req); code != http.StatusOK {
				t.Fatalf("expected 200; received %d: %s", code, body)
			}

			_, _, _, got := sp.snapshot()
			if got == nil {
				t.Fatal("the upstream saw no request")
			}
			if v := got.Get("Forwarded"); v != "" {
				t.Errorf("expected the client's Forwarded to be dropped; received %q", v)
			}
			if v := got.Get("X-Forwarded-For"); strings.Contains(v, "203.0.113.9") {
				t.Errorf("expected the claimed chain to be dropped; received %q", v)
			}
			if got.Get("X-Forwarded-For") == "" {
				t.Error("expected the peer to be reported as X-Forwarded-For")
			}
			if v := got.Get("X-Forwarded-Proto"); v != "http" {
				t.Errorf("expected the proto the proxy was reached over; received %q", v)
			}
			if got.Get("X-Forwarded-Host") == "" {
				t.Error("expected X-Forwarded-Host to be set")
			}
			if v := got.Get("Keep-Alive"); v != "" {
				t.Errorf("expected Keep-Alive not to be forwarded; received %q", v)
			}
		})
	}
}

// TestParityOpaqueErrors makes sure -opaque-errors withholds the same detail
// under both, since the rule lives in one place and each has to ask for it.
func TestParityOpaqueErrors(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			dir, target, _ := upstreamFor(t)
			address := serve(t, c.run, "-upstream.url", target,
				"-allowlist", dir, "-opaque-errors")
			// A malformed body would say why without the flag.
			code, body := post(t, address, `{"query":`)
			if code != http.StatusForbidden {
				t.Fatalf("expected 403 for everything; received %d: %s", code, body)
			}
			if !strings.Contains(body, "OPERATION_NOT_ALLOWED") ||
				strings.Contains(body, "BAD_REQUEST") {
				t.Errorf("expected the opaque answer; received %q", body)
			}
		})
	}
}

// TestParityAmbiguousFraming sends the framing a request smuggler would. A
// request whose length is open to two readings has to be refused by both, and
// nothing of it may reach the upstream.
func TestParityAmbiguousFraming(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			dir, target, sp := upstreamFor(t)
			address := serve(t, c.run, "-upstream.url", target, "-allowlist", dir)
			length := strconv.Itoa(len(docAllowed))

			for _, tc := range []struct{ name, raw string }{
				{"content-length and transfer-encoding",
					"POST /graphql HTTP/1.1\r\nHost: x\r\n" +
						"Content-Type: application/json\r\n" +
						"Content-Length: " + length + "\r\n" +
						"Transfer-Encoding: chunked\r\n\r\n0\r\n\r\n"},
				{"two conflicting content-length",
					"POST /graphql HTTP/1.1\r\nHost: x\r\n" +
						"Content-Type: application/json\r\n" +
						"Content-Length: " + length + "\r\n" +
						"Content-Length: 4\r\n\r\n" + docAllowed},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before, _, _, _ := sp.snapshot()
					status := raw(t, address, tc.raw)
					// A refusal, or no answer at all where the connection was
					// dropped. What must not happen is the request being served.
					if strings.HasPrefix(status, "HTTP/1.1 2") {
						t.Errorf("expected ambiguous framing to be refused; "+
							"received %q", status)
					}
					if after, _, _, _ := sp.snapshot(); after != before {
						t.Error("expected nothing to reach the upstream")
					}
				})
			}
		})
	}
}

// raw sends bytes over a socket and returns the status line, so the framing
// under test isn't normalized by an HTTP client on the way out.
func raw(t *testing.T, address, request string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\r\n")
	return line
}

// TestParityConnRecycling makes sure -upstream.max-conn-lifetime turns over the
// upstream pool under both commands, which is what lets a name standing for
// several backends reach one added after the pool filled.
//
// It counts connections rather than requests: the point of the flag is that the
// same traffic arrives over more of them.
func TestParityConnRecycling(t *testing.T) {
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			doc := "query GetUser {\n  user(id: 1) {\n    name\n  }\n}"
			if err := os.WriteFile(filepath.Join(dir, "get-user.graphql"),
				[]byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}

			// An upstream whose listener counts what it accepts.
			var conns atomic.Int64
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mux.Handle("/graphql", &spy{})
			server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
			go func() {
				_ = server.Serve(&countingListener{Listener: listener, conns: &conns})
			}()
			t.Cleanup(func() { _ = server.Close() })

			address := serve(t, c.run,
				"-upstream.url", "http://"+listener.Addr().String()+"/graphql",
				"-allowlist", dir,
				"-upstream.max-conn-lifetime", "100ms",
			)

			// Enough requests spread over enough time for the lifetime to fall
			// due more than once.
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if code, body := post(t, address, docAllowed); code != http.StatusOK {
					t.Fatalf("expected 200; received %d: %s", code, body)
				}
				time.Sleep(20 * time.Millisecond)
			}

			// Without recycling one connection carries all of it. With it, the
			// pool turns over, so the upstream sees several.
			if n := conns.Load(); n < 2 {
				t.Errorf("expected the pool to turn over; the upstream accepted %d "+
					"connection(s)", n)
			}
		})
	}
}

// countingListener counts what it accepts.
type countingListener struct {
	net.Listener
	conns *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.conns.Add(1)
	}
	return conn, err
}
