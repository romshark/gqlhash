package acceptance

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// rawUpstream is an API that writes whatever respond writes,
// so a test can hand the proxy an answer no HTTP server would produce.
func rawUpstream(t *testing.T, respond func(conn net.Conn)) (url string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				// Read the request, whatever it is, before answering.
				_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				buf := make([]byte, 4096)
				_, _ = conn.Read(buf)
				respond(conn)
			}()
		}
	}()
	return "http://" + listener.Addr().String() + "/graphql"
}

// TestKeepAliveReuse covers two requests on one connection,
// which is how every client sends them. The second must be decided on its own:
// a buffer kept between them, or a body left on the connection,
// shows up as one request answered with another's verdict.
func TestKeepAliveReuse(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// Allowed, refused, allowed again, over one connection,
		// with the bodies differing in length so a buffer kept between them would show.
		for i, tc := range []struct {
			body   string
			expect string
		}{
			{docAllowed, "HTTP/1.1 200"},
			{docRejected, "HTTP/1.1 403"},
			{`{"query":"` + allowedText + `","variables":{"padding":"` +
				strings.Repeat("p", 300) + `"}}`, "HTTP/1.1 200"},
			{`not json`, "HTTP/1.1 400"},
			{docAllowed, "HTTP/1.1 200"},
		} {
			if _, err := fmt.Fprintf(conn,
				"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
				len(tc.body), tc.body,
			); err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
			status, body := readAnswer(t, conn)
			if !strings.HasPrefix(status, tc.expect) {
				t.Fatalf("request %d: expected %s; received %q", i, tc.expect, status)
			}
			_ = body
		}
	})
}

// readAnswer reads one HTTP answer off conn, headers and the body its
// Content-Length names.
func readAnswer(t *testing.T, conn net.Conn) (status, body string) {
	t.Helper()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		head, rest, done := strings.Cut(string(buf), "\r\n\r\n")
		if done {
			status, _, _ = strings.Cut(head, "\r\n")
			length := 0
			for line := range strings.SplitSeq(head, "\r\n") {
				name, value, ok := strings.Cut(line, ":")
				if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
					_, _ = fmt.Sscanf(strings.TrimSpace(value), "%d", &length)
				}
			}
			if len(rest) >= length {
				return status, rest[:length]
			}
		}
		if err != nil {
			t.Fatalf("reading the answer: %v; had %q", err, buf)
		}
	}
}

// TestBrokenUpstreamAnswer covers an API that answers something no client can
// use: bytes that are no HTTP answer, and a body shorter than it declared.
// Neither may reach a client as a whole answer.
func TestBrokenUpstreamAnswer(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		t.Run("no HTTP answer at all", func(t *testing.T) {
			url := rawUpstream(t, func(conn net.Conn) {
				_, _ = conn.Write([]byte("\x00\x01 this is no status line\r\n\r\n"))
			})
			s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir)
			code, answer := post(t, s, docAllowed)
			if code != http.StatusBadGateway {
				t.Errorf("expected 502; received %d: %s", code, answer)
			}
		})

		// A body shorter than its Content-Length. What a client must not get is
		// a complete-looking answer: one command sees the truncation before it
		// writes anything and answers 502, the other has already begun
		// streaming and can only break the connection.
		t.Run("a body cut off", func(t *testing.T) {
			url := rawUpstream(t, func(conn net.Conn) {
				_, _ = conn.Write([]byte(
					"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" +
						"Content-Length: 4096\r\n\r\n{\"data\":"))
			})
			s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir)

			client := &http.Client{Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			res, err := client.Post("http://"+s.address+"/graphql",
				"application/json", strings.NewReader(docAllowed))
			if err != nil {
				return // The answer never completed, which is the point.
			}
			defer func() { _ = res.Body.Close() }()
			body, err := io.ReadAll(res.Body)
			if res.StatusCode == http.StatusOK && err == nil {
				t.Errorf("expected no whole answer; received %d and %q",
					res.StatusCode, body)
			}
		})
	})
}

// TestReadHeaderTimeout covers a client that opens a connection and dribbles:
// the request is given up on rather than held, which is what keeps a few
// thousand idle sockets from being an outage.
func TestReadHeaderTimeout(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// Both timeouts: one command bounds the headers,
		// the other the whole request, and the client below sends neither.
		e := newEnv(t, tgt, []string{allowedDoc},
			"-server.read-header-timeout", "300ms",
			"-server.read-timeout", "300ms")

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := fmt.Fprint(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\n"); err != nil {
			t.Fatal(err)
		}

		// Whatever it answers, it stops waiting: what must not happen is the
		// connection being held for as long as the client keeps it open.
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := conn.Read(make([]byte, 64)); err != nil &&
			!strings.Contains(err.Error(), "EOF") {
			t.Fatalf("expected it given up on; received %v", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("expected it given up on within the timeout; took %s", took)
		}
	})
}
