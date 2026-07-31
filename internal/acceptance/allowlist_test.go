package acceptance

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reload publishes what's on disk and answers what the reload reported.
func reload(t *testing.T, e *env) (answer reloadAnswerShape) {
	t.Helper()
	code, body := control(t, e.server, http.MethodPost, "/reload", "")
	if code != http.StatusOK {
		t.Fatalf("reload: %d: %s", code, body)
	}
	return reloadAnswer(t, body)
}

// TestAllowlistExtensions covers which files hold documents. Both extensions
// are read, and nothing else is: a directory beside the documents may hold a
// README or a generator's manifest without either being served or skipped.
func TestAllowlistExtensions(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		writeDoc(t, e.dir, "a.graphql", allowedDoc)
		writeDoc(t, e.dir, "b.gql", "{ b }")
		writeDoc(t, e.dir, "notes.md", "# not a document")
		writeDoc(t, e.dir, "manifest.json", `{"a":1}`)

		answer := reload(t, e)
		if answer.Documents.Total != 2 || answer.Skipped.Total != 0 {
			t.Fatalf("expected the two documents and no skip; received %+v", answer)
		}
		for _, request := range []string{docAllowed, `{"query":"{b}"}`} {
			if code, _ := post(t, e.server, request); code != http.StatusOK {
				t.Errorf("%s: expected it served; received %d", request, code)
			}
		}
	})
}

// TestAllowlistWalksDirectories covers the layout a generator writes: documents
// in directories of their own, and dot directories left alone.
// A reader that only lists the top level serves none of them.
func TestAllowlistWalksDirectories(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		writeDocAt(t, e.dir, "user/get.graphql", allowedDoc)
		writeDocAt(t, e.dir, "user/nested/deep.graphql", "{ deep }")
		// What a checkout, an editor or a Kubernetes mount leaves behind.
		writeDocAt(t, e.dir, ".git/objects/x.graphql", "{ fromGit }")
		writeDocAt(t, e.dir, ".hidden.graphql", "{ hidden }")

		answer := reload(t, e)
		if answer.Documents.Total != 2 {
			t.Fatalf("expected the two documents under the directories; received %+v",
				answer)
		}
		for _, request := range []string{docAllowed, `{"query":"{deep}"}`} {
			if code, _ := post(t, e.server, request); code != http.StatusOK {
				t.Errorf("%s: expected it served; received %d", request, code)
			}
		}
		// A dot file is nobody's document, and neither is one under a dot
		// directory: serving out of .git is how a stale document comes back.
		for _, request := range []string{`{"query":"{fromGit}"}`, `{"query":"{hidden}"}`} {
			if code, _ := post(t, e.server, request); code != http.StatusForbidden {
				t.Errorf("%s: expected it left alone; received %d", request, code)
			}
		}
	})
}

// TestAllowlistDottedRoot covers -allowlist naming a directory that itself
// begins with a dot, which the rule above must not swallow: dot files are
// skipped inside the directory, not the directory itself.
func TestAllowlistDottedRoot(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := filepath.Join(t.TempDir(), ".queries")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeDoc(t, dir, "a.graphql", allowedDoc)

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)
		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the document served; received %d: %s", code, answer)
		}
	})
}

// TestAllowlistSymlinks covers a Kubernetes ConfigMap mount, where every entry
// is a symlink into a hidden data directory. Reading only regular files serves
// nothing there.
func TestAllowlistSymlinks(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		// The data a mount points at, outside the allowlist directory.
		data := t.TempDir()
		writeDoc(t, data, "get.graphql", allowedDoc)
		if err := os.Symlink(filepath.Join(data, "get.graphql"),
			filepath.Join(e.dir, "get.graphql")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		answer := reload(t, e)
		if answer.Documents.Total != 1 {
			t.Fatalf("expected the symlinked document; received %+v", answer)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected it served; received %d", code)
		}
	})
}

// TestAllowlistEmptyDocument covers a file holding no definition: a generator
// that wrote nothing, or a document commented out. It's a skip that's reported,
// not a file quietly ignored, since somebody meant it to be served.
func TestAllowlistEmptyDocument(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		writeDoc(t, e.dir, "a.graphql", allowedDoc)
		writeDoc(t, e.dir, "empty.graphql", "")
		writeDoc(t, e.dir, "comments.graphql", "# nothing here\n\n# nor here\n")

		answer := reload(t, e)
		if answer.Documents.Total != 1 {
			t.Errorf("expected the one document; received %+v", answer)
		}
		if answer.Skipped.Total != 2 {
			t.Fatalf("expected both reported as skipped; received %+v", answer)
		}
		named := strings.Join(answer.Skipped.Errors, "\n")
		for _, file := range []string{"empty.graphql", "comments.graphql"} {
			if !strings.Contains(named, file) {
				t.Errorf("expected %s named; received %v", file, answer.Skipped.Errors)
			}
		}
	})
}

// TestAllowlistCollisionIsNWay covers more than two files whose documents hash
// alike: none of them is served, all of them are named, and the hash stays
// unserved rather than falling to whichever file the walk reached last.
func TestAllowlistCollisionIsNWay(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		// The same document written three ways, which hash alike.
		writeDoc(t, e.dir, "a.graphql", allowedDoc)
		writeDoc(t, e.dir, "b.graphql", "query GetUser{user(id:1){name}}")
		writeDoc(t, e.dir, "c.graphql", "query GetUser {  user(id: 1) { name } }")

		answer := reload(t, e)
		if answer.Documents.Total != 0 || answer.Skipped.Total != 3 {
			t.Fatalf("expected none served and all three named; received %+v", answer)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected the hash unserved; received %d", code)
		}

		// With the copies gone the document is one file's again.
		for _, name := range []string{"b.graphql", "c.graphql"} {
			if err := os.Remove(filepath.Join(e.dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		if answer := reload(t, e); answer.Documents.Total != 1 {
			t.Fatalf("expected the survivor served; received %+v", answer)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected it served again; received %d", code)
		}
	})
}

// TestAllowlistCollisionUnderIgnore covers -ignore making two documents the same:
// what collides is what the hash covers, so a mode that leaves values out
// takes files that differ only in a value and serves neither.
func TestAllowlistCollisionUnderIgnore(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "one.graphql", "query GetUser{user(id:1){name}}")
		writeDoc(t, dir, "two.graphql", "query GetUser{user(id:2){name}}")

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir,
			"-ignore", "inputs")

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 0 || answer.Skipped.Total != 2 {
			t.Fatalf("expected neither served; received %s", body)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected the collision to leave nothing served; received %d", code)
		}
	})
}

// TestAllowlistStartupMatchesReload covers the load a run does before it
// listens: the same rules as a reload, so a directory holding one broken file
// serves the rest rather than refusing to start.
// What "fails fast" would refuse is a deployment that a reload would have kept alive.
func TestAllowlistStartupMatchesReload(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		writeDoc(t, dir, "broken.graphql", "query Q {\n  f(a: 01)\n}")
		writeDoc(t, dir, "empty.graphql", "")
		writeDocAt(t, dir, "nested/b.gql", "{ b }")

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		// It's serving, and serving what parsed.
		for _, request := range []string{docAllowed, `{"query":"{b}"}`} {
			if code, _ := post(t, s, request); code != http.StatusOK {
				t.Errorf("%s: expected it served; received %d", request, code)
			}
		}
		// A reload of the same directory reports what the startup loaded.
		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 2 || answer.Skipped.Total != 2 {
			t.Errorf("expected the same two served and two skipped; received %s", body)
		}
	})
}

// TestAllowlistRootShapes covers what -allowlist may name.
//
// The symlinked root is the one that matters: a deploy swaps an allowlist
// atomically by pointing a link at a new directory, and a walk that lstats its
// root sees the link, loads nothing and rejects every request.
// That fails closed, but it fails — and an implementation listing the directory instead
// would serve v1 and then v2 where this served neither, which is the unsafe direction,
// and pass a suite that never looked.
func TestAllowlistRootShapes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		base := t.TempDir()
		v1, v2 := filepath.Join(base, "v1"), filepath.Join(base, "v2")
		writeDocAt(t, v1, "a.graphql", allowedDoc)
		writeDocAt(t, v2, "a.graphql", rejectedText)
		current := filepath.Join(base, "current")
		if err := os.Symlink(v1, current); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql",
			"-allowlist", current)

		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Fatalf("expected the linked directory served; received %d: %s",
				code, answer)
		}

		// The atomic swap a deploy does: a new link moved over the old one.
		// The proxy is told to reload, and what it serves is what the link now points at.
		tmp := filepath.Join(base, "tmp")
		if err := os.Symlink(v2, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, current); err != nil {
			t.Fatal(err)
		}
		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		if answer := reloadAnswer(t, body); answer.Documents.Total != 1 {
			t.Fatalf("expected the new directory loaded; received %s", body)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected the old document refused after the swap; received %d",
				code)
		}
		if code, _ := post(t, s, docRejected); code != http.StatusOK {
			t.Errorf("expected the new document served after the swap; received %d",
				code)
		}
	})
}

// TestAllowlistRootIsAFile covers -allowlist naming one document rather than a
// directory of them. It loads that document: an implementation that required a
// directory would refuse what this accepts,
// and a one-document allowlist is a reasonable thing to deploy.
func TestAllowlistRootIsAFile(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql",
			"-allowlist", filepath.Join(dir, "a.graphql"))

		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the named document served; received %d: %s",
				code, answer)
		}
		if code, _ := post(t, s, docRejected); code != http.StatusForbidden {
			t.Errorf("expected everything else refused; received %d", code)
		}
	})
}

// TestAllowlistFragmentPerFile covers the rule that one file is one document.
//
// A fragment in one file and the query using it in another are two documents,
// not one source set: the fragment is never used and the query's spread is
// unknown, so with a schema present both are skipped. Pooling the directory —
// a plausible reading of "a directory of documents" — would serve it,
// and would change the hash of every document that uses a fragment.
func TestAllowlistFragmentPerFile(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "fragment.graphql", "fragment F on User { name }")
		writeDoc(t, dir, "query.graphql",
			"query GetUser { user(id: 1) { ...F } }")
		writeDoc(t, dir, "schema.graphqls",
			"type Query { user(id: Int!): User }\ntype User { name: String }")

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 0 {
			t.Errorf("expected neither loaded; received %s", body)
		}
		if answer.Skipped.Total != 2 {
			t.Errorf("expected both skipped; received %s", body)
		}

		// And the request that would have needed them pooled is refused.
		split, err := jsonRequest("query GetUser { user(id: 1) { ...F } }\n" +
			"fragment F on User { name }")
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := post(t, s, split); code != http.StatusForbidden {
			t.Errorf("expected it refused; received %d", code)
		}
	})
}

// TestAllowlistSchemaSeveralFiles covers the generated layout, where a schema
// is split across files. They're read as one schema: loading each on its own
// leaves both invalid, which sets the schema error and — per the ruled
// fail-open behaviour — serves every document unchecked. That's the schema
// check silently disabling itself for the most ordinary layout there is.
func TestAllowlistSchemaSeveralFiles(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		// Neither file is a valid schema alone: one names a type the other
		// defines.
		writeDoc(t, dir, "q.graphqls", "type Query { user(id: Int!): User }")
		writeDoc(t, dir, "u.graphqls", "type User { name: String }")
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// A document the schema refuses, which is only refused if the schema
		// parsed at all.
		writeDoc(t, dir, "b.graphql", "query B { user(id: 1) { nosuchfield } }")

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 {
			t.Errorf("expected only the valid document loaded; received %s", body)
		}
		if answer.Skipped.Total != 1 {
			t.Errorf("expected the one the schema refuses skipped; received %s",
				body)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the valid document served; received %d", code)
		}
	})
}

// TestAllowlistEmptySchemaFile covers a .graphqls holding nothing.
//
// A schema that defines no type, not the absence of a schema: every document
// asks for something it doesn't have, so every one is skipped and every request refused.
// Reading a blank file as "no schema here" would be fail-open where
// this is fail-closed, and a generator writing one is the accident to catch.
func TestAllowlistEmptySchemaFile(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "schema.graphqls", "")
		writeDoc(t, dir, "a.graphql", allowedDoc)

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		if answer := reloadAnswer(t, body); answer.Documents.Total != 0 ||
			answer.Skipped.Total != 1 {
			t.Errorf("expected the document skipped against an empty schema;"+
				" received %s", body)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected every request refused; received %d", code)
		}
	})
}

// TestAllowlistDocumentByteNoise covers what a generator or an editor leaves in
// a file: a UTF-8 BOM, and CRLF line endings. Both load, and both match the
// plain-LF document a client sends — the hash is over the canonical form,
// so what differs here is what a reader has to get past to reach it.
func TestAllowlistDocumentByteNoise(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)

		// The BOM is written as an escape: a literal one in a Go source file
		// is a byte order mark in the source file, which the compiler refuses.
		const bom = "\ufeff"

		for _, tc := range []struct{ name, content string }{
			{"a UTF-8 BOM", bom + allowedDoc},
			{"CRLF line endings", strings.ReplaceAll(allowedDoc, "\n", "\r\n")},
			{"a BOM and CRLF", bom +
				strings.ReplaceAll(allowedDoc, "\n", "\r\n")},
			{"a trailing newline", allowedDoc + "\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e.allow(t, tc.content)
				if code, answer := post(t, e.server, docAllowed); code !=
					http.StatusOK {
					t.Errorf("expected the plain document to match; received %d: %s",
						code, answer)
				}
			})
		}
	})
}

// TestAllowlistSymlinkedSubdirectory covers a symlink to a directory *inside*
// the allowlist, as against the symlinked root TestAllowlistRootShapes covers.
//
// The root is resolved because an operator named it; a link found while walking
// is not, since following those invites a loop and nothing needs them.
// A generator that emits one gets no error and no documents, so the rule is worth
// stating rather than leaving to whichever walk an implementation reaches for.
func TestAllowlistSymlinkedSubdirectory(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		// A directory of documents outside the allowlist, linked into it.
		outside := t.TempDir()
		writeDoc(t, outside, "b.graphql", rejectedText)
		if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 {
			t.Errorf("expected only the document beside the link; received %s", body)
		}
		if answer.Skipped.Total != 0 {
			t.Errorf("expected an unwalked link to be no skip; received %s", body)
		}
		// The document behind the link is not on the list.
		if code, _ := post(t, s, docRejected); code != http.StatusForbidden {
			t.Errorf("expected the linked document refused; received %d", code)
		}
	})
}
