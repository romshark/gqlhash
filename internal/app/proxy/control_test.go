package proxy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// TestControlReloadEmptyDirectory pins the shape of the answer where a reload
// took nothing: an empty list of files, not a null. A client that reads the
// answer to fail a deployment shouldn't need a case for the empty directory,
// and JSON tells the two apart even though Go's zero value doesn't.
func TestControlReloadEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	mux := controlHandler(t, newAllowlist(t, dir), dir, nil, "")

	rec := serveTo(t, mux, http.MethodPost, "/reload")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200; received %d %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"total":0,"files":[]`) {
		t.Errorf("expected an empty list of files; received %s", body)
	}

	// The answer parses as the shape it claims, so files is a list either way.
	var answer struct {
		Documents struct {
			Total int       `json:"total"`
			Files *[]string `json:"files"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if answer.Documents.Files == nil {
		t.Error("expected files to be a list; received null")
	}
}

func TestControlStatus(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	list := newAllowlist(t, dir)
	p := newProxy(list, mustURL(t, "http://x"), sha256.New,
		proxyConfig{maxBody: 1 << 20}, nil, testLogger())
	p.counters.allowed.Add(2)
	p.counters.rejected.Add(1)
	p.counters.upstream.Add(3)

	mux := controlHandler(t, list, dir, p, "")
	w := serveTo(t, mux, http.MethodGet, "/status")
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

// TestControlNoToken covers the default: no token, so every request passes
// and the address is the only thing keeping a reload private.
func TestControlNoToken(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	list := newAllowlist(t, dir)
	mux := controlHandler(t, list, dir, nil, "")

	rec := serveTo(t, mux, http.MethodPost, "/reload")
	if body := rec.Body.String(); rec.Code != http.StatusOK ||
		!strings.Contains(body, `a.graphql"`) ||
		!strings.Contains(body, `"total":0,"errors":[]`) {
		t.Errorf("expected 200, the loaded file and no skips; received %d %s",
			rec.Code, body)
	}

	// Nothing else is served there.
	if rec := serveTo(t, mux, http.MethodGet, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 outside /reload; received %d", rec.Code)
	}
}

// TestControlReloadReportsSkipped pins that a document left out is named
// in the answer, so a deployment fails on it without reading the log.
func TestControlReloadReportsSkipped(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "ok.graphql", "{ a }")
	writeDoc(t, dir, "broken.graphql", "query Q {\n  f(a: 01)\n}")
	list := newAllowlist(t, dir)
	mux := controlHandler(t, list, dir, nil, "")

	rec := serveTo(t, mux, http.MethodPost, "/reload")
	body := rec.Body.String()

	// The reload itself worked, so it's a 200 that carries the bad news.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200; received %d %s", rec.Code, body)
	}
	var answer struct {
		Documents struct {
			Total int      `json:"total"`
			Files []string `json:"files"`
		} `json:"documents"`
		Skipped struct {
			Total  int      `json:"total"`
			Errors []string `json:"errors"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("answering no JSON: %v: %s", err, body)
	}
	if answer.Documents.Total != 1 || answer.Skipped.Total != 1 {
		t.Errorf("expected 1 document and 1 skip; received %+v", answer)
	}
	if len(answer.Documents.Files) != 1 ||
		!strings.HasSuffix(answer.Documents.Files[0], "ok.graphql") {
		t.Errorf("expected the loaded file to be named; received %v",
			answer.Documents.Files)
	}
	if len(answer.Skipped.Errors) != 1 ||
		!strings.Contains(answer.Skipped.Errors[0], "broken.graphql:2:9") {
		t.Errorf("expected the position of the error; received %v",
			answer.Skipped.Errors)
	}
}

// TestControlReloadFails covers a directory that can't be read,
// where the allowlist in use is kept.
func TestControlReloadFails(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	list := newAllowlist(t, dir)
	mux := controlHandler(t, list, dir, nil, "")

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if rec := serveTo(
		t, mux, http.MethodPost, "/reload",
	); rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500; received %d %s", rec.Code, rec.Body)
	}
	// The documents stay allowed: a failed reload changes nothing.
	if n := list.Len(); n != 1 {
		t.Errorf("expected the allowlist to be kept; %d documents", n)
	}
}

// TestLogReload covers what the proxy makes of a reload. The allowlist reports
// what happened and logs nothing, so every line below is this package's call:
// which outcome is worth an error, and what a deployment reads to find it.
func TestLogReload(t *testing.T) {
	logOf := func(t *testing.T, r allowlist.Result) string {
		t.Helper()
		logs := new(strings.Builder)
		logReload(zerolog.New(logs), "/srv/queries", r)
		return logs.String()
	}

	// A load that took everything is one line, at info: nothing needs attention.
	full := logOf(t, allowlist.Result{Files: []string{"a.graphql"}, Added: 1})
	for _, want := range []string{
		`"message":"allowlist loaded"`, `"documents":1`, `"added":1`,
		`"removed":0`, `"skipped":0`, `"dir":"/srv/queries"`,
	} {
		if !strings.Contains(full, want) {
			t.Errorf("expected %s; received %s", want, full)
		}
	}
	if strings.Contains(full, `"level":"error"`) {
		t.Errorf("expected nothing at error level; received %s", full)
	}

	// A document left out is an error: it was meant to be served and isn't.
	skipped := logOf(t, allowlist.Result{
		Files:   []string{"a.graphql"},
		Skipped: []error{errors.New("b.graphql:1:2: unexpected EOF")},
	})
	if !strings.Contains(skipped, `"message":"skipping a document"`) ||
		!strings.Contains(skipped, "b.graphql:1:2") ||
		!strings.Contains(skipped, `"skipped":1`) {
		t.Errorf("expected the skip to be reported; received %s", skipped)
	}

	// A schema that couldn't be read leaves every document unchecked,
	// which is a different thing from one document being left out.
	schema := logOf(t, allowlist.Result{
		Files:     []string{"a.graphql"},
		SchemaErr: errors.New("schema.graphqls: type Query {"),
	})
	if !strings.Contains(schema, "checked against none") ||
		!strings.Contains(schema, "schema.graphqls") {
		t.Errorf("expected the schema to be reported; received %s", schema)
	}

	// An empty allowlist rejects every request, which is loud in the counters
	// and silent otherwise, so it says so once.
	empty := logOf(t, allowlist.Result{})
	if !strings.Contains(empty, "every request is rejected") ||
		!strings.Contains(empty, `"level":"error"`) {
		t.Errorf("expected the empty allowlist to be reported; received %s", empty)
	}
}
