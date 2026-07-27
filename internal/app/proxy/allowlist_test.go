package proxy

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/parser"
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

func newTestLoader(t *testing.T, dir string, exact bool) (*Store, *Loader) {
	t.Helper()
	store := new(Store)
	return store, NewLoader(store, dir, exact, sha256.New,
		gqlhash.Options{}, testLogger())
}

func TestLoaderLoad(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	writeDoc(t, dir, "b.gql", "query B { bar }")
	writeDoc(t, dir, "nested/c.graphql", "{ nested }")
	// Files that aren't documents are ignored.
	writeDoc(t, dir, "README.md", "not a document")
	writeDoc(t, dir, "d.graphql~", "{ backup }")
	writeDoc(t, dir, ".hidden.graphql", "{ hidden }")

	store, loader := newTestLoader(t, dir, false)
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := store.Load().Len(); n != 3 {
		t.Errorf("expected 3 documents; received %d", n)
	}

	// A document is found by the hash of its canonical form, whatever its formatting.
	key := hashOf(t, "{\n\t# comment\n\tfoo\n}")
	if e := store.Load().Lookup(key); e == nil {
		t.Error("expected the reformatted document to be found")
	} else if !strings.HasSuffix(e.Name, "a.graphql") {
		t.Errorf("expected a.graphql; received %s", e.Name)
	}
	if store.Load().Lookup(hashOf(t, "{ other }")) != nil {
		t.Error("expected an unknown document not to be found")
	}
}

func TestLoaderEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	logs := new(strings.Builder)
	store := new(Store)
	loader := NewLoader(store, dir, false, sha256.New, gqlhash.Options{},
		zerolog.New(logs))

	// An empty allowlist rejects every request, so it's reported.
	// It doesn't keep the proxy from serving.
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := store.Load().Len(); n != 0 {
		t.Errorf("expected 0 documents; received %d", n)
	}
	if !strings.Contains(logs.String(), "every request is rejected") {
		t.Errorf("expected an error about the empty allowlist; received %s",
			logs.String())
	}
	if !strings.Contains(logs.String(), `"level":"error"`) {
		t.Errorf("expected it at error level; received %s", logs.String())
	}
}

func TestLoaderInvalidDocument(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "ok.graphql", "{ foo }")
	writeDoc(t, dir, "broken.graphql", "query Q {\n  f(a: 01)\n}")
	writeDoc(t, dir, "broken.gql", "query Q {")

	logs := new(strings.Builder)
	store := new(Store)
	loader := NewLoader(store, dir, false, sha256.New, gqlhash.Options{},
		zerolog.New(logs))
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The documents that parse are served, whatever the others do.
	if n := store.Load().Len(); n != 1 {
		t.Errorf("expected 1 document; received %d", n)
	}
	if store.Load().Lookup(hashOf(t, "{ foo }")) == nil {
		t.Error("expected the readable document to be allowed")
	}

	// Each skipped file is reported with the position of the error, and both
	// extensions are treated alike.
	for _, want := range []string{
		"broken.graphql:2:9", "broken.gql:1:10",
		"skipping a document that doesn't parse", `"skipped":2`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("expected %q in the log; received %s", want, logs.String())
		}
	}
}

// TestLoaderAllInvalid covers a directory where nothing is usable, which is the
// empty allowlist reached the other way.
func TestLoaderAllInvalid(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "broken.graphql", "query Q {")

	logs := new(strings.Builder)
	store := new(Store)
	loader := NewLoader(store, dir, false, sha256.New, gqlhash.Options{},
		zerolog.New(logs))
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := store.Load().Len(); n != 0 {
		t.Errorf("expected 0 documents; received %d", n)
	}
	if !strings.Contains(logs.String(), "every request is rejected") {
		t.Errorf("expected the empty allowlist to be reported; received %s",
			logs.String())
	}
}

func TestLoaderDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	writeDoc(t, dir, "b.graphql", "{\n  foo\n}")

	store, loader := newTestLoader(t, dir, false)
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The two files hold the same document, so the allowlist holds one entry.
	if n := store.Load().Len(); n != 1 {
		t.Errorf("expected 1 document; received %d", n)
	}
}

func TestLoaderExact(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	store, loader := newTestLoader(t, dir, true)
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The key is the canonical form itself, so it holds no hash.
	var canon appender
	if e := parser.Parse(&canon, gqlhash.Options{}, "{ foo }"); e.IsErr() {
		t.Fatal(e)
	}
	if store.Load().Lookup(canon.buf) == nil {
		t.Error("expected the document to be found by its canonical form")
	}
}

func TestLoaderMissingDirectory(t *testing.T) {
	store, loader := newTestLoader(t, filepath.Join(t.TempDir(), "nope"), false)
	if err := loader.Load(); err == nil {
		t.Fatal("expected an error")
	}
	if store.Load() != nil {
		t.Error("expected nothing to be published")
	}
}

func TestLoaderWatch(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	store, loader := newTestLoader(t, dir, false)
	if err := loader.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	done := make(chan struct{})
	defer close(done)
	const interval = 10 * time.Millisecond
	go loader.Watch(interval, done)

	// A new document appears once the directory settled.
	writeDoc(t, dir, "b.graphql", "{ bar }")
	waitFor(t, func() bool {
		return store.Load().Lookup(hashOf(t, "{ bar }")) != nil
	}, "the new document to be picked up")

	// A removed document disappears.
	if err := os.Remove(filepath.Join(dir, "a.graphql")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return store.Load().Lookup(hashOf(t, "{ foo }")) == nil
	}, "the removed document to disappear")

	// A broken document is skipped, the working ones keep being served.
	writeDoc(t, dir, "c.graphql", "{ broken")
	time.Sleep(10 * interval)
	if store.Load().Lookup(hashOf(t, "{ bar }")) == nil {
		t.Error("expected the working document to stay allowed")
	}
	// Nothing was added by it: a document that doesn't parse has no hash.
	if n := store.Load().Len(); n != 1 {
		t.Errorf("expected the broken document to be skipped; %d documents", n)
	}
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

// hashOf returns the allowlist key of a document under the default options.
func hashOf(t *testing.T, document string) []byte {
	t.Helper()
	h := sha256.New()
	sum, err := gqlhash.AppendHash(nil, h, gqlhash.Options{}, document)
	if err.IsErr() {
		t.Fatalf("hashing %q: %v", document, err)
	}
	return sum
}
