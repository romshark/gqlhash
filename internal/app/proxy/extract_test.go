package proxy

import (
	"errors"
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	f := func(t *testing.T, expectErr error, expect []string, body string, batch bool) {
		t.Helper()
		spans, err := extractJSON(nil, []byte(body), batch)
		if !errors.Is(err, expectErr) {
			t.Fatalf("expected err %v; received: %v; body: %s", expectErr, err, body)
		}
		if err != nil {
			return
		}
		if len(spans) != len(expect) {
			t.Fatalf("expected %d documents; received %d", len(expect), len(spans))
		}
		for i, s := range spans {
			if got := body[s.start:s.end]; got != expect[i] {
				t.Errorf("document %d: expected %q; received %q", i, expect[i], got)
			}
		}
	}

	f(t, nil, []string{"{f}"}, `{"query":"{f}"}`, false)
	f(t, nil, []string{"{f}"}, `{"query": "{f}"}`, false)
	f(t, nil, []string{`{f(s:\"x\")}`}, `{"query":"{f(s:\"x\")}"}`, false)
	f(t, nil, []string{"{f}"},
		`{"operationName":"Q","query":"{f}","variables":{"a":1}}`, false)

	// A query member of an object other than the request isn't the document.
	f(t, nil, []string{"{f}"}, `{"query":"{f}","variables":{"query":"nope"}}`, false)
	f(t, errNoQuery, nil, `{"variables":{"query":"nope"}}`, false)

	// Only a string is a document.
	f(t, errNoQuery, nil, `{"query":null}`, false)
	f(t, errNoQuery, nil, `{"query":42}`, false)
	f(t, errNoQuery, nil, `{}`, false)

	f(t, errMalformedJSON, nil, `{"query":`, false)
	f(t, errMalformedJSON, nil, `not json`, false)
	f(t, errMalformedJSON, nil, ``, false)

	// A batch is rejected unless it's allowed, then every document is returned.
	f(t, errBatch, nil, `[{"query":"{a}"},{"query":"{b}"}]`, false)
	f(t, nil, []string{"{a}", "{b}"}, `[{"query":"{a}"},{"query":"{b}"}]`, true)
	f(t, nil, []string{"{a}"}, `{"query":"{a}"}`, true)
	f(t, errNoQuery, nil, `[{"variables":{}}]`, true)
}

func TestUnescapeJSON(t *testing.T) {
	f := func(t *testing.T, expect, in string, expectShared bool) {
		t.Helper()
		src := []byte(in)
		value, _, err := unescapeJSON(make([]byte, 0, 64), src)
		if err != nil {
			t.Fatalf("unexpected error: %v; input: %q", err, in)
		}
		if string(value) != expect {
			t.Errorf("expected %q; received %q", expect, value)
		}
		// A value without escapes must be the input itself, not a copy.
		shared := len(value) > 0 && len(src) > 0 && &value[0] == &src[0]
		if shared != expectShared {
			t.Errorf("expected shared=%t; received %t", expectShared, shared)
		}
	}

	f(t, "{f}", "{f}", true)
	f(t, "", "", false)
	f(t, "{f(s:\"x\")}", `{f(s:\"x\")}`, false)
	f(t, "a\nb", `a\nb`, false)
	f(t, "a\tb\rc\bd\fe", `a\tb\rc\bd\fe`, false)
	f(t, `a\b`, `a\\b`, false)
	f(t, "a/b", `a\/b`, false)
	f(t, "A", `\u0041`, false)
	f(t, "é", `\u00e9`, false)
	f(t, "💩", `\ud83d\udca9`, false)
	f(t, "x💩y", `x\ud83d\udca9y`, false)
	// A lone surrogate is replaced, which is what encoding/json does.
	f(t, "\uFFFD", `\udca9`, false)

	fErr := func(t *testing.T, in string) {
		t.Helper()
		if _, _, err := unescapeJSON(nil, []byte(in)); err == nil {
			t.Errorf("expected an error; input: %q", in)
		}
	}
	fErr(t, `\`)
	fErr(t, `\q`)
	fErr(t, `\u`)
	fErr(t, `\u00`)
	fErr(t, `\uZZZZ`)
	fErr(t, `\ud83d`)       // A leading surrogate without its pair.
	fErr(t, `\ud83dx`)      // Followed by something other than an escape.
	fErr(t, `\ud83d\u0041`) // Followed by a non-trailing surrogate.
}

func TestExtractQueryParam(t *testing.T) {
	f := func(t *testing.T, expectErr error, expect, rawQuery string) {
		t.Helper()
		value, _, err := extractQueryParam(make([]byte, 0, 64), rawQuery)
		if !errors.Is(err, expectErr) {
			t.Fatalf("expected err %v; received %v; query: %q", expectErr, err, rawQuery)
		}
		if err == nil && string(value) != expect {
			t.Errorf("expected %q; received %q", expect, value)
		}
	}

	f(t, nil, "{f}", "query={f}")
	f(t, nil, "{f}", "operationName=Q&query={f}&variables={}")
	f(t, nil, "{ f }", "query=%7B+f+%7D")
	f(t, nil, "{f}", "query=%7Bf%7D")
	f(t, nil, "", "query=")
	f(t, errNoQuery, "", "")
	f(t, errNoQuery, "", "operationName=Q")
	// A parameter whose name merely ends in query is not the query.
	f(t, errNoQuery, "", "notquery={f}")
	f(t, errInvalidEscape, "", "query=%7")
	f(t, errInvalidEscape, "", "query=%zz")
}

// TestExtractZeroCopy asserts that the paths without escapes hand back a view of
// the input and allocate nothing.
func TestExtractZeroCopy(t *testing.T) {
	body := []byte(
		`{"operationName":"Q","query":"{ user(id: 1) { name } }","variables":{}}`,
	)
	spans := make([]span, 0, 4)
	scratch := make([]byte, 0, 1024)

	if n := testing.AllocsPerRun(100, func() {
		s, err := extractJSON(spans[:0], body, false)
		if err != nil || len(s) != 1 {
			t.Fatal("extraction failed")
		}
		value, _, err := unescapeJSON(scratch, body[s[0].start:s[0].end])
		if err != nil || len(value) == 0 {
			t.Fatal("unescaping failed")
		}
	}); n != 0 {
		t.Errorf("expected no allocations; received %v", n)
	}

	raw := "query=" + strings.Repeat("x", 32)
	if n := testing.AllocsPerRun(100, func() {
		if _, _, err := extractQueryParam(scratch, raw); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("GET: expected no allocations; received %v", n)
	}
}
