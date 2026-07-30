package acceptance

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestUpstreamRestart covers a connection the proxy kept and the API closed,
// which is every rolling deploy of an API behind it. The request never reached
// the upstream, so it goes again on a fresh connection instead of becoming a
// 502 the client did nothing to earn.
func TestUpstreamRestart(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// An API that closes an idle connection quickly,
		// which is what a restart does to every connection at once.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		api := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, upstreamAnswer)
			}),
			IdleTimeout:       50 * time.Millisecond,
			ReadHeaderTimeout: time.Second,
		}
		go func() { _ = api.Serve(listener) }()
		t.Cleanup(func() { _ = api.Close() })

		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt,
			"-upstream.url", "http://"+listener.Addr().String()+"/graphql",
			"-allowlist", dir)

		// The first request leaves a connection in the pool.
		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Fatalf("the first request: expected 200; received %d: %s", code, answer)
		}
		// The API closes it while nothing is in flight.
		time.Sleep(200 * time.Millisecond)

		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("after the connection was closed: expected 200; received %d: %s",
				code, answer)
		}
	})
}

// TestNoEncodingOfItsOwn covers what the proxy asks the API for,
// which is nothing: a client that wants an encoding says so and its request carries it,
// and what the API answers with reaches the client as the API framed it.
//
// A proxy that adds Accept-Encoding of its own has the API compress an answer
// nobody asked for, and then either hands the client an envelope it didn't ask
// for or spends the CPU to undo it.
func TestNoEncodingOfItsOwn(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// A client that asks for nothing. Go's own client adds gzip unless it's
		// told not to, so this one is built without it.
		plain := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		defer plain.CloseIdleConnections()
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("expected 200; received %d", res.StatusCode)
		}
		if v := e.api.last(t).header.Get("Accept-Encoding"); v != "" {
			t.Errorf("expected no Accept-Encoding upstream; received %q", v)
		}

		// A client that asks for one has it forwarded.
		req, err = http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Encoding", "br")
		if code, _ := send(t, req); code != http.StatusOK {
			t.Fatalf("expected 200; received %d", code)
		}
		if v := e.api.last(t).header.Get("Accept-Encoding"); v != "br" {
			t.Errorf("expected the client's Accept-Encoding; received %q", v)
		}
	})
}

// TestUpstreamAnswerEncodingPassesThrough covers an API that answers encoded:
// the bytes and the Content-Encoding naming them arrive as they were sent.
func TestUpstreamAnswerEncodingPassesThrough(t *testing.T) {
	// Not real gzip: what's under test is that nothing decodes it.
	const encoded = "\x1f\x8b not really gzip, and nothing here may unpack it"

	each(t, func(t *testing.T, tgt target) {
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(w, encoded)
		})

		req, err := http.NewRequest(http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		// The client asks for it, so nothing may decode it on the way.
		req.Header.Set("Accept-Encoding", "gzip")
		code, body, header := sendFor(t, req)

		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d", code)
		}
		if got := header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("expected the Content-Encoding of the API; received %q", got)
		}
		if body != encoded {
			t.Errorf("expected the bytes of the API; received %q", body)
		}
	})
}

// TestUpstreamTimeoutBoundsTheExchange covers an API that answers its headers
// and then stops. -upstream.timeout is the budget for the exchange,
// not for its first byte: a proxy bounding only the wait for headers holds the client,
// and the connection, for as long as the API cares to.
func TestUpstreamTimeoutBoundsTheExchange(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		release := make(chan struct{})
		defer close(release)
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// The headers are out; the body never comes.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-release
		}, "-upstream.timeout", "300ms")

		// Read it here rather than through post: the answer is cut off in the
		// middle, so the client's read fails and that's the point.
		done := make(chan struct{})
		go func() {
			defer close(done)
			client := &http.Client{Timeout: 10 * time.Second}
			res, err := client.Post("http://"+s.address+"/graphql",
				"application/json", strings.NewReader(docAllowed))
			if err == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("expected the forward to be given up on; it was still waiting")
		}
	})
}

// TestOversizedRequestIsTimed covers a request refused before a handler saw it.
// It's answered, so it's counted and timed like every other answer:
// a dashboard that reads the histogram must see every request the proxy served.
func TestOversizedRequestIsTimed(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-server.max-body", "64")

		body := `{"query":"` + strings.Repeat("x", 512) + `"}`
		if code, _ := post(t, e.server, body); code !=
			http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413; received %d", code)
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		// Its own decision, not malformed: a body over the limit says a client
		// and -server.max-body disagree, where malformed says a client sent
		// something that is no GraphQL request. An operator alerts on them differently.
		for _, want := range []string{
			`gqlhash_proxy_requests_total{decision="too_large"} 1`,
			`gqlhash_proxy_request_duration_seconds_count{decision="too_large"} 1`,
			`gqlhash_proxy_requests_total{decision="malformed"} 0`,
		} {
			if !strings.Contains(exposition, want) {
				t.Errorf("expected %q in the exposition", want)
			}
		}
	})
}

// TestUpstreamAnswerWithoutContentType covers an answer that names no type,
// which a 204 is: the proxy adds none of its own, since a client reading the
// answer of the API has to see what the API said and no more.
func TestUpstreamAnswerWithoutContentType(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})

		req, err := http.NewRequest(http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		code, body, header := sendFor(t, req)

		if code != http.StatusNoContent {
			t.Errorf("expected 204; received %d", code)
		}
		if body != "" {
			t.Errorf("expected no body; received %q", body)
		}
		if got := header.Get("Content-Type"); got != "" {
			t.Errorf("expected no content type to be invented; received %q", got)
		}
	})
}
