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

// TestProxyDepthLimit covers a proxy whose depth limit is its own rather than
// the default. A document past the default can't be on the allowlist either, so
// the limit and the lookup would refuse it alike and neither would be under
// test; here the allowlist takes the document and the limit is what turns it
// away. The default limit is covered against a running server by the
// acceptance suite, the parser's TestParseDepthLimit covers the limit itself,
// and TestAllowlistTooDeepDocument the same limit at the allowlist.
func TestProxyDepthLimit(t *testing.T) {
	// nested returns a document whose selection sets nest depth deep.
	nested := func(depth int) string {
		return "{" + strings.Repeat("f{", depth-1) + "f" + strings.Repeat("}", depth)
	}

	deep := nested(5)
	p, spy := testProxyWith(t, func(p *proxy) {
		p.options.DepthLimit = 4
	}, "{ shallow }", deep)

	if w := do(t, p, postJSON(`{"query":"{ shallow }"}`)); w.Code != http.StatusOK {
		t.Fatalf("expected the shallow document to be allowed; %d: %s", w.Code, w.Body)
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
}

// TestProxyLogsDecisions covers what a decision leaves in the log. Both events
// are behind a flag, since a rejection is the path a flood takes: -log.requests
// names what was forwarded and the debug level names what wasn't,
// and the counters carry the totals either way.
//
// It lives here rather than in the acceptance suite because nothing is asked of
// what an implementation logs, see the acceptance package.
func TestProxyLogsDecisions(t *testing.T) {
	logs := new(syncBuffer)
	p, _ := testProxyWith(t, func(p *proxy) {
		p.log = zerolog.New(logs).Level(zerolog.DebugLevel)
		p.debug, p.logRequests = true, true
	}, "{ a }")

	if w := do(t, p, postJSON(`{"query":"{ a }"}`)); w.Code != http.StatusOK {
		t.Fatalf("expected the allowed document; %d: %s", w.Code, w.Body)
	}
	if w := do(t, p, postJSON(`{"query":"{ evil }"}`)); w.Code != http.StatusForbidden {
		t.Fatalf("expected the rejection; %d: %s", w.Code, w.Body)
	}
	if w := do(t, p, postJSON("not json")); w.Code != http.StatusBadRequest {
		t.Fatalf("expected the malformed request; %d: %s", w.Code, w.Body)
	}

	for _, want := range []string{
		"forwarding",
		"the document is not on the allowlist",
		"rejecting a malformed request",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("expected %q in the log; received %s", want, logs.String())
		}
	}
	// None of the three is a failure of the proxy.
	if strings.Contains(logs.String(), `"level":"error"`) {
		t.Errorf("expected nothing at error level; received %s", logs.String())
	}

	// Neither event is logged where the level doesn't keep a debug one,
	// which is what stops a flood from writing a line per request.
	// debug follows the level of the logger, see newProxy.
	quiet := new(syncBuffer)
	q, _ := testProxyWith(t, func(p *proxy) {
		p.log = zerolog.New(quiet).Level(zerolog.InfoLevel)
		p.debug, p.logRequests = false, false
	}, "{ a }")
	if w := do(t, q, postJSON(`{"query":"{ a }"}`)); w.Code != http.StatusOK {
		t.Fatalf("expected the allowed document; %d: %s", w.Code, w.Body)
	}
	if w := do(t, q, postJSON(`{"query":"{ evil }"}`)); w.Code != http.StatusForbidden {
		t.Fatalf("expected the rejection; %d: %s", w.Code, w.Body)
	}
	if quiet.String() != "" {
		t.Errorf("expected nothing logged without the flags; received %s", quiet)
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

// TestProxyReleasesOversizedBuffers covers what a pooled state carries back into
// the pool. -server.max-body allows a megabyte by default, so without this a
// burst of requests that size leaves the pool holding one buffer per concurrent
// request for the life of the process. The parser holds the same policy,
// see its maxRetainedBufferSize.
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

	// A buffer the common request grew is kept:
	// releasing that one would cost an allocation per request to save nothing.
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
