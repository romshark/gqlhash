package proxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/parser"
)

// testProxy returns a proxy allowing the given documents and forwarding to an
// upstream that echoes what it received.
func testProxy(t *testing.T, documents ...string) (*proxy, *upstreamSpy) {
	t.Helper()
	return testProxyWith(t, func(*proxy) {}, documents...)
}

func testProxyWith(
	t *testing.T, configure func(*proxy), documents ...string,
) (*proxy, *upstreamSpy) {
	t.Helper()
	dir := t.TempDir()
	for i, d := range documents {
		writeDoc(t, dir, string(rune('a'+i))+".graphql", d)
	}

	list := newAllowlist(t, dir)

	spy := new(upstreamSpy)
	upstream := httptest.NewServer(spy)
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL + "/graphql")
	if err != nil {
		t.Fatal(err)
	}

	p := newProxy(list, u, sha256.New,
		proxyConfig{maxBody: 1 << 20}, http.DefaultTransport, testLogger())
	configure(p)
	return p, spy
}

// upstreamAnswer is what the test upstream answers.
const upstreamAnswer = `{"data":{"ok":true}}`

// upstreamSpy records what reached the upstream API.
type upstreamSpy struct {
	requests  int
	lastBody  string
	lastPath  string
	lastQuery string
}

func (s *upstreamSpy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.requests++
	s.lastBody = string(body)
	s.lastPath = r.URL.Path
	s.lastQuery = r.URL.RawQuery
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

func do(t *testing.T, p *proxy, r *http.Request) *httptest.ResponseRecorder {
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
		if spy.lastBody != body {
			t.Errorf("expected upstream to receive %s; received %s", body, spy.lastBody)
		}
		// The upstream URL names the endpoint, so its path is what upstream sees,
		// not the one the client used.
		if spy.lastPath != "/graphql" {
			t.Errorf("expected the upstream path /graphql; received %s", spy.lastPath)
		}
	}

	if d := p.snapshot(); d.allowed != 4 || d.rejected != 0 || d.malformed != 0 {
		t.Errorf("expected 4 allowed; received %d/%d/%d",
			d.allowed, d.rejected, d.malformed)
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
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(
			ct, "application/json",
		) {
			t.Errorf("expected a JSON content type; received %q", ct)
		}
	}
	if spy.requests != 0 {
		t.Error("a rejected request must not reach upstream")
	}
	if d := p.snapshot(); d.rejected != 3 {
		t.Errorf("expected 3 rejected; received %d", d.rejected)
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
	if spy.requests != 0 {
		t.Error("nothing must reach upstream")
	}
}

// TestProxyTooDeepRequest covers the depth limit at the proxy: a request nesting
// past it is refused when it's hashed, which is what keeps a nesting attack from
// costing the API anything. It's a rejection and not a malformed request,
// since the JSON is fine and the document just isn't something the proxy will hash.
// The parser's TestParseDepthLimit covers the limit itself,
// and TestAllowlistTooDeepDocument the same limit at the allowlist.
func TestProxyTooDeepRequest(t *testing.T) {
	// nested returns a document whose selection sets nest depth deep.
	nested := func(depth int) string {
		return "{" + strings.Repeat("f{", depth-1) + "f" + strings.Repeat("}", depth)
	}

	// Past the default limit, which is what a deployment runs with.
	t.Run("the default limit", func(t *testing.T) {
		atLimit := nested(parser.DefaultDepthLimit)
		p, spy := testProxy(t, atLimit)

		// A document at the limit is deep, not too deep: it's allowed like any
		// other, so nesting on its own isn't what rejects the next one.
		if w := do(t, p, postJSON(
			`{"query":`+strconv.Quote(atLimit)+`}`,
		)); w.Code != http.StatusOK {
			t.Fatalf("expected the document at the limit to be allowed; %d: %s",
				w.Code, w.Body)
		}

		w := do(t, p, postJSON(
			`{"query":`+strconv.Quote(nested(parser.DefaultDepthLimit+1))+`}`,
		))
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403; received %d: %s", w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "OPERATION_NOT_ALLOWED") {
			t.Errorf("expected a GraphQL error body; received %s", w.Body.String())
		}
		if spy.requests != 1 {
			t.Errorf("expected only the request at the limit forwarded; received %d",
				spy.requests)
		}
		if d := p.snapshot(); d.rejected != 1 || d.malformed != 0 {
			t.Errorf("expected 1 rejected and nothing malformed; received %d and %d",
				d.rejected, d.malformed)
		}
	})

	// A document past the default limit can't be on the allowlist either, so the
	// case above would reject it whatever the proxy's limit. Here the allowlist takes
	// the document and the proxy's own limit is what turns it away,
	// which is the limit doing the rejecting rather than the lookup.
	t.Run("a limit of its own", func(t *testing.T) {
		deep := nested(5)
		p, spy := testProxyWith(t, func(p *proxy) {
			p.options.DepthLimit = 4
		}, "{ shallow }", deep)

		if w := do(t, p, postJSON(`{"query":"{ shallow }"}`)); w.Code != http.StatusOK {
			t.Fatalf("expected the shallow document to be allowed; %d: %s",
				w.Code, w.Body)
		}

		w := do(t, p, postJSON(`{"query":`+strconv.Quote(deep)+`}`))
		if w.Code != http.StatusForbidden {
			t.Errorf("expected the allowlisted document to be refused; %d: %s",
				w.Code, w.Body)
		}
		if spy.requests != 1 {
			t.Errorf("expected only the shallow request forwarded; received %d",
				spy.requests)
		}
	})
}

func TestProxyBatch(t *testing.T) {
	body := `[{"query":"{ a }"},{"query":"{ b }"}]`

	// A batch is rejected outright unless it's allowed.
	p, _ := testProxy(t, "{ a }", "{ b }")
	if w := do(t, p, postJSON(body)); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400; received %d", w.Code)
	}

	pb, spy := testProxyWith(t, func(p *proxy) { p.allowBatch = true },
		"{ a }", "{ b }")
	if w := do(t, pb, postJSON(body)); w.Code != http.StatusOK {
		t.Errorf("expected 200; received %d", w.Code)
	}
	if spy.requests != 1 {
		t.Errorf("expected one forwarded request; received %d", spy.requests)
	}

	// One document of the batch is enough to reject the whole batch.
	pb2, spy2 := testProxyWith(t, func(p *proxy) { p.allowBatch = true }, "{ a }")
	if w := do(t, pb2, postJSON(body)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
	if spy2.requests != 0 {
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
	p, spy := testProxyWith(t, func(p *proxy) { p.maxBody = 64 }, "{ foo }")

	body := `{"query":"{ foo }","padding":"` + strings.Repeat("x", 200) + `"}`
	w := do(t, p, postJSON(body))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413; received %d", w.Code)
	}
	if spy.requests != 0 {
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
	p, _ := testProxyWith(t, func(p *proxy) { p.opaqueErrors = true }, "{ foo }")

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
	if spy.requests != 0 {
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
	p2 := newProxy(p.allowlist, u, sha256.New,
		proxyConfig{maxBody: 1 << 20}, http.DefaultTransport, testLogger())

	w := do(t, p2, postJSON(`{"query":"{ foo }"}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502; received %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "UPSTREAM_UNAVAILABLE") {
		t.Errorf("expected an upstream error body; received %s", w.Body.String())
	}
}

// bodyReader is a request body that can be rewound without allocating,
// so a benchmark or an allocation count measures the checking path alone.
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
		list := newAllowlist(t, dir)
		spy := new(headerSpy)
		upstream := httptest.NewServer(spy)
		t.Cleanup(upstream.Close)
		u, err := url.Parse(upstream.URL + "/graphql")
		if err != nil {
			t.Fatal(err)
		}
		p := newProxy(list, u, sha256.New, proxyConfig{
			maxBody: 1 << 20, trustForwarded: trust,
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
		list := newAllowlist(t, dir)
		spy := new(headerSpy)
		upstream := httptest.NewServer(spy)
		defer upstream.Close()
		u, err := url.Parse(upstream.URL + "/graphql")
		if err != nil {
			t.Fatal(err)
		}
		p := newProxy(list, u, sha256.New, proxyConfig{
			maxBody: 1 << 20, trustForwarded: true,
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

// TestProxyEveryDecisionIsTimed covers what replaced the metrics guard: a proxy
// always keeps its metrics, so every way out of ServeHTTP is timed and the hot
// path has nothing to decide. The control server is what exposes them, and a run
// always has one.
func TestProxyEveryDecisionIsTimed(t *testing.T) {
	p, _ := testProxy(t, "{ foo }")
	if p.metrics == nil {
		t.Fatal("expected a proxy to keep metrics")
	}

	// One request per decision, each leaving ServeHTTP by its own exit.
	if w := do(t, p, postJSON(`{"query":"{foo}"}`)); w.Code != http.StatusOK {
		t.Errorf("expected 200; received %d", w.Code)
	}
	if w := do(t, p, postJSON(`{"query":"{bar}"}`)); w.Code != http.StatusForbidden {
		t.Errorf("expected 403; received %d", w.Code)
	}
	if w := do(t, p, postJSON(`not json`)); w.Code != http.StatusBadRequest {
		t.Errorf("expected 400; received %d", w.Code)
	}

	var buf strings.Builder
	writeExposition(t, &buf, p.metrics)
	for _, want := range []string{
		`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`,
		`gqlhash_proxy_request_duration_seconds_count{decision="rejected"} 1`,
		`gqlhash_proxy_request_duration_seconds_count{decision="malformed"} 1`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("expected %q in the exposition; received %s", want, buf.String())
		}
	}
}

// writeExposition scrapes m into w.
func writeExposition(t *testing.T, w *strings.Builder, m *metrics) {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler(testLogger()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from the metrics handler; received %d", rec.Code)
	}
	w.WriteString(rec.Body.String())
}

// TestProxyClientDisconnects covers a caller that hangs up while the upstream is
// still working. The forwarded request is canceled with it, so the API stops
// paying for an answer nobody is waiting for, and the proxy counts no upstream failure:
// the upstream didn't fail, the client left.
func TestProxyClientDisconnects(t *testing.T) {
	reached := make(chan struct{})
	upstreamCtx := make(chan error, 1)
	upstream := httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			// The body is read the way an API reads it, which is also what lets
			// the server watch the connection: it notices a peer that goes away
			// only once the request is off the wire.
			_, _ = io.Copy(io.Discard, r.Body)
			close(reached)
			// The handler answers nothing and waits, which is the request in
			// flight the client gives up on.
			<-r.Context().Done()
			upstreamCtx <- r.Context().Err()
		}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ slow }")

	// A debug logger, since a hang-up is logged at debug and nowhere else.
	logs := new(syncBuffer)
	p := newProxy(newAllowlist(t, dir), mustURL(t, upstream.URL+"/graphql"),
		sha256.New, proxyConfig{maxBody: 1 << 20}, http.DefaultTransport,
		zerolog.New(logs).Level(zerolog.DebugLevel))

	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		defer close(served)
		p.ServeHTTP(w, postJSON(`{"query":"{ slow }"}`).WithContext(ctx))
	}()

	<-reached
	cancel()

	// The request the proxy made is canceled by the one the client made,
	// rather than running on until the upstream is done with it.
	if err := <-upstreamCtx; !errors.Is(err, context.Canceled) {
		t.Errorf("expected the forwarded request to be canceled; received %v", err)
	}
	<-served

	if body := w.Body.String(); body != "" {
		t.Errorf("expected nothing to be answered; received %s", body)
	}
	made := p.snapshot()
	if made.allowed != 1 {
		t.Errorf("expected the request to be allowed and forwarded; received %d",
			made.allowed)
	}
	// The one thing this arm exists for: a client that gives up is no failure
	// of the API behind the proxy, and doesn't page anyone.
	if made.upstream != 0 {
		t.Errorf("expected no upstream error; received %d", made.upstream)
	}
	if !strings.Contains(logs.String(), "the client left before the answer") {
		t.Errorf("expected the hang-up at debug; received %s", logs.String())
	}
	if strings.Contains(logs.String(), `"level":"error"`) {
		t.Errorf("expected nothing at error level; received %s", logs.String())
	}
}

// TestProxyNamedOperations covers documents holding more than one named
// operation, where the request picks which to run with operationName.
//
// What's allowed is the document, so the pick is the upstream's to resolve.
// That cuts both ways, and both ways are what this covers: every operation of an
// allowed file is allowed, and an allowed operation carries nothing else in with it,
// since the document it arrived in is what was hashed.
func TestProxyNamedOperations(t *testing.T) {
	const (
		getUser  = "query GetUser {\n  user { name }\n}"
		getPosts = "query GetPosts {\n  posts { title }\n}"
		destroy  = "mutation Destroy {\n  deleteEverything\n}"
	)
	body := func(name, document string) *http.Request {
		return postJSON(`{"operationName":` + strconv.Quote(name) +
			`,"query":` + strconv.Quote(document) + `}`)
	}

	// The allowed file holds two operations, and a request runs one of them.
	t.Run("an operation of an allowed file", func(t *testing.T) {
		both := getUser + "\n\n" + getPosts
		p, spy := testProxy(t, both)

		for _, name := range []string{"GetUser", "GetPosts", ""} {
			w := do(t, p, body(name, both))
			if w.Code != http.StatusOK {
				t.Errorf("operationName %q: expected 200; received %d: %s",
					name, w.Code, w.Body)
			}
			// The name is forwarded untouched, since resolving it is the API's job.
			// An empty one against a document of two operations is an error there,
			// which the proxy has no opinion about.
			if !strings.Contains(spy.lastBody, `"operationName":`+strconv.Quote(name)) {
				t.Errorf("operationName %q: expected it forwarded; received %s",
					name, spy.lastBody)
			}
		}

		// Only the operation the client wants, lifted out of the allowed file.
		if w := do(t, p, body("GetUser", getUser)); w.Code != http.StatusForbidden {
			t.Errorf("expected one operation on its own to be refused; received %d",
				w.Code)
		}
		// The same two the other way round is another document too.
		if w := do(t, p, body("GetUser", getPosts+"\n\n"+getUser)); w.Code !=
			http.StatusForbidden {
			t.Errorf("expected the reordered document to be refused; received %d",
				w.Code)
		}
	})

	// The other direction, and the one that matters: the request selects an
	// operation that is allowed on its own, and carries one that isn't.
	t.Run("an operation smuggled in beside an allowed one", func(t *testing.T) {
		p, spy := testProxy(t, getUser)

		if w := do(t, p, body("GetUser", getUser)); w.Code != http.StatusOK {
			t.Fatalf("expected the allowed operation on its own; received %d: %s",
				w.Code, w.Body)
		}

		// Selecting GetUser doesn't make the document GetUser: what was hashed is
		// everything the request carried, so the mutation rides in with it or the
		// request is refused. It's refused.
		for _, document := range []string{
			getUser + "\n\n" + destroy,
			destroy + "\n\n" + getUser,
		} {
			w := do(t, p, body("GetUser", document))
			if w.Code != http.StatusForbidden {
				t.Errorf("expected the smuggled operation to be refused; received %d: %s",
					w.Code, w.Body)
			}
		}
		if spy.requests != 1 {
			t.Errorf("expected only the allowed request forwarded; received %d",
				spy.requests)
		}
	})

	// Two documents that are each allowed don't add up to an allowed one.
	t.Run("two allowed files in one request", func(t *testing.T) {
		p, spy := testProxy(t, getUser, getPosts)

		for _, document := range []string{getUser, getPosts} {
			if w := do(t, p, body("GetUser", document)); w.Code != http.StatusOK {
				t.Fatalf("expected each file to be allowed; received %d: %s",
					w.Code, w.Body)
			}
		}
		if w := do(t, p, body("GetUser", getUser+"\n\n"+getPosts)); w.Code !=
			http.StatusForbidden {
			t.Errorf("expected the two of them together to be refused; received %d",
				w.Code)
		}
		if spy.requests != 2 {
			t.Errorf("expected only the two allowed requests forwarded; received %d",
				spy.requests)
		}
	})
}

// TestProxyBodyReadFails covers a request body that can't be read to the end.
//
// That's no malformed document: the client went away, a timeout cut it off,
// or the connection failed, and the answer and the log say so. Calling it malformed
// JSON would also be wrong for an application/graphql body, which is no JSON.
func TestProxyBodyReadFails(t *testing.T) {
	failure := errors.New("connection reset by peer")

	for _, td := range []struct {
		name string
		// request carries a body that fails part way through.
		request func() *http.Request
		expect  error
	}{{
		// The length is declared and the body is shorter, so one read is short.
		name: "a body shorter than its content length",
		request: func() *http.Request {
			r := postJSON(`{"query":"{ a }"}`)
			r.ContentLength = 1 << 10
			return r
		},
		expect: io.ErrUnexpectedEOF,
	}, {
		// No length, so the body is read until it ends. It doesn't end, it fails.
		name: "a body that fails mid-read",
		request: func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/graphql",
				io.MultiReader(strings.NewReader(`{"query":`), errReader{failure}))
			r.Header.Set("Content-Type", "application/json")
			r.ContentLength = -1
			return r
		},
		expect: failure,
	}} {
		t.Run(td.name, func(t *testing.T) {
			logs := new(syncBuffer)
			p, spy := testProxyWith(t, func(p *proxy) {
				p.log = zerolog.New(logs).Level(zerolog.DebugLevel)
				p.debug = true
			}, "{ a }")

			w := do(t, p, td.request())
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400; received %d: %s", w.Code, w.Body)
			}
			// What went wrong is named, rather than guessed at as malformed JSON.
			if body := w.Body.String(); !strings.Contains(body,
				"reading the request body") ||
				strings.Contains(body, errMalformedJSON.Error()) {
				t.Errorf("expected a read failure; received %s", body)
			}
			// The cause survives, which is what the log is for.
			if !strings.Contains(logs.String(), td.expect.Error()) {
				t.Errorf("expected %v in the log; received %s",
					td.expect, logs.String())
			}
			if spy.requests != 0 {
				t.Error("nothing must reach upstream")
			}
		})
	}
}

// errReader fails every read, standing in for a connection that drops.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestProxyDuplicateQueryParam covers a GET carrying the query parameter twice.
//
// The proxy reads the first and an API may read the last, so forwarding one it
// checked while the API runs the other is how an unchecked document gets
// executed. It answers rather than choosing.
func TestProxyDuplicateQueryParam(t *testing.T) {
	p, spy := testProxy(t, "{ a }")

	get := func(rawQuery string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/graphql?"+rawQuery, nil)
	}

	// The allowed document on its own goes through.
	if w := do(t, p, get("query=%7B%20a%20%7D")); w.Code != http.StatusOK {
		t.Fatalf("expected the allowed document; received %d: %s", w.Code, w.Body)
	}

	// The allowed document first and another after it, either separator.
	for _, rawQuery := range []string{
		"query=%7B%20a%20%7D&query=%7B%20evil%20%7D",
		"query=%7B%20a%20%7D;query=%7B%20evil%20%7D",
		"query=%7B%20evil%20%7D&query=%7B%20a%20%7D",
	} {
		w := do(t, p, get(rawQuery))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400; received %d: %s", rawQuery, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), errDuplicateQuery.Error()) {
			t.Errorf("%s: expected the reason; received %s", rawQuery, w.Body)
		}
	}

	if spy.requests != 1 {
		t.Errorf("expected only the single-document request forwarded; received %d",
			spy.requests)
	}
}

// TestProxyQueryKeyCase covers a request naming the query member in another
// case. encoding/json matches a struct field without case, so an API reading
// the body into one runs "queRY" as the query. Reading only the exact spelling
// would leave a document checked against one member and executed as another.
func TestProxyQueryKeyCase(t *testing.T) {
	p, spy := testProxy(t, "{ a }")

	// The allowed document under any spelling of the member is still allowed:
	// what's checked is the document, and the API would run this one.
	for _, body := range []string{
		`{"query":"{ a }"}`,
		`{"queRY":"{ a }"}`,
		`{"QUERY":"{ a }"}`,
	} {
		if w := do(t, p, postJSON(body)); w.Code != http.StatusOK {
			t.Errorf("%s: expected 200; received %d: %s", body, w.Code, w.Body)
		}
	}
	forwarded := spy.requests

	// The bypass: an allowed document beside one that isn't, under a spelling
	// that a Go API reads as the query and runs.
	for _, body := range []string{
		`{"query":"{ a }","queRY":"{ evil }"}`,
		`{"queRY":"{ evil }","query":"{ a }"}`,
		`{"query":"{ a }","QUERY":"{ evil }"}`,
	} {
		w := do(t, p, postJSON(body))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403; received %d: %s", body, w.Code, w.Body)
		}
	}
	if spy.requests != forwarded {
		t.Errorf("expected nothing more forwarded; received %d of %d",
			spy.requests, forwarded)
	}
}

// TestProxyReleasesOversizedBuffers covers what a pooled state carries back into
// the pool. -server.max-body allows a megabyte by default, so without this a
// burst of requests that size leaves the pool holding one buffer per concurrent
// request for the life of the process. The parser holds the same policy, see
// its maxRetainedBufferSize.
func TestProxyReleasesOversizedBuffers(t *testing.T) {
	// What a request that grew the buffers leaves behind.
	st := &state{
		body:    make([]byte, 0, 4<<20),
		scratch: make([]byte, 0, 4<<20),
		sum:     make([]byte, 0, defaultSumBuffer),
		spans:   make([]span, 0, 8<<10),
	}
	st.release()

	if cap(st.body) != defaultBodyBuffer {
		t.Errorf("body: expected it released to %d; received %d",
			defaultBodyBuffer, cap(st.body))
	}
	if cap(st.scratch) != defaultScratchBuffer {
		t.Errorf("scratch: expected it released to %d; received %d",
			defaultScratchBuffer, cap(st.scratch))
	}
	if cap(st.spans) != defaultSpans {
		t.Errorf("spans: expected them released to %d; received %d",
			defaultSpans, cap(st.spans))
	}

	// A buffer the common request grew is kept: releasing that one would cost an
	// allocation per request to save nothing.
	kept := &state{
		body:    make([]byte, 0, maxRetainedBuffer),
		scratch: make([]byte, 0, defaultScratchBuffer),
		sum:     make([]byte, 0, defaultSumBuffer),
		spans:   make([]span, 0, defaultSpans),
	}
	kept.release()
	if cap(kept.body) != maxRetainedBuffer {
		t.Errorf("expected a buffer at the limit to be kept; received %d",
			cap(kept.body))
	}

	// End to end: a request near the limit grows the body, and what goes back to
	// the pool is released rather than kept.
	p, _ := testProxy(t, "{ a }")
	body := `{"query":"{ a }","padding":"` + strings.Repeat("x", 512<<10) + `"}`
	if w := do(t, p, postJSON(body)); w.Code != http.StatusOK {
		t.Fatalf("expected the document to be allowed; received %d: %s", w.Code, w.Body)
	}
	pooled := p.states.Get().(*state)
	if cap(pooled.body) > maxRetainedBuffer {
		t.Errorf("expected the pool to hold no more than %d; received %d",
			maxRetainedBuffer, cap(pooled.body))
	}
}
