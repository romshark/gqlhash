package acceptance

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveUpstream starts an API with a handler of its own and a proxy in front of it,
// for the tests here that need an answer the spy doesn't give.
func serveUpstream(
	t *testing.T, tgt target, handler http.HandlerFunc, args ...string,
) *server {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", allowedDoc)
	return serve(t, tgt, append([]string{
		"-upstream.url", upstream.URL + "/graphql", "-allowlist", dir,
	}, args...)...)
}

// TestUpstreamAnswerPassesThrough covers what comes back: the proxy forwards
// the answer of the API as it is. A GraphQL API answers an error of its own
// with a status of its own, and a proxy that normalized it would hide it.
func TestUpstreamAnswerPassesThrough(t *testing.T) {
	const answer = `{"errors":[{"message":"upstream said so"}]}`

	each(t, func(t *testing.T, tgt target) {
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/graphql-response+json")
			w.Header().Set("X-Request-Id", "abc123")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, answer)
		})

		req, err := http.NewRequest(http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		code, body, header := sendFor(t, req)

		if code != http.StatusInternalServerError {
			t.Errorf("expected the status of the API; received %d", code)
		}
		if body != answer {
			t.Errorf("expected the answer of the API; received %q", body)
		}
		if got := header.Get("Content-Type"); got != "application/graphql-response+json" {
			t.Errorf("expected the content type of the API; received %q", got)
		}
		if got := header.Get("X-Request-Id"); got != "abc123" {
			t.Errorf("expected the headers of the API; received %q", got)
		}
	})
}

// TestUpstreamTimeout covers -upstream.timeout: an API that doesn't answer in
// time is a gateway timeout, told apart from an API that can't be reached,
// and counted as an upstream error either way.
func TestUpstreamTimeout(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		release := make(chan struct{})
		// Released before the upstream is closed: closing it waits for the
		// request in flight, and a cleanup would run after that close rather
		// than before it, which deadlocks.
		defer close(release)
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, upstreamAnswer)
		}, "-upstream.timeout", "200ms")

		code, answer := post(t, s, docAllowed)
		if code != http.StatusGatewayTimeout {
			t.Fatalf("expected 504; received %d: %s", code, answer)
		}
		if !strings.Contains(answer, "UPSTREAM_UNAVAILABLE") {
			t.Errorf("expected an upstream error body; received %s", answer)
		}

		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		if !strings.Contains(exposition, "gqlhash_proxy_upstream_errors_total 1") {
			t.Errorf("expected the timeout counted; received %s", exposition)
		}
	})
}

// TestConcurrentRequests covers requests arriving past
// -upstream.max-idle-conns-per-host: every one of them is answered,
// and every one of them reaches the API as the client wrote it.
//
// The documents differ per request on purpose: identical bodies can't catch the
// worst defect this proxy can have, a body forwarded under another request's
// connection. Only allowed documents reach the API, so they're all allowed too.
func TestConcurrentRequests(t *testing.T) {
	const concurrent = 16

	// concurrent documents, each allowed and each its own.
	documents := make([]string, concurrent)
	requests := make([]string, concurrent)
	for i := range documents {
		documents[i] = fmt.Sprintf("query GetUser%d {\n  user(id: %d) {\n    name\n  }\n}",
			i, i)
		requests[i] = fmt.Sprintf(`{"query":"query GetUser%d{user(id:%d){name}}"}`, i, i)
	}

	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, documents, "-upstream.max-idle-conns-per-host", "2")

		var mu sync.Mutex
		codes := make(map[int]int, 2)
		var wg sync.WaitGroup
		for i := range concurrent {
			wg.Add(1)
			go func() {
				defer wg.Done()
				code := <-postAsync(e.address, requests[i])
				mu.Lock()
				codes[code]++
				mu.Unlock()
			}()
		}
		wg.Wait()

		if codes[http.StatusOK] != concurrent {
			t.Fatalf("expected %d requests served over a pool of 2; received %v",
				concurrent, codes)
		}

		// Every document arrived, and each arrived once: a body delivered under
		// another request's connection would leave one twice and one missing.
		seen := make(map[string]int, concurrent)
		for _, r := range e.api.all() {
			seen[r.body]++
		}
		for i, request := range requests {
			if seen[request] != 1 {
				t.Errorf("document %d: expected it forwarded once; received %d",
					i, seen[request])
			}
		}
	})
}

// TestUpstreamSlowAnswer covers an answer that arrives in pieces:
// what the API wrote is what the client reads, whole, however the copy is buffered.
func TestUpstreamSlowAnswer(t *testing.T) {
	// Larger than the buffer the copy takes from the pool.
	big := strings.Repeat("x", 128<<10)

	each(t, func(t *testing.T, tgt target) {
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"big":"`)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
			_, _ = io.WriteString(w, big)
			_, _ = io.WriteString(w, `"}}`)
		})

		code, answer := post(t, s, docAllowed)
		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d", code)
		}
		if want := `{"data":{"big":"` + big + `"}}`; answer != want {
			t.Errorf("expected the whole answer; received %d bytes of %d",
				len(answer), len(want))
		}
	})
}
