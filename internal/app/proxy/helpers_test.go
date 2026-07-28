package proxy

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

func testLogger() zerolog.Logger { return zerolog.New(io.Discard) }

// writeDoc writes a document into dir, creating parent directories.
func writeDoc(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newAllowlist returns an allowlist holding what dir has, which is what every
// test here starts from.
func newAllowlist(t *testing.T, dir string) *allowlist.Allowlist {
	t.Helper()
	list := allowlist.New(sha256.New, gqlhash.Options{})
	if _, err := list.Reload(dir); err != nil {
		t.Fatal(err)
	}
	return list
}

// testClient is what every test here talks HTTP with.
// The timeout is long enough that a slow machine doesn't fail a test and
// short enough that a hang doesn't wait for the whole test binary to time out.
var testClient = &http.Client{Timeout: 10 * time.Second}

// send does the request and returns what came back, which every test reads
// and closes the same way.
func send(t *testing.T, req *http.Request) (code int, body string) {
	t.Helper()
	res, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// postGraphQL sends body to the traffic port of a proxy as a GraphQL request.
func postGraphQL(t *testing.T, address, body string) (code int, answer string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+address+"/graphql",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return send(t, req)
}

// get fetches a URL and returns the status and the body.
func get(t *testing.T, url string) (code int, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return send(t, req)
}

// serveTo drives a handler with a recorder, which is how every test of the
// control endpoints reaches them without a listener.
// It answers the recorder, so a test can read the status, the body and the headers alike.
func serveTo(
	t *testing.T, h http.Handler, method, path string,
) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// postFrom sends a GraphQL request without a [testing.T], for a goroutine,
// where calling t.Fatal isn't allowed. It answers 0 where the request itself failed.
func postFrom(address, body string, timeout time.Duration) (statusCode int) {
	client := &http.Client{Timeout: timeout}
	res, err := client.Post("http://"+address+"/graphql", "application/json",
		strings.NewReader(body))
	if err != nil {
		return 0
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// requestTo builds a request to a path of an address, with a bearer token
// where one is given.
func requestTo(t *testing.T, method, address, path, token string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// controlHandler is the control server of an allowlist, as its own mux.
// dir is what a reload through it reads, which is the directory list was built
// from: the two are given separately here as they are in a run.
func controlHandler(
	t *testing.T, list *allowlist.Allowlist, dir string, proxy *proxy, token string,
) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	(&control{
		allowlist: list, dir: dir, proxy: proxy, token: token,
		log: testLogger(),
	}).routes(mux)
	return mux
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
