package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRunProxyFlagErrors(t *testing.T) {
	f := func(t *testing.T, expectCode int, args ...string) {
		t.Helper()
		var out, errOut strings.Builder
		code := Run(context.Background(), "gqlhash-proxy", "dev",
			append([]string{"gqlhash-proxy"}, args...), &out, &errOut)
		if code != expectCode {
			t.Errorf("expected code %d; received %d; args %v; stderr: %s",
				expectCode, code, args, errOut.String())
		}
	}

	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	// The flags are validated by the config package, which tests every value.
	// What's left here is what only a run can fail at.
	f(t, 2, "-upstream", "http://x", "-allowlist", dir, "-log-level", "nope")

	// A directory that can't be read stops the start. An empty one, or one
	// holding only documents that don't parse, doesn't: it serves and rejects
	// everything, which the allowlist tests cover.
	f(t, 1, "-upstream", "http://x", "-allowlist", filepath.Join(dir, "nope"))

	// An address already in use is a start failure, not a panic.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	f(t, 1, "-upstream", "http://x", "-allowlist", dir,
		"-listen", held.Addr().String())

	var out, errOut strings.Builder
	if code := Run(context.Background(), "gqlhash-proxy", "1.2.3-test",
		[]string{"gqlhash-proxy", "-version"}, &out, &errOut); code != 0 {
		t.Errorf("expected code 0; received %d", code)
	}
	if !strings.Contains(out.String(), "gqlhash-proxy v1.2.3-test") {
		t.Errorf("expected a version; received %q", out.String())
	}
}

// syncBuffer collects log output while the server writes to it from its own
// goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freePort returns an address nothing listens on yet.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// serve starts the proxy on a port of its own and returns the address it
// reports, which is the only way to learn the port behind -listen :0.
func serve(t *testing.T, args ...string) (address string, logs *syncBuffer) {
	t.Helper()
	logs = start(t, append([]string{"-listen", "127.0.0.1:0"}, args...)...)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a := findLogged(logs.String(), "listening", "address"); a != "" {
			return a, logs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the proxy didn't report an address: %s", logs.String())
	return "", logs
}

// start runs the proxy until the test ends.
func start(t *testing.T, args ...string) (logs *syncBuffer) {
	t.Helper()
	logs = new(syncBuffer)
	ctx, cancel := context.WithCancel(context.Background())

	stopped := make(chan int, 1)
	go func() {
		stopped <- Run(ctx, "gqlhash-proxy", "dev", append([]string{"gqlhash-proxy"}, args...),
			io.Discard, logs)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-stopped:
			if code != 0 {
				t.Errorf("expected the proxy to stop cleanly; received %d: %s",
					code, logs.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("the proxy didn't stop")
		}
	})
	return logs
}

// findLogged returns a field of the first log event with the given message.
func findLogged(logs, message, field string) string {
	for line := range strings.SplitSeq(logs, "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event["message"] != message {
			continue
		}
		if v, ok := event[field].(string); ok {
			return v
		}
	}
	return ""
}

// upstreamServer answers like a GraphQL API and records what it received.
func upstreamServer(t *testing.T) (*url.URL, *upstreamSpy) {
	t.Helper()
	spy := new(upstreamSpy)
	mux := http.NewServeMux()
	mux.Handle("/graphql", spy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return mustURL(t, server.URL+"/graphql"), spy
}

// TestRunProxyEndToEnd drives the proxy over real HTTP, from its flags to
// the upstream API.
func TestRunProxyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "get-user.graphql",
		"query GetUser {\n  # the allowed operation\n  user(id: 1) {\n    name\n  }\n}")

	upstream, spy := upstreamServer(t)
	address, logs := serve(t, "-upstream", upstream.String(), "-allowlist", dir,
		"-status", "/healthz", "-log-requests", "-log-level", "debug")
	client := &http.Client{Timeout: 10 * time.Second}
	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		res, err := client.Post("http://"+address+"/graphql",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		answer, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(answer)
	}

	// The allowed document, however it's written.
	for _, body := range []string{
		`{"query":"query GetUser{user(id:1){name}}"}`,
		`{"query":"query GetUser {\n  user(id: 1) {\n    name\n  }\n}"}`,
		`{"operationName":"GetUser","query":"query GetUser{user(id:1){name}}",` +
			`"variables":{"x":[1,2,3]}}`,
	} {
		code, answer := post(t, body)
		if code != http.StatusOK {
			t.Errorf("expected 200; received %d; body: %s", code, body)
		}
		if answer != upstreamAnswer {
			t.Errorf("expected the upstream answer; received %s", answer)
		}
		// The path of -upstream is the endpoint, whatever the client used.
		if spy.LastPath != "/graphql" {
			t.Errorf("expected /graphql upstream; received %s", spy.LastPath)
		}
		// The body upstream receives is the one the client sent.
		if spy.LastBody != body {
			t.Errorf("expected upstream to receive %s; received %s", body, spy.LastBody)
		}
	}

	// One that isn't allowed, and the anonymous form of the allowed one, which
	// is a different document.
	for _, body := range []string{
		`{"query":"query GetUser{user(id:1){email}}"}`,
		`{"query":"{user(id:1){name}}"}`,
	} {
		code, answer := post(t, body)
		if code != http.StatusForbidden {
			t.Errorf("expected 403; received %d; body: %s", code, body)
		}
		if !strings.Contains(answer, "OPERATION_NOT_ALLOWED") {
			t.Errorf("expected a GraphQL error; received %s", answer)
		}
	}

	if code, _ := post(t, "nope"); code != http.StatusBadRequest {
		t.Errorf("expected 400; received %d", code)
	}

	// A GET request carries the document in the query string.
	res, err := client.Get("http://" + address +
		"/graphql?query=query%20GetUser%7Buser(id%3A1)%7Bname%7D%7D")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET: expected 200; received %d", res.StatusCode)
	}

	// The status endpoint counts what happened.
	res, err = client.Get("http://" + address + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Documents int    `json:"documents"`
		LoadedAt  string `json:"loaded_at"`
		Allowed   int    `json:"allowed"`
		Rejected  int    `json:"rejected"`
		Malformed int    `json:"malformed"`
	}
	errDecode := json.NewDecoder(res.Body).Decode(&status)
	_ = res.Body.Close()
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if status.Documents != 1 || status.Allowed != 4 ||
		status.Rejected != 2 || status.Malformed != 1 {
		t.Errorf("unexpected status: %+v", status)
	}
	if status.LoadedAt == "" {
		t.Error("expected a load time")
	}

	// A rejection is logged, and -log-requests logs what was forwarded.
	if !strings.Contains(logs.String(), "not on the allowlist") {
		t.Error("expected a rejection to be logged")
	}
	if !strings.Contains(logs.String(), "forwarding") {
		t.Error("expected -log-requests to log a forwarded request")
	}
}

// TestRunProxyEndToEndWatch covers the one flag whose behaviour needs files on
// disk and a running server. Every other flag is covered by TestBuildWiring and
// the handler tests.
func TestRunProxyEndToEndWatch(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)
	client := &http.Client{Timeout: 10 * time.Second}

	post := func(t *testing.T, address, body string) int {
		t.Helper()
		res, err := client.Post("http://"+address+"/graphql",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}

	address, logs := serve(t, "-upstream", upstream.String(),
		"-allowlist", dir, "-watch", "-watch-interval", "20ms")

	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusForbidden {
		t.Errorf("expected 403 before the document exists; received %d", code)
	}
	writeDoc(t, dir, "new.graphql", "{ new }")
	waitFor(t, func() bool {
		return post(t, address, `{"query":"{new}"}`) == http.StatusOK
	}, "the new document to become allowed")

	if err := os.Remove(filepath.Join(dir, "a.graphql")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return post(t, address, `{"query":"{a}"}`) == http.StatusForbidden
	}, "the removed document to stop being allowed")

	// A broken document is skipped, the working ones keep being served.
	writeDoc(t, dir, "broken.graphql", "{ unterminated")
	time.Sleep(200 * time.Millisecond)
	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusOK {
		t.Errorf("expected the working document to stay allowed; received %d", code)
	}
	if !strings.Contains(logs.String(), "skipping a document that doesn't parse") {
		t.Errorf("expected the skip to be logged; received %s", logs.String())
	}
}

func TestWriteStatus(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	store, loader := newTestLoader(t, dir, false)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	p := NewProxy(store, mustURL(t, "http://x"), sha256.New,
		ProxyConfig{MaxBody: 1 << 20}, nil, testLogger())
	p.counters.Allowed.Add(2)
	p.counters.Rejected.Add(1)
	p.counters.Upstream.Add(3)

	w := httptest.NewRecorder()
	writeStatus(w, store, p)
	body := w.Body.String()
	for _, want := range []string{
		`"documents":1`, `"allowed":2`, `"rejected":1`, `"malformed":0`,
		`"upstream_errors":3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in %s", want, body)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("expected a JSON content type; received %q", ct)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Errorf("expected valid JSON: %v", err)
	}
}

// TestRunProxyMetrics covers the -metrics endpoint end to end.
func TestRunProxyMetrics(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)
	client := &http.Client{Timeout: 10 * time.Second}

	metricsAddress := freePort(t)
	address, _ := serve(t, "-upstream", upstream.String(), "-allowlist", dir,
		"-metrics", metricsAddress)

	post := func(t *testing.T, body string) int {
		t.Helper()
		res, err := client.Post("http://"+address+"/graphql",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}

	if code := post(t, `{"query":"{a}"}`); code != http.StatusOK {
		t.Fatalf("expected 200; received %d", code)
	}
	for range 2 {
		if code := post(t, `{"query":"{nope}"}`); code != http.StatusForbidden {
			t.Fatalf("expected 403; received %d", code)
		}
	}
	if code := post(t, "not json"); code != http.StatusBadRequest {
		t.Fatalf("expected 400; received %d", code)
	}

	res, err := client.Get("http://" + metricsAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /metrics; received %d", res.StatusCode)
	}
	exposition := string(body)

	for _, want := range []string{
		`gqlhash_proxy_requests_total{decision="allowed"} 1`,
		`gqlhash_proxy_requests_total{decision="rejected"} 2`,
		`gqlhash_proxy_requests_total{decision="malformed"} 1`,
		`gqlhash_proxy_upstream_errors_total 0`,
		`gqlhash_proxy_allowlist_documents 1`,
		"gqlhash_proxy_allowlist_loaded_timestamp_seconds",
		// The duration of a request is measured per decision.
		`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`,
		`gqlhash_proxy_request_duration_seconds_count{decision="rejected"} 2`,
		// The Go collector comes along, which is what an operator wants.
		"go_goroutines",
	} {
		if !strings.Contains(exposition, want) {
			t.Errorf("expected %q in the exposition", want)
		}
	}

	// The metrics port serves nothing else.
	res, err = client.Get("http://" + metricsAddress + "/graphql")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 outside /metrics; received %d", res.StatusCode)
	}

	// The traffic port serves no metrics, so a scrape target isn't reachable
	// where the clients are. /metrics there is just a request without a document.
	res, err = client.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	leaked, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(leaked), "gqlhash_proxy_requests_total") {
		t.Errorf("expected no exposition on the traffic port; received %s", leaked)
	}

}

// TestRunProxyMetricsDisabled covers what happens without -metrics: the proxy
// serves, and nothing exposes an exposition anywhere.
func TestRunProxyMetricsDisabled(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)
	client := &http.Client{Timeout: 10 * time.Second}

	address, logs := serve(t, "-upstream", upstream.String(), "-allowlist", dir)

	res, err := client.Post("http://"+address+"/graphql", "application/json",
		strings.NewReader(`{"query":"{a}"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200; received %d", res.StatusCode)
	}

	// Nothing is served on the traffic port either.
	res, err = client.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if strings.Contains(string(body), "gqlhash_proxy_requests_total") {
		t.Errorf("expected no exposition; received %s", body)
	}

	// The startup says metrics are off, and no second address is reported.
	if !strings.Contains(logs.String(), `"metrics":false`) {
		t.Errorf("expected metrics to be reported as off; received %s", logs.String())
	}
	if strings.Contains(logs.String(), "serving metrics") {
		t.Errorf("expected no metrics listener; received %s", logs.String())
	}
}

// TestRunProxyMetricsUpstreamErrors covers the one counter the other metrics test
// can only see at zero.
func TestRunProxyMetricsUpstreamErrors(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	client := &http.Client{Timeout: 10 * time.Second}

	metricsAddress := freePort(t)
	// An upstream nothing listens on, so an allowed document fails to forward.
	address, _ := serve(t, "-upstream", "http://127.0.0.1:1/graphql",
		"-allowlist", dir, "-metrics", metricsAddress)

	res, err := client.Post("http://"+address+"/graphql", "application/json",
		strings.NewReader(`{"query":"{a}"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502; received %d", res.StatusCode)
	}

	res, err = client.Get("http://" + metricsAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	exposition := string(body)
	if !strings.Contains(exposition, "gqlhash_proxy_upstream_errors_total 1") {
		t.Errorf("expected an upstream error to be counted; received %s", exposition)
	}
	// The decision was to allow it, so that's the label of its duration.
	if !strings.Contains(exposition,
		`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`) {
		t.Errorf("expected the request to be timed as allowed; received %s",
			exposition)
	}
}

// TestRunProxyMetricsAddressInUse makes sure a metrics address that can't be
// bound stops the start instead of serving without metrics.
func TestRunProxyMetricsAddressInUse(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	var out, errOut strings.Builder
	code := Run(context.Background(), "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
		"-listen", "127.0.0.1:0",
		"-upstream", "http://127.0.0.1:1/graphql",
		"-allowlist", dir,
		"-metrics", held.Addr().String(),
	}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected code 1; received %d; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "listening for metrics") {
		t.Errorf("expected the metrics bind to be reported; received %s",
			errOut.String())
	}
}

// TestRunProxyGracefulShutdown asserts that a request in flight when the
// shutdown starts is answered instead of being cut off.
func TestRunProxyGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	// An upstream that answers slowly, so a request is certainly in flight when
	// the shutdown starts.
	entered := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamAnswer)
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	address := freePort(t)
	logs := new(syncBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan int, 1)
	go func() {
		stopped <- Run(ctx, "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
			"-listen", address,
			"-upstream", upstream.URL + "/graphql",
			"-allowlist", dir,
			"-shutdown-timeout", "10s",
		}, io.Discard, logs)
	}()
	waitFor(t, func() bool {
		return strings.Contains(logs.String(), "listening")
	}, "the proxy to listen")

	answered := make(chan int, 1)
	go func() {
		client := &http.Client{Timeout: 20 * time.Second}
		res, err := client.Post("http://"+address+"/graphql", "application/json",
			strings.NewReader(`{"query":"{a}"}`))
		if err != nil {
			answered <- 0
			return
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		answered <- res.StatusCode
	}()

	<-entered // The request reached the upstream.
	cancel()  // A signal would do this.

	// The proxy waits for the request instead of dropping it.
	select {
	case code := <-stopped:
		t.Fatalf("the proxy stopped before the request was answered: %d", code)
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	if code := <-answered; code != http.StatusOK {
		t.Errorf("expected the request to be answered with 200; received %d", code)
	}
	select {
	case code := <-stopped:
		if code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy didn't stop")
	}
	if !strings.Contains(logs.String(), "shutting down") {
		t.Errorf("expected the shutdown to be logged; received %s", logs.String())
	}
}

// TestRunProxyShutdownTimeout asserts that the wait is bounded and reported. Past
// -shutdown-timeout whatever is still running is abandoned.
func TestRunProxyShutdownTimeout(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	entered := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	})
	upstream := httptest.NewServer(mux)
	// The handler is released before the server is closed: Close waits for the
	// requests in flight, so the other order deadlocks.
	defer upstream.Close()
	defer close(release)

	address := freePort(t)
	logs := new(syncBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan int, 1)
	go func() {
		stopped <- Run(ctx, "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
			"-listen", address,
			"-upstream", upstream.URL + "/graphql",
			"-allowlist", dir,
			"-shutdown-timeout", "100ms",
		}, io.Discard, logs)
	}()
	waitFor(t, func() bool {
		return strings.Contains(logs.String(), "listening")
	}, "the proxy to listen")

	go func() {
		client := &http.Client{Timeout: 20 * time.Second}
		res, err := client.Post("http://"+address+"/graphql", "application/json",
			strings.NewReader(`{"query":"{a}"}`))
		if err == nil {
			_ = res.Body.Close()
		}
	}()

	<-entered
	cancel()

	select {
	case code := <-stopped:
		if code != 1 {
			t.Errorf("expected code 1 for an exceeded timeout; received %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy didn't stop")
	}
	if !strings.Contains(logs.String(), "shutting down") {
		t.Errorf("expected the timeout to be logged; received %s", logs.String())
	}
}

// TestBuildWiring asserts that every field of a config reaches the component it
// configures. A swapped field is what this catches, which no behavioural test
// would show as anything but a puzzling result.
func TestBuildWiring(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	// Every value differs from its default, so a field that reads the wrong
	// source is visible.
	cfg := config.Proxy{
		Listen:         "127.0.0.1:0",
		Upstream:       mustURL(t, "http://upstream.example:4000/graphql"),
		Allowlist:      dir,
		Watch:          true,
		WatchInterval:  7 * time.Second,
		Hash:           config.HashFunctionBLAKE3,
		Ignore:         gqlhash.IgnoreInputs,
		Exact:          false,
		MaxBody:        4096,
		AllowBatch:     true,
		OpaqueErrors:   true,
		LogRequests:    true,
		TrustForwarded: true,
		LogLevel:       "debug",
		LogJSON:        true,
		Timeout:        11 * time.Second,
		Shutdown:       13 * time.Second,
		Status:         "/healthz",
		Metrics:        "127.0.0.1:0",

		MaxIdleConnsPerHost: 7,
		MaxIdleConns:        9,
		HTTP2:               false,
	}

	log, ok := newLogger(cfg, io.Discard)
	if !ok {
		t.Fatal("expected the log level to parse")
	}
	c, err := build(cfg, log)
	if err != nil {
		t.Fatal(err)
	}

	// The handler.
	p := c.Proxy
	if p.maxBody != cfg.MaxBody {
		t.Errorf("MaxBody: expected %d; received %d", cfg.MaxBody, p.maxBody)
	}
	if p.options.Ignore != cfg.Ignore {
		t.Errorf("Ignore: expected %v; received %v", cfg.Ignore, p.options.Ignore)
	}
	if p.exact != cfg.Exact || p.allowBatch != cfg.AllowBatch ||
		p.opaqueErrors != cfg.OpaqueErrors ||
		p.trustForwarded != cfg.TrustForwarded {
		t.Errorf("a switch didn't reach the handler: %+v", p)
	}
	// -log-requests only logs where the level keeps a debug event.
	if !p.logRequests || !p.debug {
		t.Errorf("expected debug logging of requests; debug %t requests %t",
			p.debug, p.logRequests)
	}
	if p.metrics == nil {
		t.Error("expected metrics, -metrics names an address")
	}

	// The loader, and the hash function it keys the allowlist by.
	if c.Loader.dir != cfg.Allowlist {
		t.Errorf("Allowlist: expected %q; received %q", cfg.Allowlist, c.Loader.dir)
	}
	if c.Loader.exact != cfg.Exact {
		t.Errorf("Exact: expected %t; received %t", cfg.Exact, c.Loader.exact)
	}
	h := config.NewHasher(cfg.Hash)
	sum, errHash := gqlhash.AppendHash(nil, h,
		gqlhash.Options{Ignore: cfg.Ignore}, "{ a }")
	if errHash.IsErr() {
		t.Fatal(errHash)
	}
	if c.Store.Load().Lookup(sum) == nil {
		t.Error("expected the allowlist to be keyed by -hash and -ignore")
	}

	// The servers.
	if c.Server.Addr != cfg.Listen {
		t.Errorf("Listen: expected %q; received %q", cfg.Listen, c.Server.Addr)
	}
	if c.Server.WriteTimeout != cfg.Timeout+10*time.Second {
		t.Errorf("Timeout: expected %v; received %v",
			cfg.Timeout+10*time.Second, c.Server.WriteTimeout)
	}
	if c.Metrics == nil {
		t.Fatal("expected a metrics server")
	}
	if c.Metrics.Addr != cfg.Metrics {
		t.Errorf("Metrics: expected %q; received %q", cfg.Metrics, c.Metrics.Addr)
	}
	if c.Metrics.Addr == c.Server.Addr && cfg.Metrics != cfg.Listen {
		t.Error("expected the metrics server on its own address")
	}
	transport, okT := c.Proxy.upstream.Transport.(*http.Transport)
	if !okT {
		t.Fatalf("expected an *http.Transport; received %T",
			c.Proxy.upstream.Transport)
	}
	if transport.ResponseHeaderTimeout != cfg.Timeout {
		t.Errorf("Timeout: expected %v upstream; received %v",
			cfg.Timeout, transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != cfg.MaxIdleConnsPerHost ||
		transport.MaxIdleConns != cfg.MaxIdleConns ||
		transport.ForceAttemptHTTP2 != cfg.HTTP2 {
		t.Errorf("the upstream connection settings didn't reach the transport: "+
			"%+v", transport)
	}

	// -status serves the status, and the traffic route still answers.
	rec := httptest.NewRecorder()
	c.Server.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, cfg.Status, nil))
	if !strings.Contains(rec.Body.String(), `"documents":1`) {
		t.Errorf("expected the status on %s; received %s", cfg.Status, rec.Body)
	}

	// Without -metrics and -status there is no second server and no extra route.
	cfg.Metrics, cfg.Status = "", ""
	c2, err := build(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Metrics != nil {
		t.Error("expected no metrics server")
	}
	if c2.Proxy.metrics != nil {
		t.Error("expected no metrics in the handler")
	}
}

// TestNewLogger covers the flags the logger is built from.
func TestNewLogger(t *testing.T) {
	var out strings.Builder
	log, ok := newLogger(config.Proxy{LogLevel: "info", LogJSON: true}, &out)
	if !ok {
		t.Fatal("expected info to parse")
	}
	log.Info().Str("k", "v").Msg("hello")
	if !strings.Contains(out.String(), `"message":"hello"`) {
		t.Errorf("expected JSON; received %q", out.String())
	}

	// The readable format is no JSON.
	out.Reset()
	log, ok = newLogger(config.Proxy{LogLevel: "info", LogJSON: false}, &out)
	if !ok {
		t.Fatal("expected info to parse")
	}
	log.Info().Msg("hello")
	if strings.Contains(out.String(), `"message"`) {
		t.Errorf("expected text; received %q", out.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("expected the message; received %q", out.String())
	}

	// The level drops what's below it.
	out.Reset()
	log, ok = newLogger(config.Proxy{LogLevel: "error", LogJSON: true}, &out)
	if !ok {
		t.Fatal("expected error to parse")
	}
	log.Info().Msg("dropped")
	log.Error().Msg("kept")
	if strings.Contains(out.String(), "dropped") {
		t.Errorf("expected info to be dropped; received %q", out.String())
	}
	if !strings.Contains(out.String(), "kept") {
		t.Errorf("expected the error; received %q", out.String())
	}

	// An unknown level is reported to stderr, not guessed at.
	out.Reset()
	if _, ok := newLogger(config.Proxy{LogLevel: "shout"}, &out); ok {
		t.Error("expected an unknown level to fail")
	}
	if !strings.Contains(out.String(), `unsupported log level "shout"`) {
		t.Errorf("expected the level in the message; received %q", out.String())
	}
}
