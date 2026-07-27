package proxy

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testProxy returns a proxy allowing the given documents and forwarding to an
// upstream that echoes what it received.
func testProxy(t *testing.T, documents ...string) (*Proxy, *upstreamSpy) {
	t.Helper()
	return testProxyWith(t, func(*Proxy) {}, documents...)
}

func testProxyWith(
	t *testing.T, configure func(*Proxy), documents ...string,
) (*Proxy, *upstreamSpy) {
	t.Helper()
	dir := t.TempDir()
	for i, d := range documents {
		writeDoc(t, dir, string(rune('a'+i))+".graphql", d)
	}

	store, loader := newTestLoader(t, dir, false)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}

	spy := new(upstreamSpy)
	upstream := httptest.NewServer(spy)
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL + "/graphql")
	if err != nil {
		t.Fatal(err)
	}

	p := NewProxy(store, u, sha256.New,
		ProxyConfig{MaxBody: 1 << 20}, http.DefaultTransport, testLogger())
	configure(p)
	return p, spy
}

// upstreamAnswer is what the test upstream answers.
const upstreamAnswer = `{"data":{"ok":true}}`

// upstreamSpy records what reached the upstream API.
type upstreamSpy struct {
	Requests  int
	LastBody  string
	LastPath  string
	LastQuery string
}

func (s *upstreamSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.Requests++
	s.LastBody = string(body)
	s.LastPath = r.URL.Path
	s.LastQuery = r.URL.RawQuery
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

func do(t *testing.T, p *Proxy, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func postJSON(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestProxyAllowed(t *testing.T) {
	p, spy := testProxy(t, "{ user { name } }")

	// The document is allowed however it's formatted.
	for _, body := range []string{
		`{"query":"{ user { name } }"}`,
		`{"query":"{user{name}}"}`,
		`{"query":"{\n  # comment\n  user { name }\n}"}`,
		`{"operationName":"Q","query":"{user{name}}","variables":{"a":[1,2]}}`,
	} {
		w := do(t, p, postJSON(body))
		if w.Code != http.StatusOK {
			t.Errorf("expected 200; received %d; body: %s", w.Code, body)
		}
		if got := w.Body.String(); got != upstreamAnswer {
			t.Errorf("expected the upstream response; received %s", got)
		}
		// The body reaching upstream is the one the client sent.
		if spy.LastBody != body {
			t.Errorf("expected upstream to receive %s; received %s", body, spy.LastBody)
		}
		// The upstream URL names the endpoint, so its path is what upstream
		// sees, not the one the client used.
		if spy.LastPath != "/graphql" {
			t.Errorf("expected the upstream path /graphql; received %s", spy.LastPath)
		}
	}

	allowed, rejected, malformed, _ := p.CountersSnapshot()
	if allowed != 4 || rejected != 0 || malformed != 0 {
		t.Errorf("expected 4 allowed; received %d/%d/%d",
			allowed, rejected, malformed)
	}
}

func TestProxyRejected(t *testing.T) {
	p, spy := testProxy(t, "{ user { name } }")

	// A document that isn't on the list, and one that only differs in a field.
	for _, body := range []string{
		`{"query":"{ user { email } }"}`,
		`{"query":"{ user { name id } }"}`,
		`{"query":"mutation { deleteEverything }"}`,
	} {
		w := do(t, p, postJSON(body))
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403; received %d; body: %s", w.Code, body)
		}
		if !strings.Contains(w.Body.String(), "OPERATION_NOT_ALLOWED") {
			t.Errorf("expected a GraphQL error body; received %s", w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("expected a JSON content type; received %q", ct)
		}
	}
	if spy.Requests != 0 {
		t.Error("a rejected request must not reach upstream")
	}
	if _, rejected, _, _ := p.CountersSnapshot(); rejected != 3 {
		t.Errorf("expected 3 rejected; received %d", rejected)
	}
}

func TestProxyMalformed(t *testing.T) {
	p, spy := testProxy(t, "{ foo }")

	// A body that is no JSON, and one holding no query.
	for _, body := range []string{`not json`, `{"variables":{}}`, `{"query":`} {
		w := do(t, p, postJSON(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400; received %d; body: %s", w.Code, body)
		}
	}

	// A document that doesn't parse is rejected as not allowed, not as malformed:
	// the JSON is fine, the document just isn't on the list.
	w := do(t, p, postJSON(`{"query":"{ broken"}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
	if spy.Requests != 0 {
		t.Error("nothing must reach upstream")
	}
}

func TestProxyBatch(t *testing.T) {
	body := `[{"query":"{ a }"},{"query":"{ b }"}]`

	// A batch is rejected outright unless it's allowed.
	p, _ := testProxy(t, "{ a }", "{ b }")
	if w := do(t, p, postJSON(body)); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400; received %d", w.Code)
	}

	pb, spy := testProxyWith(t, func(p *Proxy) { p.allowBatch = true },
		"{ a }", "{ b }")
	if w := do(t, pb, postJSON(body)); w.Code != http.StatusOK {
		t.Errorf("expected 200; received %d", w.Code)
	}
	if spy.Requests != 1 {
		t.Errorf("expected one forwarded request; received %d", spy.Requests)
	}

	// One document of the batch is enough to reject the whole batch.
	pb2, spy2 := testProxyWith(t, func(p *Proxy) { p.allowBatch = true }, "{ a }")
	if w := do(t, pb2, postJSON(body)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
	if spy2.Requests != 0 {
		t.Error("a partly allowed batch must not reach upstream")
	}
}

func TestProxyContentTypes(t *testing.T) {
	p, _ := testProxy(t, "{ foo }")

	// The body is the document itself.
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader("{ foo }"))
	r.Header.Set("Content-Type", "application/graphql")
	if w := do(t, p, r); w.Code != http.StatusOK {
		t.Errorf("application/graphql: expected 200; received %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader("{ bar }"))
	r.Header.Set("Content-Type", "application/graphql; charset=utf-8")
	if w := do(t, p, r); w.Code != http.StatusForbidden {
		t.Errorf("application/graphql: expected 403; received %d", w.Code)
	}

	// A GET request carries the document in the query string.
	if w := do(t, p, httptest.NewRequest(
		http.MethodGet, "/graphql?query=%7B+foo+%7D", nil,
	)); w.Code != http.StatusOK {
		t.Errorf("GET: expected 200; received %d", w.Code)
	}
	if w := do(t, p, httptest.NewRequest(
		http.MethodGet, "/graphql?query=%7B+bar+%7D", nil,
	)); w.Code != http.StatusForbidden {
		t.Errorf("GET: expected 403; received %d", w.Code)
	}
}

func TestProxyMaxBody(t *testing.T) {
	p, spy := testProxyWith(t, func(p *Proxy) { p.maxBody = 64 }, "{ foo }")

	body := `{"query":"{ foo }","padding":"` + strings.Repeat("x", 200) + `"}`
	w := do(t, p, postJSON(body))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413; received %d", w.Code)
	}
	if spy.Requests != 0 {
		t.Error("an oversized request must not reach upstream")
	}

	// A body with no declared length is bounded too.
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = -1
	if w := do(t, p, r); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("unknown length: expected 413; received %d", w.Code)
	}
}

func TestProxyOpaqueErrors(t *testing.T) {
	p, _ := testProxyWith(t, func(p *Proxy) { p.opaqueErrors = true }, "{ foo }")

	// Malformed and not-allowed become the same answer.
	for _, body := range []string{`not json`, `{"query":"{ bar }"}`} {
		w := do(t, p, postJSON(body))
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403; received %d; body: %s", w.Code, body)
		}
		if !strings.Contains(w.Body.String(), "OPERATION_NOT_ALLOWED") {
			t.Errorf("expected no detail; received %s", w.Body.String())
		}
	}
}

func TestProxyEmptyAllowlist(t *testing.T) {
	// An empty allowlist rejects everything rather than allowing it.
	p, spy := testProxy(t)
	if w := do(t, p, postJSON(`{"query":"{ foo }"}`)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
	if spy.Requests != 0 {
		t.Error("nothing must reach upstream")
	}
}

func TestProxyUpstreamDown(t *testing.T) {
	p, _ := testProxy(t, "{ foo }")
	// Point at a port nothing listens on.
	u, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	p2 := NewProxy(p.store, u, sha256.New,
		ProxyConfig{MaxBody: 1 << 20}, http.DefaultTransport, testLogger())

	w := do(t, p2, postJSON(`{"query":"{ foo }"}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502; received %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "UPSTREAM_UNAVAILABLE") {
		t.Errorf("expected an upstream error body; received %s", w.Body.String())
	}
}

func TestProxyExact(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ user { name } }")
	store, loader := newTestLoader(t, dir, true)
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}

	spy := new(upstreamSpy)
	upstream := httptest.NewServer(spy)
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(store, u, sha256.New,
		ProxyConfig{Exact: true, MaxBody: 1 << 20}, http.DefaultTransport,
		testLogger())

	if w := do(t, p, postJSON(`{"query":"{user{name}}"}`)); w.Code != http.StatusOK {
		t.Errorf("expected 200; received %d", w.Code)
	}
	if w := do(t, p, postJSON(`{"query":"{user{email}}"}`)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
}

// bodyReader is a request body that can be rewound without allocating, so a
// benchmark or an allocation count measures the checking path alone.
type bodyReader struct{ *strings.Reader }

func (bodyReader) Close() error { return nil }

// TestProxyCheckZeroAlloc asserts that deciding on a request allocates nothing.
// Everything the decision needs comes out of the pooled state.
func TestProxyCheckZeroAlloc(t *testing.T) {
	p, _ := testProxy(t, "{ user(id: 1) { name email } }")
	st := p.states.Get().(*state)
	defer p.states.Put(st)

	f := func(t *testing.T, name, body string) {
		t.Helper()
		reader := bodyReader{strings.NewReader(body)}
		r := httptest.NewRequest(http.MethodPost, "/graphql", reader)
		r.Header.Set("Content-Type", "application/json")
		r.Body = reader

		run := func() {
			reader.Reset(body)
			allowed, err := p.check(st, r)
			if err != nil || !allowed {
				t.Fatalf("%s: expected the document to be allowed: %v", name, err)
			}
		}
		for range 3 {
			run() // Warm the buffers of the state.
		}
		if n := testing.AllocsPerRun(200, run); n != 0 {
			t.Errorf("%s: expected no allocations; received %v", name, n)
		}
	}

	f(t, "plain", `{"operationName":"Q",`+
		`"query":"{ user(id: 1) { name email } }","variables":{"a":1}}`)
	// A document with escape sequences takes the scratch buffer of the state.
	f(t, "escaped", `{"query":"{\n  user(id: 1) { name email }\n}"}`)
}

// BenchmarkProxyCheck measures the decision alone, without the HTTP machinery.
func BenchmarkProxyCheck(b *testing.B) {
	p, _ := testProxy(&testing.T{}, "{ user(id: 1) { name email } }")
	st := p.states.Get().(*state)
	defer p.states.Put(st)

	for _, c := range []struct{ name, body string }{
		{"plain", `{"operationName":"Q",` +
			`"query":"{ user(id: 1) { name email } }","variables":{"a":1}}`},
		{"escaped", `{"query":"{\n  user(id: 1) { name email }\n}"}`},
		{"rejected", `{"query":"{ user(id: 1) { name secret } }"}`},
	} {
		b.Run(c.name, func(b *testing.B) {
			reader := bodyReader{strings.NewReader(c.body)}
			r := httptest.NewRequest(http.MethodPost, "/graphql", reader)
			r.Header.Set("Content-Type", "application/json")
			r.Body = reader
			b.SetBytes(int64(len(c.body)))
			b.ReportAllocs()
			for b.Loop() {
				reader.Reset(c.body)
				if _, err := p.check(st, r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// headerSpy is an upstream that records the forwarding headers it received.
type headerSpy struct{ Forwarded, For, Host, Proto string }

func (s *headerSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.For = r.Header.Get("X-Forwarded-For")
	s.Host = r.Header.Get("X-Forwarded-Host")
	s.Proto = r.Header.Get("X-Forwarded-Proto")
	s.Forwarded = r.Header.Get("Forwarded")
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

// TestProxyForwardedHeaders covers what the upstream API learns about the client.
func TestProxyForwardedHeaders(t *testing.T) {
	f := func(t *testing.T, trust bool) *headerSpy {
		t.Helper()
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", "{ foo }")
		store, loader := newTestLoader(t, dir, false)
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		spy := new(headerSpy)
		upstream := httptest.NewServer(spy)
		t.Cleanup(upstream.Close)
		u, err := url.Parse(upstream.URL + "/graphql")
		if err != nil {
			t.Fatal(err)
		}
		p := NewProxy(store, u, sha256.New, ProxyConfig{
			MaxBody: 1 << 20, TrustForwarded: trust,
		}, http.DefaultTransport, testLogger())

		r := postJSON(`{"query":"{foo}"}`)
		r.RemoteAddr = "10.0.0.7:34567"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		r.Header.Set("X-Forwarded-Host", "api.example.com")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("Forwarded", `for=203.0.113.9;proto=https`)
		if w := do(t, p, r); w.Code != http.StatusOK {
			t.Fatalf("expected 200; received %d", w.Code)
		}
		return spy
	}

	{ // Without trust the client is the direct peer, whatever it claims.
		spy := f(t, false)
		if spy.For != "10.0.0.7" {
			t.Errorf("expected the peer address; received %q", spy.For)
		}
		if spy.Host == "api.example.com" {
			t.Error("expected the claimed host to be replaced")
		}
		if spy.Proto != "http" {
			t.Errorf("expected the protocol of the connection; received %q", spy.Proto)
		}
		// An RFC 7239 header of the client must not reach upstream unchecked.
		if spy.Forwarded != "" {
			t.Errorf("expected Forwarded to be dropped; received %q", spy.Forwarded)
		}
	}

	{ // With trust the chain of the balancer is kept and the peer appended.
		spy := f(t, true)
		if spy.For != "203.0.113.9, 10.0.0.7" {
			t.Errorf("expected the chain plus the peer; received %q", spy.For)
		}
		if spy.Host != "api.example.com" {
			t.Errorf("expected the forwarded host; received %q", spy.Host)
		}
		if spy.Proto != "https" {
			t.Errorf("expected the forwarded protocol; received %q", spy.Proto)
		}
	}

	{ // With trust and nothing to keep, the peer is the whole chain.
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", "{ foo }")
		store, loader := newTestLoader(t, dir, false)
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}
		spy := new(headerSpy)
		upstream := httptest.NewServer(spy)
		defer upstream.Close()
		u, err := url.Parse(upstream.URL + "/graphql")
		if err != nil {
			t.Fatal(err)
		}
		p := NewProxy(store, u, sha256.New, ProxyConfig{
			MaxBody: 1 << 20, TrustForwarded: true,
		}, http.DefaultTransport, testLogger())

		r := postJSON(`{"query":"{foo}"}`)
		r.RemoteAddr = "192.0.2.5:1111"
		if w := do(t, p, r); w.Code != http.StatusOK {
			t.Fatalf("expected 200; received %d", w.Code)
		}
		if spy.For != "192.0.2.5" {
			t.Errorf("expected the peer alone; received %q", spy.For)
		}
	}
}

// TestProxyMetricsGuard pins the state the hot path branches on.
func TestProxyMetricsGuard(t *testing.T) {
	p, _ := testProxy(t, "{ foo }")
	if p.metrics != nil {
		t.Error("expected no metrics by default")
	}

	// A request is served the same either way.
	if w := do(t, p, postJSON(`{"query":"{foo}"}`)); w.Code != http.StatusOK {
		t.Errorf("expected 200 without metrics; received %d", w.Code)
	}

	p.SetMetrics(NewMetrics(p, p.store))
	if p.metrics == nil {
		t.Fatal("expected metrics after SetMetrics")
	}
	if w := do(t, p, postJSON(`{"query":"{foo}"}`)); w.Code != http.StatusOK {
		t.Errorf("expected 200 with metrics; received %d", w.Code)
	}
	if w := do(t, p, postJSON(`{"query":"{bar}"}`)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403 with metrics; received %d", w.Code)
	}

	// Both decisions reach the histogram, which has an Observe call per exit.
	var buf strings.Builder
	writeExposition(t, &buf, p.metrics)
	for _, want := range []string{
		`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`,
		`gqlhash_proxy_request_duration_seconds_count{decision="rejected"} 1`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("expected %q in the exposition; received %s", want, buf.String())
		}
	}
}

// writeExposition scrapes m into w.
func writeExposition(t *testing.T, w *strings.Builder, m *Metrics) {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler(testLogger()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the metrics handler; received %d", rec.Code)
	}
	w.WriteString(rec.Body.String())
}
