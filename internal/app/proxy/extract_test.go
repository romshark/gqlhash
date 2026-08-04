package proxy

import (
	"errors"
	"strings"
	"testing"
)

// noBatch is -server.max-batch off, inBatch a cap above every batch in these tables,
// so a row says whether batching is on rather than what the cap happens to be.
// TestExtractJSONBatchCap covers the cap itself.
const (
	noBatch = 0
	inBatch = 8
)

func TestExtractJSON(t *testing.T) {
	f := func(t *testing.T, expectErr error, expect []string, body string, maxBatch int) {
		t.Helper()
		spans, err := extractJSON(nil, []byte(body), maxBatch)
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

	f(t, nil, []string{"{f}"}, `{"query":"{f}"}`, noBatch)
	f(t, nil, []string{"{f}"}, `{"query": "{f}"}`, noBatch)
	f(t, nil, []string{`{f(s:\"x\")}`}, `{"query":"{f(s:\"x\")}"}`, noBatch)
	f(t, nil, []string{"{f}"},
		`{"operationName":"Q","query":"{f}","variables":{"a":1}}`, noBatch)

	// A query member of an object other than the request isn't the document.
	f(t, nil, []string{"{f}"}, `{"query":"{f}","variables":{"query":"nope"}}`, noBatch)
	f(t, errNoQuery, nil, `{"variables":{"query":"nope"}}`, noBatch)

	// Only a string is a document.
	f(t, errNoQuery, nil, `{"query":null}`, noBatch)
	f(t, errNoQuery, nil, `{"query":42}`, noBatch)
	f(t, errNoQuery, nil, `{}`, noBatch)

	f(t, errMalformedJSON, nil, `{"query":`, noBatch)
	f(t, errMalformedJSON, nil, `not json`, noBatch)
	f(t, errMalformedJSON, nil, ``, noBatch)

	// A batch is rejected unless it's allowed, then every document is returned.
	f(t, errBatch, nil, `[{"query":"{a}"},{"query":"{b}"}]`, noBatch)
	f(t, nil, []string{"{a}", "{b}"}, `[{"query":"{a}"},{"query":"{b}"}]`, inBatch)
	f(t, nil, []string{"{a}"}, `{"query":"{a}"}`, inBatch)
	f(t, errNoQuery, nil, `[{"variables":{}}]`, inBatch)
}

// TestExtractJSONBatchCap covers -server.max-batch:
// a batch of up to that many documents is read, one past it is refused,
// and a lone request object is one document whatever the cap says.
//
// The scan stops at the document that breaks the cap, so a body carrying tens of
// thousands of them costs the cap and not the body — which is the point of a count
// over -server.max-body's bytes.
func TestExtractJSONBatchCap(t *testing.T) {
	batchOf := func(n int) string {
		elements := make([]string, n)
		for i := range elements {
			elements[i] = `{"query":"{a}"}`
		}
		return "[" + strings.Join(elements, ",") + "]"
	}

	for _, tc := range []struct {
		maxBatch, documents int
		expectErr           error
	}{
		{maxBatch: 0, documents: 1, expectErr: errBatch},
		{maxBatch: 1, documents: 1},
		{maxBatch: 1, documents: 2, expectErr: errBatchTooLarge},
		{maxBatch: 3, documents: 3},
		{maxBatch: 3, documents: 4, expectErr: errBatchTooLarge},
		{maxBatch: 2, documents: 10_000, expectErr: errBatchTooLarge},
	} {
		body := []byte(batchOf(tc.documents))
		spans, err := extractJSON(nil, body, tc.maxBatch)
		if !errors.Is(err, tc.expectErr) {
			t.Errorf("cap %d over %d documents: expected %v; received %v",
				tc.maxBatch, tc.documents, tc.expectErr, err)
			continue
		}
		switch {
		case tc.expectErr == nil && len(spans) != tc.documents:
			t.Errorf("cap %d: expected %d documents; received %d",
				tc.maxBatch, tc.documents, len(spans))
		case errors.Is(err, errBatchTooLarge) && len(spans) != tc.maxBatch+1:
			// One past the cap and no further: the rest of the array is never read.
			t.Errorf("cap %d: expected the scan to stop at %d documents; it read %d",
				tc.maxBatch, tc.maxBatch+1, len(spans))
		}
	}

	// A lone request object is no batch, so the cap doesn't reach it.
	for _, maxBatch := range []int{0, 1, 8} {
		spans, err := extractJSON(nil, []byte(`{"query":"{a}"}`), maxBatch)
		if err != nil || len(spans) != 1 {
			t.Errorf("cap %d: expected one document, no error; received %d, %v",
				maxBatch, len(spans), err)
		}
	}
}

// TestExtractJSONEscapedQueryKey covers a query member whose key carries a JSON
// escape. Read raw, "quer\u0079" is no query member;
// a JSON decoder unescapes the key first and reads it as one.
//
// Where this isn't held to, the bypass is one request:
//
//	{"query":"<allowed>","quer\u0079":"<anything>"}
//
// The proxy would see the first member alone, allow it and forward the body,
// while the API unescapes the second key to query and runs that document —
// never hashed, and any operation the schema exposes.
// So the second is a collision: the request names the document twice and is refused.
func TestExtractJSONEscapedQueryKey(t *testing.T) {
	f := func(t *testing.T, expectErr error, expect []string, body string) {
		t.Helper()
		spans, err := extractJSON(nil, []byte(body), noBatch)
		if !errors.Is(err, expectErr) {
			t.Fatalf("expected err %v; received %v; body: %s", expectErr, err, body)
		}
		if err != nil {
			return
		}
		if len(spans) != len(expect) {
			t.Fatalf("expected %d documents; received %d; body: %s",
				len(expect), len(spans), body)
		}
		for i, s := range spans {
			if got := body[s.start:s.end]; got != expect[i] {
				t.Errorf("document %d: expected %q; received %q", i, expect[i], got)
			}
		}
	}

	// The escaped spelling on its own is the document of the request.
	// The escape unescapes to another case of the name, \u0051uery, the same way:
	// isQueryKey takes every case of the plain spelling.
	f(t, nil, []string{"{a}"}, `{"quer\u0079":"{a}"}`)
	f(t, nil, []string{"{a}"}, `{"\u0051uery":"{a}"}`)
	f(t, nil, []string{"{a}"}, `{"qu\u0065ry":"{a}"}`)

	// Beside the plain spelling it's the same member named twice.
	f(t, errQueryCollision, nil, `{"query":"{a}","quer\u0079":"{b}"}`)
	f(t, errQueryCollision, nil, `{"quer\u0079":"{b}","query":"{a}"}`)
	f(t, errQueryCollision, nil, `{"query":"{a}","\u0051uery":"{b}"}`)
	f(t, errQueryCollision, nil, `{"quer\u0079":"{a}","\u0051uery":"{b}"}`)

	// A key that only holds an escape elsewhere is still not the query member.
	f(t, nil, []string{"{a}"}, `{"query":"{a}","var\u0079ables":{"query":"nope"}}`)
	// Nor is one that unescapes to something longer or shorter.
	f(t, errNoQuery, nil, `{"quer\u0079\u0079":"{a}"}`)
	// A broken escape never reaches the comparison: it's no JSON string,
	// so the scanner refuses the body before a key is read out of it.
	f(t, errMalformedJSON, nil, `{"quer\u007":"{a}"}`)
	f(t, errMalformedJSON, nil, `{"quer\q":"{a}"}`)
}

// TestExtractJSONQueryCollision covers the rule the escape above runs into:
// a request object names the document once. Which of two members an API runs is
// the decoder's business, so the request is refused rather than one of them
// picked and a body forwarded that an API may read the other way round.
func TestExtractJSONQueryCollision(t *testing.T) {
	f := func(t *testing.T, expectErr error, body string, maxBatch int) {
		t.Helper()
		if _, err := extractJSON(nil, []byte(body), maxBatch); !errors.Is(err, expectErr) {
			t.Errorf("expected err %v; received %v; body: %s", expectErr, err, body)
		}
	}

	// The plain spelling twice, whatever the two carry.
	f(t, errQueryCollision, `{"query":"{a}","query":"{b}"}`, noBatch)
	f(t, errQueryCollision, `{"query":"{a}","query":"{a}"}`, noBatch)
	f(t, errQueryCollision, `{"query":"{a}","queRY":"{b}"}`, noBatch)
	f(t, errQueryCollision, `{"query":"{a}","query":"{b}","query":"{c}"}`, noBatch)

	// The name is what collides, not the document: a member this reads nothing
	// from is one an API may still run something out of.
	f(t, errQueryCollision, `{"query":"{a}","query":null}`, noBatch)
	f(t, errQueryCollision, `{"query":null,"query":"{a}"}`, noBatch)
	f(t, errQueryCollision, `{"query":"{a}","query":{"x":1}}`, noBatch)
	f(t, errQueryCollision, `{"query":"{a}","query":["x"]}`, noBatch)

	// A member of another object is another request's, or no request's at all.
	f(t, nil, `{"query":"{a}","variables":{"query":"nope"}}`, noBatch)
	f(t, nil, `[{"query":"{a}"},{"query":"{b}"}]`, inBatch)
	f(t, errQueryCollision, `[{"query":"{a}","query":"{b}"}]`, inBatch)
	f(t, errQueryCollision, `[{"query":"{a}"},{"query":"{b}","queRY":"{c}"}]`, inBatch)
}

// TestExtractQueryParamEncodedName is the GET half of TestExtractJSONEscapedQueryKey.
// extractQueryParam compares the raw parameter name, so quer%79 is no query to it,
// while [net/url.ParseQuery] decodes the name first and puts both under query.
func TestExtractQueryParamEncodedName(t *testing.T) {
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

	// The encoded name on its own is the query parameter.
	f(t, nil, "{f}", "quer%79={f}")
	f(t, nil, "{f}", "%71uery={f}")

	// Beside the plain name it's the second query parameter,
	// which is the case extractQueryParam answers rather than choosing between.
	f(t, errDuplicateQuery, "", "query={a}&quer%79={b}")
	f(t, errDuplicateQuery, "", "quer%79={b}&query={a}")
	f(t, errDuplicateQuery, "", "query={a};quer%79={b}")

	// A parameter that merely ends in the name is still not the query.
	f(t, errNoQuery, "", "not%71uery={f}")
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
		s, err := extractJSON(spans[:0], body, noBatch)
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

// TestMergeQuery covers the query a forwarded request carries:
// the endpoint's parameters lead and the client's follow,
// and the pair is joined only where both have one.
func TestMergeQuery(t *testing.T) {
	for _, tc := range []struct{ upstream, client, expect string }{
		// The common deployment: a plain endpoint, so the client's query is it.
		{"", "query=%7Ba%7D", "query=%7Ba%7D"},
		{"", "", ""},
		// A POST names no query of its own, so the endpoint's arrives alone.
		{"env=staging", "", "env=staging"},
		{"env=staging&key=abc", "query=%7Ba%7D", "env=staging&key=abc&query=%7Ba%7D"},
	} {
		if got := mergeQuery(tc.upstream, tc.client); got != tc.expect {
			t.Errorf("mergeQuery(%q, %q) = %q; expected %q",
				tc.upstream, tc.client, got, tc.expect)
		}
	}

	// The endpoint carrying no query of its own is every deployment that names a
	// plain URL, and it's the forwarding path, so it allocates nothing.
	client := "query=" + strings.Repeat("x", 32)
	if n := testing.AllocsPerRun(100, func() {
		if mergeQuery("", client) == "" {
			t.Fatal("merging failed")
		}
	}); n != 0 {
		t.Errorf("expected no allocations for a plain endpoint; received %v", n)
	}
	// One endpoint parameter costs the one string it takes to join them.
	if n := testing.AllocsPerRun(100, func() {
		if mergeQuery("env=staging", client) == "" {
			t.Fatal("merging failed")
		}
	}); n > 1 {
		t.Errorf("expected one allocation at most; received %v", n)
	}
}
