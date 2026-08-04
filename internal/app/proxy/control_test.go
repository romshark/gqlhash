package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

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
