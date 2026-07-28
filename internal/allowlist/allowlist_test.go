package allowlist_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
	"github.com/romshark/gqlhash/v2/parser"
)

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

// newAllowlist returns an allowlist and a reload of dir, so a test names the
// directory once rather than at every call.
func newAllowlist(
	t *testing.T, dir string,
) (*allowlist.Allowlist, func() (allowlist.Result, error)) {
	t.Helper()
	list := allowlist.New(sha256.New, gqlhash.Options{})
	return list, func() (allowlist.Result, error) { return list.Reload(dir) }
}

// unreadable puts a file at dir/name that can't be read: a symlink to a target
// that doesn't exist. [filepath.WalkDir] doesn't follow it, so the allowlist takes
// it for a document and fails at [os.ReadFile], which is the failure under test.
//
// Why not chmod 000: root reads a file whatever its mode, so that test would
// skip wherever the suite runs as root, which is any ordinary container,
// and a skip covers nothing while looking like a pass. This one fails for root too.
func unreadable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), path); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestAllowlistBeforeFirstReload covers an allowlist between [allowlist.New] and
// its first reload: it holds nothing, and every question about it is answered
// rather than panicking, since the metrics collector and the status endpoint read
// it whenever they're asked, which can be before the proxy finished starting.
func TestAllowlistBeforeFirstReload(t *testing.T) {
	list, _ := newAllowlist(t, t.TempDir())

	if list.Allowed(hashOf(t, "{ foo }")) {
		t.Error("expected nothing to be allowed before the first reload")
	}
	if n := list.Len(); n != 0 {
		t.Errorf("expected 0 documents; received %d", n)
	}
	documents, loadedAt := list.Stats()
	if documents != 0 || !loadedAt.IsZero() {
		t.Errorf("expected nothing loaded; received %d at %v", documents, loadedAt)
	}
}

// TestAllowlistStats covers the time a reload stamps: it's when that list was
// published, so it follows the list in use rather than being stamped once.
func TestAllowlistStats(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	list, reload := newAllowlist(t, dir)

	// The stamp bounds the reload, which is what pins it to that reload and not
	// to a later one: the second call has to land in its own window.
	load := func(t *testing.T) {
		t.Helper()
		before := time.Now()
		if _, err := reload(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		documents, loadedAt := list.Stats()
		if documents != 1 {
			t.Errorf("expected 1 document; received %d", documents)
		}
		if loadedAt.Before(before) || loadedAt.After(time.Now()) {
			t.Errorf("expected the load time within the reload; received %v", loadedAt)
		}
	}

	load(t)
	load(t)
}

// TestAllowlistSchema covers a directory holding a schema:
// a document the schema doesn't take is skipped, and the rest is served.
func TestAllowlistSchema(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "schema.graphqls", `
		type Query { user(id: ID!): User }
		type User { id: ID!  name: String! }
	`)
	writeDoc(t, dir, "ok.graphql", "{ user(id: 1) { name } }")
	writeDoc(t, dir, "unknown-field.graphql", "{ user(id: 1) { nope } }")
	writeDoc(t, dir, "unknown-operation.graphql", "mutation { deleteUser(id: 1) }")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Files) != 1 ||
		!strings.HasSuffix(result.Files[0], "ok.graphql") {
		t.Errorf("expected only ok.graphql; received %v", result.Files)
	}
	if !list.Allowed(hashOf(t, "{ user(id: 1) { name } }")) {
		t.Error("expected the document the schema takes to be served")
	}
	if list.Allowed(hashOf(t, "{ user(id: 1) { nope } }")) {
		t.Error("expected the document with the unknown field to be skipped")
	}

	// Each one is reported with the position of what the schema objected to.
	if len(result.Skipped) != 2 {
		t.Fatalf("expected 2 skipped; received %v", result.Skipped)
	}
	for _, want := range []string{
		`unknown-field.graphql:1:17: Cannot query field "nope" on type "User"`,
		`unknown-operation.graphql:1:1: ` +
			`Schema does not support operation type "mutation"`,
	} {
		var found bool
		for _, e := range result.Skipped {
			found = found || strings.Contains(e.Error(), want)
		}
		if !found {
			t.Errorf("expected %q among the skipped; received %v", want,
				result.Skipped)
		}
	}
}

// TestAllowlistSchemaSeveralFiles tests that every .graphqls file of the directory
// is read as one schema. Neither file below is a schema on its own, since one names a
// type the other defines, so if they weren't joined there would be no schema,
// and no schema means no check at all.
func TestAllowlistSchemaSeveralFiles(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "query.graphqls", "type Query { user(id: ID!): User }")
	writeDoc(t, dir, "user.graphqls", "type User { id: ID!  name: String! }")
	writeDoc(t, dir, "ok.graphql", "{ user(id: 1) { name } }")
	writeDoc(t, dir, "unknown-field.graphql", "{ user(id: 1) { nope } }")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A schema that doesn't load is reported instead, see TestAllowlistBrokenSchema.
	if result.SchemaErr != nil {
		t.Errorf("expected the two files to load as one schema; received %v",
			result.SchemaErr)
	}

	// The field the second file defines is taken.
	if len(result.Files) != 1 ||
		!strings.HasSuffix(result.Files[0], "ok.graphql") {
		t.Errorf("expected only ok.graphql; received %v", result.Files)
	}
	if !list.Allowed(hashOf(t, "{ user(id: 1) { name } }")) {
		t.Error("expected the document the joined schema takes to be served")
	}

	// A field it doesn't define is not, which is what proves the join:
	// either file alone leaves the schema unloadable and every document allowed.
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0].Error(),
		`unknown-field.graphql:1:17: Cannot query field "nope" on type "User"`) {
		t.Errorf("expected the unknown field to be skipped; received %v", result.Skipped)
	}
}

// TestAllowlistBrokenSchema covers a schema that doesn't parse: it's reported,
// and the documents are served without a schema check rather than not at all.
func TestAllowlistBrokenSchema(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "schema.graphqls", "type Query {")
	writeDoc(t, dir, "a.graphql", "{ whatever }")

	_, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("expected the document to be served; received %v", result.Files)
	}
	// The schema is no file left out: it's what every document was to be checked
	// against, so it's reported on its own.
	if result.SchemaErr == nil ||
		!strings.Contains(result.SchemaErr.Error(), "schema.graphqls") {
		t.Errorf("expected the schema to be reported; received %v", result.SchemaErr)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("expected no document to be skipped; received %v", result.Skipped)
	}
}

// TestAllowlistUnreadableSchema covers a schema file the allowlist can't read.
// It ends where TestAllowlistBrokenSchema ends, by a read failure rather than a syntax one:
// the file is reported and the documents are served against no schema rather than
// not at all. The document below is one no schema naming its fields would take,
// so it's served because there is no schema to check it against.
func TestAllowlistUnreadableSchema(t *testing.T) {
	dir := t.TempDir()
	unreadable(t, dir, "schema.graphqls")
	writeDoc(t, dir, "a.graphql", "{ whatever }")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The schema is no file left out: it's what every document was to be checked
	// against, so it's reported on its own.
	if result.SchemaErr == nil ||
		!strings.Contains(result.SchemaErr.Error(), "schema.graphqls") {
		t.Errorf("expected the schema to be reported; received %v", result.SchemaErr)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("expected no document to be skipped; received %v", result.Skipped)
	}

	if len(result.Files) != 1 {
		t.Errorf("expected the document to be served; received %v", result.Files)
	}
	if !list.Allowed(hashOf(t, "{ whatever }")) {
		t.Error("expected the document to be allowed without a schema check")
	}
}

// TestAllowlistHiddenDirectory covers a hidden directory, which is skipped whole
// rather than walked: an editor or a tool that keeps its state under a dotted
// name puts nothing on the allowlist, whatever it holds.
//
// The directory the allowlist reads is the exception, since naming it .queries
// is a choice a deployment made, and skipping it would serve nothing and say
// only that the directory was empty.
func TestAllowlistHiddenDirectory(t *testing.T) {
	t.Run("under the directory", func(t *testing.T) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", "{ foo }")
		writeDoc(t, dir, ".git/objects/b.graphql", "{ hidden }")
		writeDoc(t, dir, "nested/.cache/c.graphql", "{ alsoHidden }")

		list, reload := newAllowlist(t, dir)
		result, err := reload()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Files) != 1 ||
			!strings.HasSuffix(result.Files[0], "a.graphql") {
			t.Errorf("expected only a.graphql; received %v", result.Files)
		}
		// Skipped whole and not read at all, so neither is served and neither is
		// reported: a hidden directory is no document that was left out.
		if len(result.Skipped) != 0 {
			t.Errorf("expected nothing skipped; received %v", result.Skipped)
		}
		for _, document := range []string{"{ hidden }", "{ alsoHidden }"} {
			if list.Allowed(hashOf(t, document)) {
				t.Errorf("expected %s not to be served", document)
			}
		}
	})

	t.Run("the directory itself", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), ".queries")
		writeDoc(t, dir, "a.graphql", "{ foo }")

		list, reload := newAllowlist(t, dir)
		if _, err := reload(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !list.Allowed(hashOf(t, "{ foo }")) {
			t.Error("expected a dotted directory to be read like any other")
		}
	})
}

func TestAllowlistLoad(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	writeDoc(t, dir, "b.gql", "query B { bar }")
	writeDoc(t, dir, "nested/c.graphql", "{ nested }")
	// Files that aren't documents are ignored.
	writeDoc(t, dir, "README.md", "not a document")
	writeDoc(t, dir, "d.graphql~", "{ backup }")
	writeDoc(t, dir, ".hidden.graphql", "{ hidden }")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := list.Len(); n != 3 {
		t.Errorf("expected 3 documents; received %d", n)
	}

	// The list holds hashes, so which file a document came from is answered by
	// the result of the load rather than by a lookup.
	if len(result.Files) != 3 {
		t.Fatalf("expected 3 files; received %v", result.Files)
	}
	for _, want := range []string{"a.graphql", "b.gql", "nested/c.graphql"} {
		var found bool
		for _, f := range result.Files {
			found = found || strings.HasSuffix(f, want)
		}
		if !found {
			t.Errorf("expected %s among the loaded; received %v", want, result.Files)
		}
	}

	// A document is found by the hash of its canonical form, whatever its formatting.
	if !list.Allowed(hashOf(t, "{\n\t# comment\n\tfoo\n}")) {
		t.Error("expected the reformatted document to be found")
	}
	if list.Allowed(hashOf(t, "{ other }")) {
		t.Error("expected an unknown document not to be found")
	}
}

// TestAllowlistEmptyAllowlist covers the two routes to an allowlist that serves
// nothing: a directory holding no documents, and one where every document fails.
// The end state is the same and is reported the same way.
// An empty allowlist rejects every request, so it's an error,
// but it doesn't keep the proxy from serving.
func TestAllowlistEmptyAllowlist(t *testing.T) {
	for _, td := range []struct {
		name  string
		write func(t *testing.T, dir string)

		// skipped is what the route has to report, so the second case can't
		// pass by leaving the directory empty the way the first one does.
		skipped int
	}{
		{"no files", func(*testing.T, string) {}, 0},
		{"every document invalid", func(t *testing.T, dir string) {
			writeDoc(t, dir, "broken.graphql", "query Q {")
		}, 1},
	} {
		t.Run(td.name, func(t *testing.T) {
			dir := t.TempDir()
			td.write(t, dir)
			list, reload := newAllowlist(t, dir)

			result, err := reload()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Skipped) != td.skipped {
				t.Errorf("expected %d skipped; received %v", td.skipped, result.Skipped)
			}
			// Empty and not nil: the reload endpoint answers with this list,
			// and a null there is a case every client would have to carry.
			if result.Files == nil {
				t.Error("expected an empty list of files, not nil")
			}
			if n := list.Len(); n != 0 {
				t.Errorf("expected 0 documents; received %d", n)
			}
			// That an empty allowlist is worth an error is the proxy's call,
			// see TestLogReload.
			if len(result.Files) != 0 {
				t.Errorf("expected no file loaded; received %v", result.Files)
			}
		})
	}
}

func TestAllowlistInvalidDocument(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "ok.graphql", "{ foo }")
	writeDoc(t, dir, "broken.graphql", "query Q {\n  f(a: 01)\n}")
	writeDoc(t, dir, "broken.gql", "query Q {")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// What was loaded and what was left out is reported back,
	// so a reload can answer with both.
	if len(result.Files) != 1 || !strings.HasSuffix(result.Files[0], "ok.graphql") {
		t.Errorf("expected ok.graphql to be loaded; received %v", result.Files)
	}
	if len(result.Skipped) != 2 {
		t.Errorf("expected 2 skipped; received %v", result.Skipped)
	}
	for _, want := range []string{"broken.graphql:2:9", "broken.gql:1:10"} {
		var found bool
		for _, e := range result.Skipped {
			found = found || strings.Contains(e.Error(), want)
		}
		if !found {
			t.Errorf("expected %q among the skipped; received %v", want,
				result.Skipped)
		}
	}

	// The documents that parse are served, whatever the others do.
	if n := list.Len(); n != 1 {
		t.Errorf("expected 1 document; received %d", n)
	}
	if !list.Allowed(hashOf(t, "{ foo }")) {
		t.Error("expected the readable document to be allowed")
	}

}

// nested returns a document whose selection sets nest depth deep.
func nested(depth int) string {
	return "{" + strings.Repeat("f{", depth-1) + "f" + strings.Repeat("}", depth)
}

// TestAllowlistTooDeepDocument covers the depth limit at the allowlist:
// a document nesting past it is skipped like any other that doesn't parse,
// and the reason says it's too deep.
// A directory can't put on the allowlist what the proxy would refuse to hash.
// TestParseDepthLimit covers the limit itself.
func TestAllowlistTooDeepDocument(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "ok.graphql", nested(parser.DefaultDepthLimit))
	writeDoc(t, dir, "deep.graphql", nested(parser.DefaultDepthLimit+1))

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A document at the limit is deep, not too deep, so the limit is what
	// separates the two and not the nesting on its own.
	if len(result.Files) != 1 ||
		!strings.HasSuffix(result.Files[0], "ok.graphql") {
		t.Errorf("expected the document at the limit to be loaded; received %v",
			result.Files)
	}
	if !list.Allowed(hashOf(t, nested(parser.DefaultDepthLimit))) {
		t.Error("expected the document at the limit to be allowed")
	}

	if len(result.Skipped) != 1 ||
		!strings.Contains(result.Skipped[0].Error(), parser.ErrTooDeep.Error()) {
		t.Errorf("expected the too deep document to be skipped; received %v",
			result.Skipped)
	}
}

// TestAllowlistUnreadableDocument covers a document the allowlist can't read, which is a
// deployment mistake rather than a broken document: the file is named and the rest of
// the directory is served, so one bad mode doesn't take the allowlist down with it.
func TestAllowlistUnreadableDocument(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "ok.graphql", "{ foo }")
	unreadable(t, dir, "locked.graphql")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Skipped) != 1 ||
		!strings.Contains(result.Skipped[0].Error(), "locked.graphql") {
		t.Errorf("expected the unreadable file to be reported; received %v",
			result.Skipped)
	}

	// The rest of the directory is served.
	if len(result.Files) != 1 ||
		!strings.HasSuffix(result.Files[0], "ok.graphql") {
		t.Errorf("expected ok.graphql to be loaded; received %v", result.Files)
	}
	if !list.Allowed(hashOf(t, "{ foo }")) {
		t.Error("expected the readable document to be allowed")
	}
}

// TestAllowlistSharedHash covers two files whose documents hash alike:
// neither is served, since which one a request meant is unknowable.
func TestAllowlistSharedHash(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	writeDoc(t, dir, "b.graphql", "{\n  foo\n}")
	writeDoc(t, dir, "c.graphql", "{ bar }")

	list, reload := newAllowlist(t, dir)
	result, err := reload()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only the document nothing clashes with is served.
	if n := list.Len(); n != 1 {
		t.Errorf("expected 1 document; received %d", n)
	}
	if list.Allowed(hashOf(t, "{ foo }")) {
		t.Error("expected neither of the two to be served")
	}
	if !list.Allowed(hashOf(t, "{ bar }")) {
		t.Error("expected the document without a clash to be served")
	}

	// Both are reported, each naming the other.
	if len(result.Skipped) != 2 {
		t.Fatalf("expected 2 skipped; received %v", result.Skipped)
	}
	for _, want := range []string{
		"a.graphql: the same hash as", "b.graphql: the same hash as",
	} {
		var found bool
		for _, e := range result.Skipped {
			found = found || strings.Contains(e.Error(), want)
		}
		if !found {
			t.Errorf("expected %q among the skipped; received %v", want,
				result.Skipped)
		}
	}
}

func TestAllowlistMissingDirectory(t *testing.T) {
	list, reload := newAllowlist(t, filepath.Join(t.TempDir(), "nope"))
	if _, err := reload(); err == nil {
		t.Fatal("expected an error")
	}
	// Nothing is published, so the failed reload leaves the allowlist as it was:
	// empty here, and whatever it held where a reload follows a good one.
	if documents, loadedAt := list.Stats(); documents != 0 || !loadedAt.IsZero() {
		t.Errorf("expected nothing to be published; received %d at %v",
			documents, loadedAt)
	}
}

// TestAllowlistReload covers what the control endpoint does: read the directory
// again and publish what it finds now.
func TestAllowlistReload(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	list, reload := newAllowlist(t, dir)
	if _, err := reload(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A new document counts once it's reloaded, not before.
	writeDoc(t, dir, "b.graphql", "{ bar }")
	if list.Allowed(hashOf(t, "{ bar }")) {
		t.Error("expected the new document to wait for a reload")
	}
	if _, err := reload(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !list.Allowed(hashOf(t, "{ bar }")) {
		t.Error("expected the new document after the reload")
	}

	// A removed document disappears.
	if err := os.Remove(filepath.Join(dir, "a.graphql")); err != nil {
		t.Fatal(err)
	}
	if _, err := reload(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list.Allowed(hashOf(t, "{ foo }")) {
		t.Error("expected the removed document to be gone")
	}

	// A broken document is skipped and the working ones keep being served.
	writeDoc(t, dir, "c.graphql", "{ broken")
	if _, err := reload(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !list.Allowed(hashOf(t, "{ bar }")) {
		t.Error("expected the working document to stay allowed")
	}
	if n := list.Len(); n != 1 {
		t.Errorf("expected the broken document to be skipped; %d documents", n)
	}
}

// TestAllowlistConcurrentLoad pins that Load serializes its callers.
// Several requests to the control endpoint may reach it at once.
func TestAllowlistConcurrentLoad(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")
	list, reload := newAllowlist(t, dir)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := reload(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	if n := list.Len(); n != 1 {
		t.Errorf("expected 1 document; received %d", n)
	}
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
