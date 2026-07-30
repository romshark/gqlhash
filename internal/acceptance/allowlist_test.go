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
