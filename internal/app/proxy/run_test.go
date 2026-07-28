package proxy

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

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
			append([]string{"gqlhash-proxy", "-control.listen", "127.0.0.1:0"}, args...),
			&out, &errOut)
		if code != expectCode {
			t.Errorf("expected code %d; received %d; args %v; stderr: %s",
				expectCode, code, args, errOut.String())
		}
	}

	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	// The flags are validated by the config package, which tests every value.
	// What's left here is what only a run can fail at.
	f(t, 2, "-upstream.url", "http://x", "-allowlist", dir, "-log.level", "nope")

	// A directory that can't be read stops the start. An empty one, or one holding
	// only documents that don't parse, doesn't: it serves and rejects everything,
	// which the allowlist tests cover.
	f(t, 1, "-upstream.url", "http://x", "-allowlist", filepath.Join(dir, "nope"))

	// An address already in use is a start failure, not a panic.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	f(t, 1, "-upstream.url", "http://x", "-allowlist", dir,
		"-server.listen", held.Addr().String())

	var out, errOut strings.Builder
	if code := Run(context.Background(), "gqlhash-proxy", "1.2.3-test",
		[]string{"gqlhash-proxy", "-version"}, &out, &errOut); code != 0 {
		t.Errorf("expected code 0; received %d", code)
	}
	if !strings.Contains(out.String(), "gqlhash-proxy v1.2.3-test") {
		t.Errorf("expected a version; received %q", out.String())
	}
}

// syncBuffer collects log output while the server writes to it from its own goroutines.
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

// serve starts the proxy on a port of its own and returns the address it reports,
// which is the only way to learn the port behind -server.listen :0.
func serve(t *testing.T, args ...string) (address string, logs *syncBuffer) {
	t.Helper()
	logs = start(t, append([]string{
		"-server.listen", "127.0.0.1:0", "-control.listen", "127.0.0.1:0",
	}, args...)...)

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

// TestRunProxyEndToEnd drives the proxy over real HTTP,
// from its flags to the upstream API.
func TestRunProxyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "get-user.graphql",
		"query GetUser {\n  # the allowed operation\n  user(id: 1) {\n    name\n  }\n}")

	upstream, spy := upstreamServer(t)
	controlAddress := freePort(t)
	address, logs := serve(t, "-upstream.url", upstream.String(), "-allowlist", dir,
		"-control.listen", controlAddress, "-log.requests", "-log.level", "debug")
	post := func(t *testing.T, body string) (int, string) {
		t.Helper()
		return postGraphQL(t, address, body)
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
		// The path of -upstream.url is the endpoint, whatever the client used.
		if spy.lastPath != "/graphql" {
			t.Errorf("expected /graphql upstream; received %s", spy.lastPath)
		}
		// The body upstream receives is the one the client sent.
		if spy.lastBody != body {
			t.Errorf("expected upstream to receive %s; received %s", body, spy.lastBody)
		}
	}

	// One that isn't allowed, and the anonymous form of the allowed one,
	// which is a different document.
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
	if code, _ := get(t, "http://"+address+
		"/graphql?query=query%20GetUser%7Buser(id%3A1)%7Bname%7D%7D"); code !=
		http.StatusOK {
		t.Errorf("GET: expected 200; received %d", code)
	}

	// The status endpoint of the control server counts what happened. It's no
	// route of the traffic port: that one forwards everything it doesn't reject.
	_, statusBody := get(t, "http://"+controlAddress+"/status")
	var status struct {
		Documents int    `json:"documents"`
		LoadedAt  string `json:"loaded_at"`
		Allowed   int    `json:"allowed"`
		Rejected  int    `json:"rejected"`
		Malformed int    `json:"malformed"`
	}
	if err := json.Unmarshal([]byte(statusBody), &status); err != nil {
		t.Fatal(err)
	}
	if status.Documents != 1 || status.Allowed != 4 ||
		status.Rejected != 2 || status.Malformed != 1 {
		t.Errorf("unexpected status: %+v", status)
	}
	if status.LoadedAt == "" {
		t.Error("expected a load time")
	}
	if _, trafficBody := get(t, "http://"+address+"/status"); strings.Contains(
		trafficBody, `"documents"`,
	) {
		t.Errorf("expected no status on the traffic port; received %s", trafficBody)
	}

	// A rejection is logged, and -log.requests logs what was forwarded.
	if !strings.Contains(logs.String(), "not on the allowlist") {
		t.Error("expected a rejection to be logged")
	}
	if !strings.Contains(logs.String(), "forwarding") {
		t.Error("expected -log.requests to log a forwarded request")
	}
}

// TestRunProxyReload covers the control server end to end: files on disk,
// a running proxy and a reload request.
// Every other flag is covered by TestBuildWiring and the handler tests.
func TestRunProxyReload(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)
	post := func(t *testing.T, address, body string) int {
		t.Helper()
		code, _ := postGraphQL(t, address, body)
		return code
	}
	controlAddress := freePort(t)
	reload := func(t *testing.T, method, token string) (int, string) {
		t.Helper()
		return send(t, requestTo(t, method, controlAddress, "/reload", token))
	}

	// The token has no flag: the environment is the only way to give it.
	t.Setenv(config.EnvName("control-token"), "s3cret")
	address, logs := serve(t, "-upstream.url", upstream.String(),
		"-allowlist", dir, "-control.listen", controlAddress)

	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusForbidden {
		t.Errorf("expected 403 before the document exists; received %d", code)
	}
	writeDoc(t, dir, "new.graphql", "{ new }")

	// A document on disk waits for a reload,
	// and a reload needs the token and the method.
	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusForbidden {
		t.Errorf("expected 403 before the reload; received %d", code)
	}
	if code, _ := reload(t, http.MethodGet, "s3cret"); code !=
		http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET; received %d", code)
	}
	if code, _ := reload(t, http.MethodPost, ""); code != http.StatusUnauthorized {
		t.Errorf("expected 401 without the token; received %d", code)
	}
	if code, _ := reload(t, http.MethodPost, "wrong"); code !=
		http.StatusUnauthorized {
		t.Errorf("expected 401 for a wrong token; received %d", code)
	}
	if code, body := reload(t, http.MethodPost, "s3cret"); code != http.StatusOK ||
		!strings.Contains(body, `"total":2`) ||
		!strings.Contains(body, "new.graphql") {
		t.Errorf("expected 200 and the two loaded files; received %d %s",
			code, body)
	}
	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusOK {
		t.Errorf("expected the new document after the reload; received %d", code)
	}

	// A removed document stops being allowed once reloaded.
	if err := os.Remove(filepath.Join(dir, "a.graphql")); err != nil {
		t.Fatal(err)
	}
	if code, _ := reload(t, http.MethodPost, "s3cret"); code != http.StatusOK {
		t.Fatalf("reload: received %d", code)
	}
	if code := post(t, address, `{"query":"{a}"}`); code != http.StatusForbidden {
		t.Errorf("expected the removed document to be rejected; received %d", code)
	}

	// A broken document is skipped, the working ones keep being served.
	writeDoc(t, dir, "broken.graphql", "{ unterminated")
	code, body := reload(t, http.MethodPost, "s3cret")
	if code != http.StatusOK || !strings.Contains(body, "new.graphql") {
		t.Errorf("expected the broken document to be skipped; received %d %s",
			code, body)
	}
	// The answer names it, so a deployment doesn't have to read the log.
	if !strings.Contains(body, `"total":1`) ||
		!strings.Contains(body, "broken.graphql:1:15") {
		t.Errorf("expected the skipped document in the answer; received %s", body)
	}
	if code := post(t, address, `{"query":"{new}"}`); code != http.StatusOK {
		t.Errorf("expected the working document to stay allowed; received %d", code)
	}
	// The allowlist reports the skip, the proxy is what logs it,
	// and the reason travels as the error of the event rather than as its message.
	if !strings.Contains(logs.String(), `"message":"skipping a document"`) ||
		!strings.Contains(logs.String(), "broken.graphql:1:15") {
		t.Errorf("expected the skip to be logged; received %s", logs.String())
	}

	// The token guards the reload and nothing else on that server.
	// A scraper carries no Authorization header, so /metrics and /status have to answer
	// without one even where a token is configured.
	for _, path := range []string{"/metrics", "/status"} {
		code, body := send(t, requestTo(t, http.MethodGet, controlAddress, path, ""))
		if code != http.StatusOK {
			t.Errorf("expected 200 from %s without a token; received %d %s",
				path, code, body)
		}
	}
	// The exposition is the whole point of the scrape, so an empty 200 is no pass.
	if _, exposition := get(
		t, "http://"+controlAddress+"/metrics",
	); !strings.Contains(exposition, "gqlhash_proxy_allowlist_documents") {
		t.Errorf("expected the exposition; received %s", exposition)
	}
}

// TestRunProxyTrafficPortServesNoControl pins that the control endpoints live
// on the control address alone. The traffic port routes nothing but the proxy,
// so a request to one of their paths is read as a GraphQL request and answered as one,
// whatever the path says.
func TestRunProxyTrafficPortServesNoControl(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, spy := upstreamServer(t)
	controlAddress := freePort(t)

	address, _ := serve(t, "-upstream.url", upstream.String(), "-allowlist", dir,
		"-control.listen", controlAddress)

	// An empty body is no GraphQL request, so the proxy answers 400
	// on every one of these paths. A route would answer something else.
	for _, path := range []string{"/reload", "/metrics", "/status"} {
		code, body := send(t, requestTo(t, http.MethodPost, address, path, ""))
		if code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 from the proxy; received %d: %s",
				path, code, body)
		}
		if !strings.Contains(body, `"code":"BAD_REQUEST"`) {
			t.Errorf("%s: expected the answer of the proxy; received %s", path, body)
		}
	}

	// The same paths answer on the control address, which is what makes the point:
	// they exist, just not there.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/status"},
		{http.MethodGet, "/metrics"},
		{http.MethodPost, "/reload"},
	} {
		if code, body := send(t,
			requestTo(t, c.method, controlAddress, c.path, ""),
		); code != http.StatusOK {
			t.Errorf("%s on the control address: expected 200; received %d: %s",
				c.path, code, body)
		}
	}

	// None of it reached the API.
	if spy.requests != 0 {
		t.Errorf("expected nothing to be forwarded; received %d", spy.requests)
	}
}

// TestRunProxyMetrics covers the metrics of the control server end to end.
func TestRunProxyMetrics(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)
	controlAddress := freePort(t)
	address, logs := serve(t, "-upstream.url", upstream.String(), "-allowlist", dir,
		"-control.listen", controlAddress)

	post := func(t *testing.T, body string) int {
		t.Helper()
		code, _ := postGraphQL(t, address, body)
		return code
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

	// The startup reports the address the metrics are served on.
	if got := findLogged(logs.String(), "listening", "control"); got != controlAddress {
		t.Errorf("expected the control address %q in the log; received %q",
			controlAddress, got)
	}

	code, exposition := get(t, "http://"+controlAddress+"/metrics")
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics; received %d", code)
	}

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
	if code, _ := get(
		t, "http://"+controlAddress+"/graphql",
	); code != http.StatusNotFound {
		t.Errorf("expected 404 outside /metrics; received %d", code)
	}

	// The traffic port serves no metrics, so a scrape target isn't reachable
	// where the clients are. /metrics there is just a request without a document.
	if _, leaked := get(t, "http://"+address+"/metrics"); strings.Contains(
		leaked, "gqlhash_proxy_requests_total",
	) {
		t.Errorf("expected no exposition on the traffic port; received %s", leaked)
	}
}

// TestRunProxyMetricsUpstreamErrors covers the one counter the other metrics test can
// only see at zero.
func TestRunProxyMetricsUpstreamErrors(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	controlAddress := freePort(t)
	// An upstream nothing listens on, so an allowed document fails to forward.
	address, _ := serve(t, "-upstream.url", "http://127.0.0.1:1/graphql",
		"-allowlist", dir, "-control.listen", controlAddress)

	if code, _ := postGraphQL(t, address, `{"query":"{a}"}`); code !=
		http.StatusBadGateway {
		t.Fatalf("expected 502; received %d", code)
	}

	_, body := get(t, "http://"+controlAddress+"/metrics")
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

// TestRunProxyControlAddressInUse makes sure a control address that can't be
// bound stops the start instead of serving without it.
func TestRunProxyControlAddressInUse(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	var out, errOut strings.Builder
	code := Run(context.Background(), "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
		"-server.listen", "127.0.0.1:0",
		"-upstream.url", "http://127.0.0.1:1/graphql",
		"-allowlist", dir,
		"-control.listen", held.Addr().String(),
	}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected code 1; received %d; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "listening for the control server") {
		t.Errorf("expected the control bind to be reported; received %s",
			errOut.String())
	}
}

// TestRunProxyGracefulShutdown asserts that a request in flight when
// the shutdown starts is answered instead of being cut off.
func TestRunProxyGracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	// An upstream that answers slowly,
	// so a request is certainly in flight when the shutdown starts.
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
			"-server.listen", address,
			"-upstream.url", upstream.URL + "/graphql",
			"-allowlist", dir,
			"-control.listen", "127.0.0.1:0",
			"-server.shutdown-timeout", "10s",
		}, io.Discard, logs)
	}()
	waitFor(t, func() bool {
		return strings.Contains(logs.String(), "listening")
	}, "the proxy to listen")

	answered := make(chan int, 1)
	go func() {
		answered <- postFrom(address, `{"query":"{a}"}`, 20*time.Second)
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

// TestRunProxyShutdownTimeout asserts that the wait is bounded and reported.
// Past -server.shutdown-timeout whatever is still running is abandoned.
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
	// The handler is released before the server is closed:
	// Close waits for the requests in flight, so the other order deadlocks.
	defer upstream.Close()
	defer close(release)

	address := freePort(t)
	logs := new(syncBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan int, 1)
	go func() {
		stopped <- Run(ctx, "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
			"-server.listen", address,
			"-upstream.url", upstream.URL + "/graphql",
			"-allowlist", dir,
			"-control.listen", "127.0.0.1:0",
			"-server.shutdown-timeout", "100ms",
		}, io.Discard, logs)
	}()
	waitFor(t, func() bool {
		return strings.Contains(logs.String(), "listening")
	}, "the proxy to listen")

	go func() { _ = postFrom(address, `{"query":"{a}"}`, 20*time.Second) }()

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

// TestBuildWiring asserts that every field of a config reaches the component
// it configures. A swapped field is what this catches,
// which no behavioural test would show as anything but a puzzling result.
func TestBuildWiring(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	// Every value differs from its default,
	// so a field that reads the wrong source is visible.
	cfg := config.Proxy{
		AllowlistDir:   dir,
		HashFunc:       config.HashFunctionBLAKE3,
		Ignore:         gqlhash.IgnoreInputs,
		AllowBatch:     true,
		OpaqueErrors:   true,
		TrustForwarded: true,
		Server: config.ProxyServer{
			Listen:          "127.0.0.1:0",
			MaxBody:         4096,
			ShutdownTimeout: 13 * time.Second,
			// Every listener timeout differs from the others,
			// so a field that reads the wrong one is visible.
			ReadHeaderTimeout: 3 * time.Second,
			ReadTimeout:       17 * time.Second,
			WriteTimeout:      23 * time.Second,
			IdleTimeout:       29 * time.Second,
		},
		Upstream: config.ProxyUpstream{
			URL:                 mustURL(t, "http://upstream.example:4000/graphql"),
			Timeout:             11 * time.Second,
			MaxIdleConnsPerHost: 7,
			MaxIdleConns:        9,
			HTTP2:               false,
		},
		Control: config.ProxyControl{Address: "127.0.0.1:0", Token: "s3cret"},
		Log: config.ProxyLog{
			Level: "debug", JSON: true, Requests: true,
		},
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
	p := c.proxy
	if p.maxBody != cfg.Server.MaxBody {
		t.Errorf("MaxBody: expected %d; received %d", cfg.Server.MaxBody, p.maxBody)
	}
	if p.options.Ignore != cfg.Ignore {
		t.Errorf("Ignore: expected %v; received %v", cfg.Ignore, p.options.Ignore)
	}
	if p.allowBatch != cfg.AllowBatch ||
		p.opaqueErrors != cfg.OpaqueErrors ||
		p.trustForwarded != cfg.TrustForwarded {
		t.Errorf("a switch didn't reach the handler: %+v", p)
	}
	// -log.requests only logs where the level keeps a debug event.
	if !p.logRequests || !p.debug {
		t.Errorf("expected debug logging of requests; debug %t requests %t",
			p.debug, p.logRequests)
	}
	if p.metrics == nil {
		t.Error("expected the metrics, -control.listen names an address")
	}

	// The allowlist. A hit for a document of cfg.AllowlistDir under the key of
	// cfg.HashFunc and cfg.Ignore covers the directory, the hash function and
	// the ignore mode in one assertion.
	h, _ := config.NewHasher(cfg.HashFunc)
	sum, errHash := gqlhash.AppendHash(nil, h,
		gqlhash.Options{Ignore: cfg.Ignore}, "{ a }")
	if errHash.IsErr() {
		t.Fatal(errHash)
	}
	if !c.allowlist.Allowed(sum) {
		t.Error("expected the allowlist to be keyed by -hash and -ignore")
	}

	// The servers.
	if c.server.Addr != cfg.Server.Listen {
		t.Errorf("Listen: expected %q; received %q", cfg.Server.Listen, c.server.Addr)
	}
	if c.server.ReadHeaderTimeout != cfg.Server.ReadHeaderTimeout ||
		c.server.ReadTimeout != cfg.Server.ReadTimeout ||
		c.server.WriteTimeout != cfg.Server.WriteTimeout ||
		c.server.IdleTimeout != cfg.Server.IdleTimeout {
		t.Errorf("a listener timeout didn't reach the server: %+v", c.server)
	}
	if c.control == nil {
		t.Fatal("expected a control server, -control.listen names an address")
	}
	if c.control.Addr != cfg.Control.Address {
		t.Errorf("Control: expected %q; received %q",
			cfg.Control.Address, c.control.Addr)
	}
	if c.control.Addr == c.server.Addr && cfg.Control.Address != cfg.Server.Listen {
		t.Error("expected the control server on its own address")
	}
	// It serves the metrics, the reload and the status.
	for _, path := range []string{"/metrics", "/reload", "/status"} {
		if rec := serveTo(
			t, c.control.Handler, http.MethodPost, path,
		); rec.Code == http.StatusNotFound {
			t.Errorf("expected the control server to serve %s", path)
		}
	}
	transport, okT := c.proxy.upstream.Transport.(*http.Transport)
	if !okT {
		t.Fatalf("expected an *http.Transport; received %T",
			c.proxy.upstream.Transport)
	}
	if transport.ResponseHeaderTimeout != cfg.Upstream.Timeout {
		t.Errorf("Timeout: expected %v upstream; received %v",
			cfg.Upstream.Timeout, transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != cfg.Upstream.MaxIdleConnsPerHost ||
		transport.MaxIdleConns != cfg.Upstream.MaxIdleConns ||
		transport.ForceAttemptHTTP2 != cfg.Upstream.HTTP2 {
		t.Errorf("the upstream connection settings didn't reach the transport: "+
			"%+v", transport)
	}

	// The traffic server routes nothing but the proxy.
	if rec := serveTo(
		t, c.server.Handler, http.MethodGet, "/status",
	); strings.Contains(rec.Body.String(), `"documents":1`) {
		t.Errorf("expected no status on the traffic server; received %s", rec.Body)
	}

	// A run always has the second server, and the metrics it exposes. There's no
	// -control.listen that turns it off, which config.ParseProxy enforces and
	// TestParseProxyErrors covers, so nothing downstream hedges against it.
	if c.control == nil {
		t.Fatal("expected a control server")
	}
	if c.control.Addr != cfg.Control.Address {
		t.Errorf("Control.Addr: expected %q; received %q",
			cfg.Control.Address, c.control.Addr)
	}
	if c.proxy.metrics == nil {
		t.Error("expected the proxy to keep metrics")
	}
}

// TestNewLogger covers the flags the logger is built from.
func TestNewLogger(t *testing.T) {
	var out strings.Builder
	log, ok := newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "info", JSON: true},
	}, &out)
	if !ok {
		t.Fatal("expected info to parse")
	}
	log.Info().Str("k", "v").Msg("hello")
	if !strings.Contains(out.String(), `"message":"hello"`) {
		t.Errorf("expected JSON; received %q", out.String())
	}

	// The readable format is no JSON.
	out.Reset()
	log, ok = newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "info", JSON: false},
	}, &out)
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
	log, ok = newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "error", JSON: true},
	}, &out)
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
	if _, ok := newLogger(config.Proxy{Log: config.ProxyLog{Level: "shout"}}, &out); ok {
		t.Error("expected an unknown level to fail")
	}
	if !strings.Contains(out.String(), `unsupported log level "shout"`) {
		t.Errorf("expected the level in the message; received %q", out.String())
	}
}

// TestRunProxyServeFails covers the traffic server failing while it runs, which
// is neither a bind failure nor a shutdown: the listener goes away under it.
// That's the one way out of a run that isn't asked for,
// so it has to be reported and it has to leave a nonzero exit code behind.
func TestRunProxyServeFails(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)

	logs := new(syncBuffer)
	log := zerolog.New(logs)
	cfg := config.Proxy{
		AllowlistDir: dir,
		HashFunc:     config.HashFunctionSHA2,
		Server: config.ProxyServer{
			Listen:          "127.0.0.1:0",
			MaxBody:         1 << 20,
			ShutdownTimeout: 5 * time.Second,
		},
		Upstream: config.ProxyUpstream{URL: upstream, Timeout: 5 * time.Second},
	}
	c, err := build(cfg, log)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// Nothing cancels the context: the run ends because serving ended.
	code := make(chan int, 1)
	go func() {
		code <- c.serveOn(context.Background(), listener, cfg, log)
	}()

	// The listener is taken away from under a server that is already serving,
	// which is what a serving failure looks like: Serve stops with an error that
	// is no ErrServerClosed.
	waitFor(t, func() bool {
		return findLogged(logs.String(), "listening", "address") != ""
	}, "the server to report listening")
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case received := <-code:
		if received != 1 {
			t.Errorf("expected the run to fail; received %d", received)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run didn't end")
	}

	if !strings.Contains(logs.String(), `"message":"serving"`) {
		t.Errorf("expected the failure to be logged; received %s", logs.String())
	}
	// It isn't a shutdown, so nothing says it was one.
	if strings.Contains(logs.String(), "shutting down") {
		t.Errorf("expected no shutdown; received %s", logs.String())
	}
}
