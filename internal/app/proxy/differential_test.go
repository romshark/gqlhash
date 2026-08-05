package proxy

import (
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzExtractQueryParamAgainstNetURL holds the private query parser against the
// standard one. A document the proxy reads differently from the API behind it
// is the failure that matters, so the divergences are named here rather than
// left to be discovered.
//
// Where the two disagree, this asserts which disagreement it is.
func FuzzExtractQueryParamAgainstNetURL(f *testing.F) {
	for _, seed := range []string{
		"", "query", "query=", "query={x}", "query=%7Bx%7D", "query=a+b",
		"a=1&query={x}", "a=1;query={x}", "query={x};a=1",
		"query={a}&query={b}", "query={a};query={b}",
		"query=%ZZ", "query=%", "query=%7", "%ZZ=1&query={x}",
		"query={x}&", "&&query={x}", "=query={x}", "query==x",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, rawQuery string) {
		got, _, err := extractQueryParam(nil, rawQuery)
		if err != nil {
			// Refusing is safe whatever the reason: nothing is forwarded, so
			// nothing runs that this didn't read. Which refusals are wanted is
			// pinned by TestExtractQueryParamDivergences, where over-refusal is
			// the risk and a fuzzer would only guess at it.
			return
		}

		// Answering is the direction that can go wrong: the document read here
		// is the one checked against the allowlist, and an API that reads a
		// different one runs something this never saw.
		values, _ := url.ParseQuery(rawQuery)
		if std := values.Get("query"); string(got) != std {
			// ';' ends a pair here and doesn't for net/url, which drops such a
			// pair whole. Reading it is the wider of the two, so what's answered
			// here is a document some API would run, which is the safe half.
			if !strings.Contains(rawQuery, ";") {
				t.Errorf("%q: proxy read %q, net/url read %q", rawQuery, got, std)
			}
		}
	})
}

// TestExtractQueryParamDivergences names what the private parser refuses that
// the standard one takes. Each is a request answered rather than forwarded,
// so the cost of being wrong here is a client turned away, not a document run
// unchecked, which is why these are stated rather than fuzzed.
func TestExtractQueryParamDivergences(t *testing.T) {
	f := func(t *testing.T, expect error, rawQuery string) {
		t.Helper()
		if _, _, err := extractQueryParam(nil, rawQuery); !errors.Is(err, expect) {
			t.Errorf("%q: expected %v; received %v", rawQuery, expect, err)
		}
	}

	// Two of them, and no way to know which one an API would run.
	f(t, errDuplicateQuery, "query={a}&query={b}")
	f(t, errDuplicateQuery, "query={a};query={b}")
	// A key with no '=' is that key with an empty value, so it counts as one.
	f(t, errDuplicateQuery, "query&query={b}")
	f(t, errDuplicateQuery, "query=%&query")

	// net/url drops the pair with the broken escape and carries on with the
	// rest. This refuses the request, which forwards nothing either way.
	f(t, errInvalidEscape, "query=%ZZ")
	f(t, errInvalidEscape, "query=%")

	// A query nothing names.
	f(t, errNoQuery, "")
	f(t, errNoQuery, "a=1")
	// ';' ends a pair here, so this names one where net/url names none.
	f(t, nil, "a=1;query={x}")

	// The plain ones agree with net/url, which the fuzz target holds them to.
	f(t, nil, "query={x}")
	f(t, nil, "query=%7Bx%7D")
	f(t, nil, "query")
}

// FuzzUnescapeJSONAgainstEncodingJSON holds the private string unescaper against
// the standard one. Both read the contents of a JSON string;
// what they make of a half of a surrogate pair on its own is where they part.
func FuzzUnescapeJSONAgainstEncodingJSON(f *testing.F) {
	for _, seed := range []string{
		"", "x", `\n`, `\t`, `\"`, `\\`, `\/`, `A`, `é`,
		`💩`, `\udca9`, `\ud83d`, `\ud83dx`, `\ud83dA`,
		`{ user { name } }`, `aAb`, `\u`, `\u00`, `\x`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		// The caller hands this the bytes between the quotes of a string the
		// JSON scanner already found, so a raw quote or control byte can't
		// arrive: either would have ended the string before this saw it.
		// Feeding one anyway tests a call that can't happen.
		for i := 0; i < len(body); i++ {
			if body[i] == '\\' {
				i++
				continue
			}
			if body[i] == '"' || body[i] < 0x20 {
				return
			}
		}

		// The input is the contents of a JSON string, so it's quoted for the
		// standard parser and handed raw to this one.
		var want string
		stdErr := json.Unmarshal([]byte(`"`+body+`"`), &want)

		got, _, err := unescapeJSON(nil, []byte(body))
		if err != nil {
			// Refusing what the standard parser takes is allowed: nothing is
			// forwarded. Taking what it refuses is not, and is checked below.
			return
		}
		if stdErr != nil {
			// The proxy read a string out of something encoding/json won't.
			// A lone leading surrogate is the known one: encoding/json replaces
			// it, the proxy refuses it, so this direction should stay empty.
			t.Errorf("%q: proxy read %q, encoding/json refused it: %v",
				body, got, stdErr)
			return
		}
		if string(got) == want {
			return
		}
		// encoding/json replaces what isn't valid UTF-8, and a half of a
		// surrogate pair with it. This copies the bytes through, so the two
		// differ exactly there. Both are checking the same document either way:
		// a request reaches the API only by matching an allowlisted file byte
		// for byte, so what this reads is what that file holds.
		if utf8.ValidString(body) && !strings.Contains(body, `\u`) {
			t.Errorf("%q: proxy read %q, encoding/json read %q", body, got, want)
		}
	})
}

// FuzzExtractJSONAgainstEncodingJSON holds the private body reader against the
// standard one over the question a duplicate key asks: encoding/json unescapes
// a key before it matches it and keeps the last "query" member, so the proxy
// refuses a request that names the member twice and reads the one document out
// of the rest.
//
// What's under test is that the document an API would run is among the ones
// checked here. Checking more costs a request that could have been forwarded;
// checking fewer runs a document nobody read. A refused request runs nothing,
// which is why the escapes below are only followed where one is read.
func FuzzExtractJSONAgainstEncodingJSON(f *testing.F) {
	for _, seed := range []string{
		`{"query":"{a}"}`,
		`{"query":"{a}","query":"{b}"}`,
		`{"query":"{a}","query":"{b}","query":"{c}"}`,
		`{"operationName":"Q","query":"{a}","variables":{"x":1}}`,
		`{"query":"A"}`, `{"query":""}`, `{"query":123}`, `{}`, `[]`,
		`{"a":{"query":"{nested}"}}`, `{"query":"{a}","a":{"query":"{n}"}}`,
		`[{"query":"{a}"}]`, `not json`,
		`[{"query":"{a}"}, {"query":"{a}"}]`,
		`[[]]`,
		`[{"query":"{a}"},{"a":1}]`,
		`[{"query":"{a}","query":"{b}"}]`,
		`[{"query":"{a}"},{"query":"{b}"}]`, `{"query":"💩"}`,
		`{"quer\u0079":"{a}"}`, `{"\u0051uery":"{a}"}`,
		`{"query":"{a}","quer\u0079":"{b}"}`,
		`{"quer\u0079":"{b}","query":"{a}"}`,
		`{"query":"{a}","QUERY":"{b}"}`, `{"query":"{a}","query":null}`,
		`[{"query":"{a}","quer\u0079":"{b}"}]`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		// checked is every document the proxy would hash for this body, or nil
		// where it refuses the request and hashes nothing.
		checked := func(maxBatch int) ([]string, bool) {
			spans, err := extractJSON(nil, []byte(body), maxBatch)
			if err != nil {
				return nil, false
			}
			documents := make([]string, 0, len(spans))
			for _, s := range spans {
				value, _, err := unescapeJSON(nil, []byte(body)[s.start:s.end])
				if err != nil {
					return nil, false
				}
				documents = append(documents, string(value))
			}
			return documents, true
		}

		// runs reports what an API reading with encoding/json would execute.
		runs := func(documents []string, want *string) {
			t.Helper()
			if want == nil || slices.Contains(documents, *want) {
				return
			}
			// The escapes the two read differently, as
			// FuzzUnescapeJSONAgainstEncodingJSON names them.
			if utf8.ValidString(body) && !strings.Contains(body, `\u`) {
				t.Errorf("%q: encoding/json runs %q, proxy checked %q",
					body, *want, documents)
			}
		}

		// One request object.
		if documents, ok := checked(noBatch); ok {
			var request struct {
				Query *string `json:"query"`
			}
			if json.Unmarshal([]byte(body), &request) == nil {
				runs(documents, request.Query)
			}
		}

		// A batch of them, where -server.max-batch takes an array of up to that many and
		// every document of it has to be allowed. The cap is above what a seed carries,
		// so what's compared is the reading and not the limit.
		if documents, ok := checked(inBatch); ok {
			var requests []struct {
				Query *string `json:"query"`
			}
			if json.Unmarshal([]byte(body), &requests) == nil {
				for _, request := range requests {
					runs(documents, request.Query)
				}
			}
		}
	})
}
