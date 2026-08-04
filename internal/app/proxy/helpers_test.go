package proxy

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

func testLogger() zerolog.Logger { return zerolog.New(io.Discard) }

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

// newAllowlist returns an allowlist holding what dir has,
// which is what every test here starts from.
func newAllowlist(t *testing.T, dir string) *allowlist.Allowlist {
	t.Helper()
	list := allowlist.New(sha256.New, gqlhash.Options{})
	if _, err := list.Reload(dir); err != nil {
		t.Fatal(err)
	}
	return list
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
