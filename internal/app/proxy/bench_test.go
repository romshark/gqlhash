package proxy

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// benchDocument is the allowed document, and benchBody the request that carries
// it the way a client sends it: minified, with variables and an operation name.
const (
	benchDocument = "query GetUser {\n  user(id: 1) {\n    name\n    email\n  }\n}"
	benchBody     = `{"operationName":"GetUser",` +
		`"query":"query GetUser{user(id:1){name email}}","variables":{"id":1}}`
	benchBodyRejected = `{"operationName":"GetUser",` +
		`"query":"query GetUser{user(id:1){name secret}}","variables":{"id":1}}`
)

// benchUpstream is an upstream that answers as cheaply as possible,
// so what a benchmark measures is the proxy and not the API behind it.
type benchUpstream struct{}

func (benchUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

// benchProxy returns a proxy that allows [benchDocument] and forwards to a live upstream.
func benchProxy(b *testing.B) *proxy {
	b.Helper()
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.graphql"),
		[]byte(benchDocument), 0o644); err != nil {
		b.Fatal(err)
	}
	list := allowlist.New(sha256.New, gqlhash.Options{})
	if _, err := list.Reload(dir); err != nil {
		b.Fatal(err)
	}

	upstream := httptest.NewServer(benchUpstream{})
	b.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL + "/graphql")
	if err != nil {
		b.Fatal(err)
	}
	return newProxy(list, u, sha256.New,
		proxyConfig{maxBody: 1 << 20}, http.DefaultTransport,
		testLogger())
}

// BenchmarkProxyHandler measures the handler without a network in between: the
// allowed path forwards to a live upstream, the rejected path answers alone.
func BenchmarkProxyHandler(b *testing.B) {
	for _, c := range []struct {
		name   string
		body   string
		expect int
	}{
		{"allowed", benchBody, http.StatusOK},
		{"rejected", benchBodyRejected, http.StatusForbidden},
		{"rejected_debug", benchBodyRejected, http.StatusForbidden},
		{"malformed", `{"nope":`, http.StatusBadRequest},
	} {
		b.Run(c.name, func(b *testing.B) {
			p := benchProxy(b)
			// A rejection is logged at debug level only,
			// so the default case writes nothing. The difference between the two is
			// what an operator pays for turning the events on.
			if c.name == "rejected_debug" {
				p.log = p.log.Level(zerolog.DebugLevel)
				p.debug = true
			} else {
				p.log = p.log.Level(zerolog.InfoLevel)
				p.debug = false
			}
			reader := bodyReader{strings.NewReader(c.body)}
			r := httptest.NewRequest(http.MethodPost, "/graphql", reader)
			r.Header.Set("Content-Type", "application/json")

			b.SetBytes(int64(len(c.body)))
			b.ReportAllocs()
			for b.Loop() {
				// Forwarding replaces the body of the request, so it's restored
				// for every iteration. Without the status check below a
				// broken setup would quietly measure the rejection path.
				reader.Reset(c.body)
				r.Body = reader
				w := httptest.NewRecorder()
				p.ServeHTTP(w, r)
				if w.Code != c.expect {
					b.Fatalf("expected %d; received %d: %s",
						c.expect, w.Code, w.Body.String())
				}
			}
		})
	}
}

// BenchmarkProxyEndToEnd measures a real client against a real listener.
// The direct case is the same request straight to the upstream API,
// so the difference between the two is what the proxy costs.
func BenchmarkProxyEndToEnd(b *testing.B) {
	// The upstream every case talks to, directly or through the proxy.
	upstream := httptest.NewServer(benchUpstream{})
	defer upstream.Close()

	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.graphql"),
		[]byte(benchDocument), 0o644); err != nil {
		b.Fatal(err)
	}
	list := allowlist.New(sha256.New, gqlhash.Options{})
	if _, err := list.Reload(dir); err != nil {
		b.Fatal(err)
	}
	u, err := url.Parse(upstream.URL + "/graphql")
	if err != nil {
		b.Fatal(err)
	}
	proxy := httptest.NewServer(newProxy(list, u, sha256.New,
		proxyConfig{maxBody: 1 << 20}, http.DefaultTransport, testLogger()))
	defer proxy.Close()

	for _, c := range []struct {
		name, target, body string
		expect             int
	}{
		{"direct", upstream.URL + "/graphql", benchBody, http.StatusOK},
		{"proxy_allowed", proxy.URL + "/graphql", benchBody, http.StatusOK},
		{"proxy_rejected", proxy.URL + "/graphql", benchBodyRejected,
			http.StatusForbidden},
	} {
		b.Run(c.name, func(b *testing.B) {
			client := &http.Client{Transport: &http.Transport{
				MaxIdleConnsPerHost: 128,
			}}
			b.SetBytes(int64(len(c.body)))
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					res, err := client.Post(c.target, "application/json",
						strings.NewReader(c.body))
					if err != nil {
						b.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, res.Body)
					_ = res.Body.Close()
					if res.StatusCode != c.expect {
						b.Fatalf("expected %d; received %d", c.expect, res.StatusCode)
					}
				}
			})
		})
	}
}
