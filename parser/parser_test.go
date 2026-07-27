package parser_test

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/internal"
	"github.com/romshark/gqlhash/parser"
)

// bom is the UTF-8 encoding of U+FEFF, which is Ignored
// (https://spec.graphql.org/September2025/#UnicodeBOM).
const bom = "\xef\xbb\xbf"

// Supplementary-plane (astral) characters (https://www.unicode.org/roadmaps/smp),
// each a 4-byte UTF-8 sequence:
// U+1F4A9 PILE OF POO and U+1D11E MUSICAL SYMBOL G CLEF.
// Used to exercise the full Unicode SourceCharacter range
// (https://spec.graphql.org/September2025/#SourceCharacter).
var astral1, astral2 = string(rune(0x1F4A9)), string(rune(0x1D11E))

// Byte sequences that aren't valid UTF-8 encodings of a Unicode scalar value.
// SourceCharacter admits only scalar values, so none of these may appear in a
// document (https://spec.graphql.org/September2025/#SourceCharacter).
var badUTF8 = map[string]string{
	"lone continuation byte":    "\x80",
	"invalid byte 0xFF":         "\xff",
	"truncated 2-byte sequence": "\xc3",
	"truncated 3-byte sequence": "\xe2\x82",
	"truncated 4-byte sequence": "\xf0\x9f\x92",
	"overlong encoding of '/'":  "\xc0\xaf",
	"surrogate U+D800":          "\xed\xa0\x80",
	"surrogate U+DFFF":          "\xed\xbf\xbf",
	"above U+10FFFF":            "\xf4\x90\x80\x80",
}

// recorder is an [io.Writer] that keeps the canonical form instead of hashing
// it.
type recorder struct{ stream []byte }

func (r *recorder) Write(b []byte) (int, error) {
	r.stream = append(r.stream, b...)
	return len(b), nil
}

func (r *recorder) String() string { return string(r.stream) }

var _ io.Writer = new(recorder)

// parse reads input and returns its canonical form and the error.
func parse(o parser.Options, input string) (string, parser.Error) {
	r := new(recorder)
	err := parser.Parse(r, o, input)
	return r.String(), err
}

// stream builds a canonical form from prefix bytes and text.
func stream(parts ...any) string {
	var b []byte
	for _, p := range parts {
		switch v := p.(type) {
		case byte:
			b = append(b, v)
		case string:
			b = append(b, v...)
		default:
			panic(fmt.Sprintf("unsupported stream part: %T", p))
		}
	}
	return string(b)
}

// hash returns the SHA-1 sum of the canonical form of input.
// Fails the test if input is invalid.
func hash(t *testing.T, options parser.Options, input string) string {
	t.Helper()
	h := sha1.New()
	if err := parser.Parse(h, options, input); err.Err != nil {
		t.Fatalf("Parse(%q): %v", input, err)
	}
	return string(h.Sum(nil))
}

func TestParse(t *testing.T) {
	f := func(t *testing.T, expectErr error, input string) {
		t.Helper()
		_, err := parse(parser.Options{}, input)
		if expectErr != err.Err {
			t.Errorf("expected err: %v; received err: %v; input: %q",
				expectErr, err, input)
		}
	}

	f(t, parser.ErrUnexpectedEOF, "")
	f(t, parser.ErrUnexpectedEOF, " \r\n\t, ")
	f(t, parser.ErrUnexpectedToken, "foo")
	f(t, nil, `
		mutation AddUser ( $ name : String! ) {
			addUser ( name: $ name ) {
				id
			}
		}
		mutation _changeUser (
			$ email : String !
			$ __Nickname : String = null
			$ roles : [ String ! ] !
		) {
			changeUser (
				email: $ email
				__Nickname: $ __Nickname
			) @important {
				email
				nickname
				roles {
					title
					description @ translated ( prio: [ DE EN FR ] )
				}
			}
		}
		fragment UserInfo on User { name email }
	`)

	{ // Keyword boundaries (https://spec.graphql.org/September2025/#Name).
		// Reject a keyword that's only the start of a longer name.
		f(t, parser.ErrUnexpectedToken, "queryFoo { x }")
		f(t, parser.ErrUnexpectedToken, "mutationFoo { x }")
		f(t, parser.ErrUnexpectedToken, "subscriptionFoo { x }")
		f(t, parser.ErrUnexpectedToken, "fragmentFoo on T { x }")
		f(t, parser.ErrUnexpectedToken, "fragment F onType { x }")

		// Reject it for digit and underscore continuations too.
		f(t, parser.ErrUnexpectedToken, "query1 { x }")
		f(t, parser.ErrUnexpectedToken, "query_ { x }")
		f(t, parser.ErrUnexpectedToken, "fragment F on_T { x }")

		// Accept a keyword ended by an ignorable or by punctuation.
		f(t, nil, "query Foo { x }")
		f(t, nil, "query,Foo { x }")
		f(t, nil, "query\nFoo { x }")
		f(t, nil, "query#comment\nFoo { x }")
		f(t, nil, "query{ x }")
		f(t, nil, "mutation M { x }")
		f(t, nil, "subscription S { x }")
		f(t, nil, "fragment F on T { x }")
		f(t, nil, "fragment F#comment\non#comment\nT { x }")
		f(t, nil, "fragment F on T{ x }")

		// Accept a name that starts with a keyword.
		f(t, nil, "query queryFoo { x }")
		f(t, nil, "fragment fragmentFoo on onType { x }")

		// Accept a fragment spread whose name starts with `on`.
		f(t, nil, "{ ... onType }")
		f(t, nil, "{ ... on_T }")
		f(t, nil, "{ ... on1 }")

		// Accept a type condition whose `on` is ended by any ignorable.
		f(t, nil, "{ ... on Bar { x } }")
		f(t, nil, "{ ... on,Bar { x } }")
		f(t, nil, "{ ... on#comment\nBar { x } }")

		// Reject a bare `on`, which can't name a spread and has no type after it.
		f(t, parser.ErrUnexpectedToken, "{ ... on }")
		f(t, parser.ErrUnexpectedToken, "{ ... on{ x } }")
	}

	{ // Ignored tokens
		// (https://spec.graphql.org/September2025/#sec-Language.Source-Text.Ignored-Tokens).
		f(t, nil, "{x}")
		f(t, nil, ",{,x,},")
		f(t, nil, " \t\r\n{ \t\r\nx \t\r\n} \t\r\n")
		f(t, nil, "# comment\n{x}")
		f(t, nil, "{x}# comment")
		f(t, nil, "{x}# comment\n")
		// A bare CR ends a comment just like a LF does.
		f(t, nil, "# comment\r{x}")
		f(t, nil, "{#\n#\n#\nx#\n}")
		// A comment runs to the end of the line, so it swallows a whole document.
		f(t, parser.ErrUnexpectedEOF, "# {x}")

		// A UTF-8 BOM is Ignored and may appear before or after any token.
		f(t, nil, bom+"{ x }")
		f(t, nil, "{ x }"+bom)
		f(t, nil, "{ "+bom+"x }")
		f(t, nil, "{ x"+bom+"}")
		f(t, nil, bom+"query Foo { x }")
		f(t, nil, "query"+bom+"Foo { x }")
		f(t, nil, "fragment F on"+bom+"T { x }")
		f(t, nil, "{ f(a:"+bom+"1) }")
		f(t, nil, bom+bom+"{x}")
		f(t, nil, "{x"+bom+"# comment\n"+bom+"}")

		// A BOM doesn't glue two names together.
		f(t, nil, "query"+bom+"Foo"+bom+"{ x }")

		// A BOM starts with 0xEF, but 0xEF alone is no BOM.
		f(t, parser.ErrUnexpectedToken, "\xef{x}")
		f(t, parser.ErrUnexpectedToken, "\xef\xbb{x}")
	}

	{ // Constants (https://spec.graphql.org/September2025/#VariableDefinition).
		// A default value and the directives of a variable definition take
		// Value[Const], which excludes variable usages, however deeply nested.
		f(t, parser.ErrUnexpectedVariable, `query Q($x: Int = $y) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: [Int] = [$y]) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: [[Int]] = [[1, $y]]) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: In = {a: $y}) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: In = {a: {b: [$y]}}) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: Int @d(a: $y)) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: Int @d(a: [$y])) { f }`)
		f(t, parser.ErrUnexpectedVariable, `query Q($x: Int = 1 @d(a: {b: $y})) { f }`)

		// Constants stay valid there.
		f(t, nil, `query Q($x: Int = 1) { f }`)
		f(t, nil, `query Q($x: [Int] = [1, 2]) { f }`)
		f(t, nil, `query Q($x: In = {a: [1], b: "s"}) { f }`)
		f(t, nil, `query Q($x: Int = null @d(a: [ENUM])) { f }`)

		// Everywhere else a variable is just another value.
		f(t, nil, `query Q($x: Int) { f(a: $x) }`)
		f(t, nil, `query Q($x: Int) { f(a: [$x, {b: $x}]) }`)
		f(t, nil, `query Q($x: Int) { f @d(a: $x) }`)
		f(t, nil, `query Q($x: Int) @d(a: $x) { f }`)
		f(t, nil, `fragment F on T @d(a: $x) { f } { ...F }`)
		f(t, nil, `{ ... @d(a: $x) { f } }`)
		f(t, nil, `{ ...F @d(a: $x) }`)

		// The constant rule ends with the variable definitions: the directives of
		// the operation itself take a Value again.
		f(t, nil, `query Q($x: Int = 1) @d(a: $x) { f }`)
	}

	{ // Control characters (https://spec.graphql.org/September2025/#SourceCharacter).
		// A BlockStringCharacter is any SourceCharacter, so a block string may
		// hold control scalar values.
		f(t, nil, "{ f(s: \"\"\"a\x00b\"\"\") }")
		f(t, nil, "{ f(s: \"\"\"a\x07b\"\"\") }")
		f(t, nil, "{ f(s: \"\"\"\x01\x0b\x0c\x1f\"\"\") }")
		f(t, nil, "{ f(s: \"\"\"a\x00b\nc\x1fd\"\"\") }")

		// A normal string keeps rejecting them, they must be escaped there.
		f(t, parser.ErrUnescapedControlChar, "{ f(s: \"a\x00b\") }")
		f(t, parser.ErrUnescapedControlChar, "{ f(s: \"a\x07b\") }")
		f(t, parser.ErrUnescapedControlChar, "{ f(s: \"a\x1fb\") }")
		f(t, nil, `{ f(s: "a\u0000b") }`)

		// A LineTerminator is a control character too, so it needs an escape
		// (https://spec.graphql.org/September2025/#StringCharacter).
		f(t, parser.ErrUnescapedControlChar, "{ f(s: \"a\nb\") }")
		f(t, parser.ErrUnescapedControlChar, "{ f(s: \"a\rb\") }")

		// A control character isn't Ignored anywhere else either.
		f(t, parser.ErrUnexpectedToken, "{\x00x}")
	}

	{ // Descriptions (https://spec.graphql.org/September2025/#sec-Descriptions).
		// spec of September 2025 allows a description on an operation,
		// a fragment and a variable definition.
		f(t, nil, `"Operation description" query Q { f }`)
		f(t, nil, "\"Operation description\"\nquery Q { f }")
		f(t, nil, `"""Block description""" query Q { f }`)
		f(t, nil, `"Anonymous operation" query { f }`)
		f(t, nil, `"Mutation" mutation M { f }`)
		f(t, nil, `"Subscription" subscription S { f }`)
		f(t, nil, `"Fragment description" fragment F on T { f } query Q { ...F }`)
		f(t, nil, "query Q(\n\t\"Variable description\"\n\t$x: Int\n) { f }")
		f(t, nil, `query Q("A" $x: Int, "B" $y: Int = 1 @d) { f }`)
		f(t, nil, `query Q("""Block""" $x: Int) { f }`)
		f(t, nil, `"A" query Q { f } "B" mutation M { g }`)

		// Query shorthand takes no description.
		f(t, parser.ErrUnexpectedToken, `"Description" { f }`)
		// Only one description, and only a string.
		f(t, parser.ErrUnexpectedToken, `"A" "B" query Q { f }`)
		f(t, parser.ErrUnexpectedToken, `1 query Q { f }`)
		f(t, parser.ErrUnexpectedToken, `query Q("A" "B" $x: Int) { f }`)
		// A description is still a string value and must be valid.
		f(t, parser.ErrInvalidEscape, `"\q" query Q { f }`)
		f(t, parser.ErrInvalidEscape, `query Q("\q" $x: Int) { f }`)
		f(t, parser.ErrUnexpectedEOF, `"Description"`)
		f(t, parser.ErrUnexpectedEOF, `query Q("A"`)
		f(t, parser.ErrUnexpectedEOF, `"""Description"""`)
	}

	{ // Numeric literals (https://spec.graphql.org/September2025/#sec-Int-Value).
		// A broken number is one invalid number and must not be split into
		// several values.
		f(t, parser.ErrMalformedNumber, "{ f(a: [01]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [-.1]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [0x123]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [123L]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [1e2foo]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [- foo]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: 007) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [-]) }")
		f(t, parser.ErrMalformedNumber, "{ f(a: [1.]) }")
		f(t, parser.ErrUnexpectedToken, "{ f(a: .5) }")
		f(t, parser.ErrMalformedNumber, "query Q($x: Int = 01) { f }")

		// The same rules apply wherever a value is read.
		f(t, parser.ErrMalformedNumber, "{ f(a: {x: 01}) }")
		f(t, parser.ErrMalformedNumber, "{ f @d(a: 1e2foo) }")
		f(t, parser.ErrMalformedNumber, "query Q($x: Int @d(a: -.1)) { f }")

		// Values that an Ignored token separates legally are still accepted.
		f(t, nil, "{ f(a: [1 2]) }")
		f(t, nil, "{ f(a: [1, 2]) }")
		f(t, nil, "{ f(a: [1 -2]) }")
		f(t, nil, "{ f(a: [0 1]) }")
		f(t, nil, "{ f(a: [1 foo]) }")
		f(t, nil, "{ f(a: [1.5 2e3 -0]) }")
		f(t, nil, "{ f(a: [1#comment\n2]) }")
		f(t, nil, "{ f(a: [1\n2]) }")
		f(t, nil, "{ f(a: [1"+bom+"2]) }")
		f(t, nil, "{ f(a: {x: 1, y: -2.5e-3}) }")
		f(t, nil, "query Q($x: Int = -0) { f }")

		// Only the integer part rejects leading zeroes.
		f(t, nil, "{ f(a: [1.05 1e05 1e+05 0.0 0e0 -0.05]) }")
	}

	{ // Source text is UTF-8 encoded Unicode scalar values
		// (https://spec.graphql.org/September2025/#SourceCharacter).
		for name, seq := range badUTF8 {
			t.Run(name, func(t *testing.T) {
				// Reject it in a single-line string.
				f(t, parser.ErrMalformedUTF8, `{ f(s: "`+seq+`") }`)
				// Reject it in a block string.
				f(t, parser.ErrMalformedUTF8, `{ f(s: """`+seq+`""") }`)
				// Reject it in a comment, whose CommentChar is a SourceCharacter
				// too (https://spec.graphql.org/September2025/#CommentChar).
				f(t, parser.ErrUnexpectedToken, "# "+seq+"\n{ x }")
				f(t, parser.ErrUnexpectedToken, "{ x } # "+seq)
			})
		}

		// Keep accepting well-formed sequences of every length.
		f(t, nil, `{ f(s: "é € `+astral1+`") }`)
		f(t, nil, `{ f(s: """é € `+astral1+`""") }`)
		f(t, nil, "# é € "+astral1+"\n{ x }")
		f(t, nil, "{ x } # é € "+astral1)
	}

	{ // Selection sets (https://spec.graphql.org/September2025/#sec-Selection-Sets).
		// A selection set holds at least one selection.
		f(t, parser.ErrUnexpectedToken, "{}")
		f(t, parser.ErrUnexpectedToken, "{ }")
		f(t, parser.ErrUnexpectedToken, "{ f {} }")
		f(t, parser.ErrUnexpectedToken, "{ ... on T {} }")
		// Deeply nested selection sets need no stack of their own.
		f(t, nil, strings.Repeat("{f", 200)+strings.Repeat("}", 200))
		// A '.' that begins no '...' is an unexpected token.
		f(t, parser.ErrUnexpectedToken, "{ . }")
		f(t, parser.ErrUnexpectedToken, "{ .. }")
		f(t, parser.ErrUnexpectedToken, "{ ..")
	}

	{ // Values (https://spec.graphql.org/September2025/#Value).
		// Deeply nested lists and input objects grow the value stack.
		f(t, nil, "{f(a:"+strings.Repeat("[", 100)+strings.Repeat("]", 100)+")}")
		f(t, nil, "{f(a:"+strings.Repeat("[", 100)+"1"+strings.Repeat("]", 100)+")}")
		f(t, nil, "{f(a:"+strings.Repeat("{k:", 100)+"1"+strings.Repeat("}", 100)+")}")
		// A list and an input object may nest within one another.
		f(t, nil, "{f(a:[{k:[{k:1}]}])}")
		// A closing bracket must match its opening one.
		f(t, parser.ErrUnexpectedToken, "{f(a:[1})}")
		f(t, parser.ErrUnexpectedToken, "{f(a:{k:1])}")
	}
}

// TestParseCanonicalStream pins the canonical form of every kind of token.
func TestParseCanonicalStream(t *testing.T) {
	f := func(t *testing.T, expect string, inputs ...string) {
		t.Helper()
		for _, input := range inputs {
			actual, err := parse(parser.Options{}, input)
			if err.Err != nil {
				t.Fatalf("unexpected error: %v; input: %q", err, input)
			}
			if actual != expect {
				t.Errorf("expected stream:\n%q\nreceived:\n%q\ninput: %q",
					expect, actual, input)
			}
		}
	}

	f(t, stream(
		parser.HPrefQuery,
		parser.HPrefSelectionSet,
		parser.HPrefField, "foo",
		parser.HPrefSelectionSetEnd,
	), "{foo}", "{ foo }", "query { foo }", "\t{\n\tfoo,\n}\n", "{#c\nfoo}")

	f(t, stream(
		parser.HPrefMutation, "M",
		parser.HPrefSelectionSet,
		parser.HPrefField, "a",
		parser.HPrefFieldAliasedName, "b",
		parser.HPrefSelectionSetEnd,
	), "mutation M{a:b}", "mutation M { a : b }")

	f(t, stream(
		parser.HPrefSubscription, "S",
		parser.HPrefSelectionSet,
		parser.HPrefField, "a",
		parser.HPrefSelectionSet,
		parser.HPrefField, "b",
		parser.HPrefSelectionSetEnd,
		parser.HPrefSelectionSetEnd,
	), "subscription S{a{b}}")

	// A description is documentation and must not appear in the stream.
	f(t, stream(
		parser.HPrefQuery, "Q",
		parser.HPrefSelectionSet,
		parser.HPrefField, "f",
		parser.HPrefSelectionSetEnd,
	), `query Q{f}`, `"Description" query Q{f}`, `"""Block""" query Q{f}`)

	// Variable definitions, types, default values and directives.
	f(t, stream(
		parser.HPrefQuery, "Q",
		parser.HPrefVariableDefinition, "x",
		parser.HPrefType, "[Int!]!",
		parser.HPrefValueInteger, "1",
		parser.HPrefDirective, "dep",
		parser.HPrefArgument, "since",
		parser.HPrefValueString, "v2",
		parser.HPrefSelectionSet,
		parser.HPrefField, "f",
		parser.HPrefSelectionSetEnd,
	), `query Q($x:[Int!]!=1@dep(since:"v2")){f}`,
		"query Q (\n\t\"Doc\"\n\t$x : [ Int ! ] ! = 1 @dep ( since : \"v2\" )\n) { f }")

	// Fragments and inline fragments.
	f(t, stream(
		parser.HPrefFragmentDefinition, "F",
		parser.HPrefType, "T",
		parser.HPrefDirective, "d",
		parser.HPrefSelectionSet,
		parser.HPrefField, "a",
		parser.HPrefSelectionSetEnd,
		parser.HPrefQuery,
		parser.HPrefSelectionSet,
		parser.HPrefFragmentSpread, "F",
		parser.HPrefInlineFragment,
		parser.HPrefType, "T",
		parser.HPrefSelectionSet,
		parser.HPrefField, "b",
		parser.HPrefSelectionSetEnd,
		parser.HPrefInlineFragment,
		parser.HPrefDirective, "skip",
		parser.HPrefSelectionSet,
		parser.HPrefField, "c",
		parser.HPrefSelectionSetEnd,
		parser.HPrefSelectionSetEnd,
	), "fragment F on T@d{a} {...F ...on T{b} ...@skip{c}}")

	// Every kind of value.
	f(t, stream(
		parser.HPrefQuery,
		parser.HPrefSelectionSet,
		parser.HPrefField, "f",
		parser.HPrefArgument, "i", parser.HPrefValueInteger, "-42",
		parser.HPrefArgument, "fl", parser.HPrefValueFloat, "3.14e-2",
		parser.HPrefArgument, "s", parser.HPrefValueString, "text",
		parser.HPrefArgument, "b", parser.HPrefValueString, "block",
		parser.HPrefArgument, "t", parser.HPrefValueTrue,
		parser.HPrefArgument, "f", parser.HPrefValueFalse,
		parser.HPrefArgument, "n", parser.HPrefValueNull,
		parser.HPrefArgument, "e", parser.HPrefValueEnum, "ENUM",
		parser.HPrefArgument, "v", parser.HPrefValueVariable, "var",
		parser.HPrefArgument, "l", parser.HPrefValueList,
		parser.HPrefValueInteger, "1", parser.HPrefValueInteger, "2",
		parser.HPrefValueListEnd,
		parser.HPrefArgument, "o", parser.HPrefValueInputObject,
		parser.HPrefValueInputObjectField, "k", parser.HPrefValueInteger, "1",
		parser.HPrefInputObjectEnd,
		parser.HPrefSelectionSetEnd,
	), `{f(i:-42 fl:3.14e-2 s:"text" b:"""block""" t:true f:false n:null`+
		` e:ENUM v:$var l:[1 2] o:{k:1})}`)

	// An empty list and an empty input object have no items to separate, so
	// neither gets an end marker.
	f(t, stream(
		parser.HPrefQuery,
		parser.HPrefSelectionSet,
		parser.HPrefField, "f",
		parser.HPrefArgument, "l", parser.HPrefValueList,
		parser.HPrefArgument, "o", parser.HPrefValueInputObject,
		parser.HPrefSelectionSetEnd,
	), "{f(l:[] o:{})}", "{f(l:[,] o:{,})}", "{f(l:[ ] o:{ })}")
}

// TestParseStringValue asserts that a string is written as the value it stands
// for and not as it's spelled
// (https://spec.graphql.org/September2025/#sec-String-Value).
// stringValueOf returns the bytes the string value input is hashed as, read as
// the only argument of a minimal document.
func stringValueOf(t *testing.T, input string) string {
	t.Helper()
	s, err := parse(parser.Options{}, `{f(a:`+input+`)}`)
	if err.Err != nil {
		t.Fatalf("unexpected error: %v; input: %q", err, input)
	}
	prefix := stream(
		parser.HPrefQuery, parser.HPrefSelectionSet,
		parser.HPrefField, "f", parser.HPrefArgument, "a",
		parser.HPrefValueString,
	)
	suffix := stream(parser.HPrefSelectionSetEnd)
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
		t.Fatalf("unexpected stream: %q", s)
	}
	return s[len(prefix) : len(s)-len(suffix)]
}

func TestParseStringValue(t *testing.T) {
	f := func(t *testing.T, expect string, inputs ...string) {
		t.Helper()
		for _, input := range inputs {
			if a := stringValueOf(t, input); a != expect {
				t.Errorf("expected value %q; received %q; input: %q", expect, a, input)
			}
		}
	}

	f(t, "", `""`, `""""""`, `"""    """`, "\"\"\"\n \t\n\"\"\"", "\"\"\"\n   \"\"\"")
	f(t, "ok", `"ok"`, `"""ok"""`, "\"\"\"\nok\n\"\"\"", "\"\"\"\n\n  ok\n  \"\"\"")

	{ // EscapedCharacter
		// (https://spec.graphql.org/September2025/#EscapedCharacter).
		f(t, `"`, `"\""`)
		f(t, `/`, `"\/"`)
		f(t, "\t", `"\t"`)
		f(t, "\n", `"\n"`)
		f(t, "\r", `"\r"`)
		// A backslash and the control bytes the hash prefixes are taken from are
		// escaped again, so no string value can imitate a prefix.
		f(t, `\\`, `"\\"`)
		f(t, `\H`, `"\b"`, `"\u0008"`, `"\u{8}"`)
		f(t, `\L`, `"\f"`, `"\u000C"`, `"\u{c}"`)
		f(t, `\@`, `"\u0000"`, `"\u{0}"`)
		f(t, `\A`, `"\u0001"`)
		f(t, `\_`, `"\u001f"`)
	}

	{ // EscapedUnicode (https://spec.graphql.org/September2025/#EscapedUnicode).
		// The fixed-width, the variable-width and the literal spelling of one
		// character all hash alike.
		f(t, "A", `"\u0041"`, `"\u{41}"`, `"\u{0000041}"`, `"A"`)
		f(t, "é", `"\u00e9"`, `"\u00E9"`, `"\u{e9}"`, `"é"`)
		f(t, "こんにちは", `"\u3053\u3093\u306b\u3061\u306f"`, `"こんにちは"`)
		// A supplementary-plane character, as a surrogate pair, as a
		// variable-width escape and as the literal character.
		f(t, astral1, `"\uD83D\uDCA9"`, `"\u{1F4A9}"`, `"\u{1f4a9}"`, `"`+astral1+`"`)
		f(t, astral2, `"\u{1D11E}"`, `"`+astral2+`"`)
		f(t, "\U0010FFFF", `"\u{10FFFF}"`)
	}

	{ // BlockStringValue
		// (https://spec.graphql.org/September2025/#BlockStringValue()).
		// Escape sequences aren't interpreted, only `\"""` is.
		f(t, `\\n`, `"""\n"""`)
		f(t, `"""`, `"""\""""""`)
		// A pair of quotes is ordinary content, and so is a quote that opens no
		// delimiter, even as the first byte of the block.
		f(t, `" x`, `"""" x"""`)
		f(t, `x"" y`, `"""x"" y"""`)
		f(t, `a"""b`, `"""a\"""b"""`)
		// The lines are joined by a single line feed, so LF, CRLF and CR agree.
		f(t, "a\nb", "\"\"\"a\nb\"\"\"", "\"\"\"a\r\nb\"\"\"", "\"\"\"a\rb\"\"\"")
		// The common indentation is stripped from every line but the first.
		f(t, "line one.\n\tline two.\nline three.",
			"\"\"\"line one.\n\t\t\t\t\tline two.\n\t\t\t\tline three.\n\"\"\"")
		f(t, "a\n b", "\"\"\"\n  a\n   b\n  \"\"\"")
		// A blank line between two content lines is kept.
		f(t, "a\n\nb", "\"\"\"a\n\nb\"\"\"", "\"\"\"\n  a\n\n  b\n  \"\"\"")
		// Leading and trailing blank lines are dropped.
		f(t, "foo", "\"\"\"\n\nfoo\n\n\"\"\"", "\"\"\"foo\n\n\t\n  \n\n  \"\"\"")
		f(t, "foo", "\"\"\"foo\r\n\r\n\"\"\"", "\"\"\"\r\nfoo\r\n  \r\n\"\"\"")
		// Trailing WhiteSpace of a content line is content.
		f(t, "foo  ", "\"\"\"foo  \n\n\t\n  \n\n  \"\"\"")
		// A control character stays a BlockStringCharacter but is escaped, so it
		// can't imitate a hash prefix either.
		f(t, `a\@b`, "\"\"\"a\x00b\"\"\"")
		f(t, "a\tb", "\"\"\"a\tb\"\"\"")
		// GraphQL WhiteSpace is only space and tab, so U+00A0 is content.
		f(t, "\u00a0", "\"\"\"\u00a0\"\"\"")
	}
}

// TestParseValueErrors covers the lexical rules of the individual values. Each
// value is read in an argument, which takes a Value, and in a variable
// definition's default value, which takes a Value[Const].
func TestParseValueErrors(t *testing.T) {
	f := func(t *testing.T, expectErr error, values ...string) {
		t.Helper()
		for _, v := range values {
			for _, input := range []string{
				`{f(a:` + v + `)}`,
				`{f(a:[` + v + `])}`,
				`{f(a:{k:` + v + `})}`,
				`{f @d(a:` + v + `)}`,
				`query Q($x:T=` + v + `){f}`,
				`query Q($x:T@d(a:` + v + `)){f}`,
			} {
				_, err := parse(parser.Options{}, input)
				if err.Err != expectErr {
					t.Errorf("expected err: %v; received: %v; input: %q",
						expectErr, err, input)
				}
			}
		}
	}

	{ // IntValue (https://spec.graphql.org/September2025/#sec-Int-Value).
		f(t, nil, "0", "-0", "42", "-42", "1234567890",
			"10000000000000000000000000")
		// A number is either a single 0 or starts with 1-9, so a leading zero
		// is illegal (https://spec.graphql.org/September2025/#IntegerPart).
		f(t, parser.ErrMalformedNumber, "00", "01", "0123", "-01")
		// A NegativeSign must be followed by a Digit.
		f(t, parser.ErrMalformedNumber, "-", "-.1", "-foo", "- 1")
		// No digit, '.' or NameStart may follow a number, so a broken number
		// is one invalid number and not two valid tokens.
		f(t, parser.ErrMalformedNumber, "0x123", "123L", "1foo", "1_")
	}

	{ // FloatValue (https://spec.graphql.org/September2025/#sec-Float-Value).
		f(t, nil, "0.1", "-0.1", "42.123", "-3.14159265359",
			"10000000000000000000000000.0", "0.1e1234567890",
			"0.1e+1234567890", "0.1e-1234567890", "0.1E+1234567890",
			"1e2", "1E2", "1e+2", "1e-2", "1.0e2", "0e0", "1.05", "1e05")
		// A fraction and an exponent both need at least one digit.
		f(t, parser.ErrMalformedNumber, "1.", "1e", "1E", "1e+", "1e-",
			"1.0e", "1.0E-", "1.e2", "1.E2")
		// The same end-of-number rule applies.
		f(t, parser.ErrMalformedNumber, "1e2foo", "1.5x", "1.2.3", "1.5e3.4",
			"1e2_", "01.5", "-01.5e2")
	}

	{ // A Variable is a '$' followed by a Name
		// (https://spec.graphql.org/September2025/#Variable).
		for _, input := range []string{
			`{f(a:$1)}`, `{f(a:$$x)}`, `{f(a:$ )}`, `{f(a:[$!])}`,
		} {
			if _, err := parse(parser.Options{}, input); err.Err !=
				parser.ErrUnexpectedToken {
				t.Errorf("expected %v; received: %v; input: %q",
					parser.ErrUnexpectedToken, err, input)
			}
		}
	}

	{ // NullValue, BooleanValue and EnumValue.
		f(t, nil, "null", "true", "false", "x", "foo", "Bar", "_x", "_0",
			// A name that merely starts with a keyword is an enum value.
			"nullable", "trueStory", "falseFlag")
		f(t, parser.ErrUnexpectedToken, "@", "ж", "ツ", ")", ":", "!")
	}

	{ // Single-line strings
		// (https://spec.graphql.org/September2025/#sec-String-Value).
		f(t, nil, `""`, `"ok"`, `"\""`, `"\\"`, `"\/"`, `"\b"`, `"\f"`, `"\n"`,
			`"\r"`, `"\t"`, `"\uabcd"`, `"\uABCD"`, `"\u{0}"`, `"\u{10FFFF}"`,
			`"one two\t\nthree 123"`, `"ツ ёж ïх жэ こんにちは\n"`)

		// An unknown escape character.
		f(t, parser.ErrInvalidEscape, `"\k"`, `"\q"`, `"\ "`, `"\0"`)
		// A fixed-width escape needs four hexadecimal digits.
		f(t, parser.ErrInvalidEscape, `"\uGGGG"`, `"\u123G"`)
		// StringCharacter :: \u EscapedUnicode asserts the escaped value is a
		// Unicode scalar value, so out-of-range and surrogate escapes are a parse
		// error even though their syntax is valid
		// (https://spec.graphql.org/September2025/#StringCharacter).
		f(t, parser.ErrInvalidEscape, `"\u{110000}"`, `"\u{FFFFFFFF}"`,
			`"\u{D800}"`, `"\u{DFFF}"`)
		// A variable-width escape needs at least one hexadecimal digit and a
		// closing brace.
		f(t, parser.ErrInvalidEscape, `"\u{}"`, `"\u{G}"`, `"\u{1F4A9"`,
			`"\u{41 }"`)

		// The legacy pair production is the only way a surrogate may appear: a
		// leading surrogate must be followed by a trailing one.
		f(t, nil, `"\uD83D\uDCA9"`)
		f(t, parser.ErrInvalidEscape,
			`"\uD800"`,       // Lone leading.
			`"\uDC00"`,       // Lone trailing.
			`"\uD800\uD800"`, // Leading + leading.
			`"\uDC00\uDC00"`, // Trailing + trailing.
			`"\uDC00\uD800"`, // Reversed pair.
			`"\uD800a"`,      // Leading, not paired.
			`"\uD800\n"`,     // Second half is no `\uXXXX` escape.
			`"\uD800\uZZZZ"`, // Second half is no hexadecimal.
		)
	}

	{ // Block strings.
		f(t, nil, `""""""`, `"""ok"""`, `"""\uGGGG"""`,
			`"""\""""""`, "\"\"\"a\nb\"\"\"", `"""`+astral1+`"""`)
		// A `\"""` escapes the closing delimiter, so this block string is
		// unterminated (https://spec.graphql.org/September2025/#BlockStringCharacter).
		f(t, parser.ErrUnexpectedEOF, `"""\\"""`, `"""\""""`)
	}
}

// TestParseValueErrorsEOF covers the values that end with the document, which
// makes them [parser.ErrUnexpectedEOF] instead of a lexical error.
func TestParseValueErrorsEOF(t *testing.T) {
	for _, v := range []string{
		`"`, `"\`, `"\u`, `"\uAB`, `"\uD800`, `"\uD800\`, `"\uD800\u12`,
		`"\u{1F4A9`, `"""`, `"""\`, `"""\u`, `"""a`, `[`, `[1`, `{`, `{k`, `{k:`,
		`{k:1`, `$`,
	} {
		input := `{f(a:` + v
		_, err := parse(parser.Options{}, input)
		if !errors.Is(err.Err, parser.ErrUnexpectedEOF) {
			t.Errorf("expected %v; received: %v; input: %q",
				parser.ErrUnexpectedEOF, err, input)
		}
	}
}

// TestParseTypeFormatting asserts that Ignored tokens inside a variable type
// reference leave the hash alone while structural differences change it
// (https://spec.graphql.org/September2025/#Type).
func TestParseTypeFormatting(t *testing.T) {
	o := parser.Options{}
	const canonical = `query Q($x: [Int!]!) { f }`
	canonicalHash := hash(t, o, canonical)

	for _, s := range []string{
		`query Q($x: [ Int ! ] !) { f }`,
		`query Q($x:[Int!]!){f}`,
		"query Q($x: [\n\tInt!\n]!) { f }",
		"query Q($x: [\rInt!\r]!) { f }",
		`query Q($x: [Int!,]!) { f }`,
		"query Q($x: [ # comment\n\tInt ! # comment\n\t] !) { f }",
		`query Q($x: ` + bom + `[` + bom + `Int` + bom +
			`!` + bom + `]` + bom + `!` + bom + `) { f }`,
	} {
		if hash(t, o, s) != canonicalHash {
			t.Errorf("formatting must not change the hash: %q", s)
		}
	}

	// Nested list types are normalized at every level.
	if hash(t, o, `query Q($x: [[Int!]!]!) { f }`) !=
		hash(t, o, "query Q($x: [ [\tInt ! ] ! # comment\n]  !) { f }") {
		t.Error("formatting must not change the hash of a nested list type")
	}

	// [parser.IgnoreInputs] keeps the variable signature, so the type
	// is still hashed and must still be normalized. Under
	// [parser.IgnoreVariables] the whole definition is dropped, which
	// makes even structurally different types equal.
	ignoreInputs := parser.Options{Ignore: parser.IgnoreInputs}
	if hash(t, ignoreInputs, canonical) !=
		hash(t, ignoreInputs, `query Q($x: [ Int ! ] !) { f }`) {
		t.Error("IgnoreInputs must normalize the type as well")
	}
	if hash(t, ignoreInputs, canonical) == hash(t, ignoreInputs, `query Q($x: Int) { f }`) {
		t.Error("IgnoreInputs must keep distinguishing types")
	}
	ignoreVars := parser.Options{Ignore: parser.IgnoreVariables}
	if hash(t, ignoreVars, canonical) != hash(t, ignoreVars, `query Q($x: Int) { f }`) {
		t.Error("IgnoreVariables must drop the type entirely")
	}

	// Structurally different types must keep producing different hashes.
	types := []string{
		"Int", "Int!", "[Int]", "[Int!]", "[Int]!", "[Int!]!", "[[Int]]", "Float",
	}
	for i, a := range types {
		for _, b := range types[i+1:] {
			ha := hash(t, o, `query Q($x: `+a+`) { f }`)
			hb := hash(t, o, `query Q($x: `+b+`) { f }`)
			if ha == hb {
				t.Errorf("types %q and %q must produce different hashes", a, b)
			}
		}
	}

	// A malformed type reference.
	for _, s := range []string{
		`query Q($x: ) { f }`, `query Q($x: [) { f }`, `query Q($x: []) { f }`,
		`query Q($x: [Int) { f }`, `query Q($x: [Int!) { f }`,
		`query Q($x: Int]) { f }`, `query Q($x: @d) { f }`,
	} {
		if _, err := parse(o, s); !errors.Is(err.Err, parser.ErrUnexpectedToken) &&
			!errors.Is(err.Err, parser.ErrUnexpectedEOF) {
			t.Errorf("expected a syntax error; received: %v; input: %q", err, s)
		}
	}
}

// TestErrorPosition pins the character the offset of a [parser.Error] points at,
// as [parser.Position] reports it.
func TestErrorPosition(t *testing.T) {
	f := func(t *testing.T, expectLine, expectColumn int, input string) {
		t.Helper()
		_, e := parse(parser.Options{}, input)
		if e.Err == nil {
			t.Fatalf("expected an error; received none")
		}
		if e.Offset < 0 || e.Offset > len(input) {
			t.Errorf("offset %d out of range for %q", e.Offset, input)
		}
		line, column := parser.Position(input, e.Offset)
		if line != expectLine || column != expectColumn {
			t.Errorf("expected line %d, column %d; received line %d, column %d;"+
				" input: %q", expectLine, expectColumn, line, column, input)
		}
		// Both input types report the same position.
		if l, c := parser.Position([]byte(input), e.Offset); l != line || c != column {
			t.Errorf("[]byte: expected line %d, column %d; received line %d,"+
				" column %d", line, column, l, c)
		}
	}

	f(t, 1, 1, "")
	f(t, 1, 1, "?")
	f(t, 1, 2, "{")
	f(t, 1, 3, "{ ?")

	// Every LineTerminator starts a new line, CRLF counts as one.
	f(t, 2, 1, "{\n?")
	f(t, 2, 1, "{\r\n?")
	f(t, 2, 1, "{\r?")
	f(t, 3, 4, "query Q {\n\tf(a: 1)\n\t\t\t?")

	// A column counts characters, not bytes: the 3-byte ツ counts as one.
	f(t, 1, 13, `{ f(a: "ok" ?) }`)
	f(t, 1, 13, `{ f(a: "ツ") ? }`)
	f(t, 1, 3, "{ ツ? }")

	// The position of a value that breaks a rule.
	f(t, 2, 9, "query Q {\n  f(a: 01)\n}")
	f(t, 1, 19, `query Q($x: Int = $y) { f }`)

	// A comment doesn't shift the line count.
	f(t, 2, 1, "# comment\n?")

	// A control character in a string, and a byte that is no SourceCharacter,
	// are reported where they are and not at the end of the document.
	f(t, 1, 9, "{ f(a: \"\x00\") }")
	f(t, 2, 3, "{ f(a:\n\t\"\x1f\") }")
	f(t, 1, 9, "{ f(a: \"\xff\") }")
	f(t, 1, 11, "{ f(a: \"\"\"\xff\"\"\") }")
}

// TestParseErrEOF tests all possible EOF situations.
func TestParseErrEOF(t *testing.T) {
	for _, s := range internal.TestUnexpectedEOF {
		if _, err := parse(parser.Options{}, s); err.Err == nil {
			t.Errorf("expected %v; input: %q", parser.ErrUnexpectedEOF, s)
		} else if !errors.Is(err.Err, parser.ErrUnexpectedEOF) {
			t.Errorf("expected %v; received: %v; input: %q",
				parser.ErrUnexpectedEOF, err, s)
		}

		// The queries that end with a string with an unfinished escape sequence
		// produce [parser.ErrUnexpectedToken] once a byte follows, skip those.
		if strings.HasSuffix(s, `"\`) ||
			strings.HasSuffix(s, `"\u`) ||
			strings.HasSuffix(s, `"""\`) ||
			strings.HasSuffix(s, `"""\u`) {
			continue
		}

		in := s + "\n"
		if _, err := parse(parser.Options{}, in); !errors.Is(
			err.Err, parser.ErrUnexpectedEOF,
		) {
			t.Errorf("(with ignorable suffix) expected %v; received: %v in %q",
				parser.ErrUnexpectedEOF, err, in)
		}
	}
}

// TestParseErrUnexpectedToken tests all possible unexpected token situations.
func TestParseErrUnexpectedToken(t *testing.T) {
	for _, s := range internal.TestErrUnexpectedToken {
		if _, err := parse(parser.Options{}, s); !errors.Is(
			err.Err, parser.ErrUnexpectedToken,
		) {
			t.Errorf("expected ErrUnexpectedToken; received: %v (input: %q)", err, s)
		}
	}
}

// hashPrefixes lists every prefix of the canonical form.
var hashPrefixes = []byte{
	parser.HPrefQuery,
	parser.HPrefMutation,
	parser.HPrefSubscription,
	parser.HPrefFragmentDefinition,
	parser.HPrefVariableDefinition,
	parser.HPrefDirective,
	parser.HPrefField,
	parser.HPrefType,
	parser.HPrefFieldAliasedName,
	parser.HPrefFragmentSpread,
	parser.HPrefInlineFragment,
	parser.HPrefArgument,
	parser.HPrefSelectionSet,
	parser.HPrefSelectionSetEnd,
	parser.HPrefValueInputObject,
	parser.HPrefValueInputObjectField,
	parser.HPrefInputObjectEnd,
	parser.HPrefValueNull,
	parser.HPrefValueTrue,
	parser.HPrefValueFalse,
	parser.HPrefValueInteger,
	parser.HPrefValueFloat,
	parser.HPrefValueEnum,
	parser.HPrefValueString,
	parser.HPrefValueList,
	parser.HPrefValueListEnd,
	parser.HPrefValueVariable,
}

// TestHPrefInStringValue asserts that no prefix of the canonical form can appear
// in a string value unescaped.
func TestHPrefInStringValue(t *testing.T) {
	for _, hpref := range hashPrefixes {
		// A single-line string rejects the byte, it's a control character.
		s := `{f(a:"` + string(hpref) + `")}`
		if expectLen := len(`{f(a:"`) + 1 + len(`")}`); len(s) != expectLen {
			t.Fatalf("expected string value slice len: %d; received: %d",
				expectLen, len(s))
		}
		if _, err := parse(parser.Options{}, s); err.Err != parser.ErrUnescapedControlChar {
			t.Errorf("hpref %#x must not be valid within a string value: %q; "+
				"expected: %v; received: %v",
				hpref, s, parser.ErrUnescapedControlChar, err)
		}

		// A block string may hold the byte, but it's escaped in the stream, so
		// it can't imitate a prefix.
		s = `{f(a:"""` + string(hpref) + `""")}`
		if expectLen := len(`{f(a:"""`) + 1 + len(`""")}`); len(s) != expectLen {
			t.Fatalf("expected block string value slice len: %d; received: %d",
				expectLen, len(s))
		}
		got, err := parse(parser.Options{}, s)
		if err.Err != nil {
			t.Errorf("hpref %#x must be valid within a block string value: %q; "+
				"received: %v", hpref, s, err)
		}
		// Adding 0x40 turns the control byte into a printable character, so the
		// stream holds the escape sequence and not the prefix itself.
		expect := stream(
			parser.HPrefQuery, parser.HPrefSelectionSet,
			parser.HPrefField, "f", parser.HPrefArgument, "a",
			parser.HPrefValueString, `\`, hpref+0x40,
			parser.HPrefSelectionSetEnd,
		)
		if got != expect {
			t.Errorf("hpref %#x is not escaped in the stream: %q; want %q",
				hpref, got, expect)
		}
	}
}

func TestParseIgnoreInputs(t *testing.T) {
	// Exercises every value kind so the
	// [parser.IgnoreInputs] branches are covered.
	const dense = `query Q($v: Int = 7) {
		f(
			i: 42, fl: 3.14, s: "text", bs: """block""",
			b: true, n: null, e: ENUM_VALUE, var: $v,
			list: [1, "two", THREE], obj: {a: 1, b: "x"}
		) @dir(k: 9) { sub }
	}`
	// Same structure, different input values everywhere (including differing
	// booleans and value types), so the structure hash must match.
	const denseOtherValues = `query Q($v: Int = 999) {
		f(
			i: 1, fl: 9.99, s: "different", bs: """other""",
			b: false, n: null, e: OTHER_ENUM, var: $v,
			list: [9, "nine", NINE], obj: {a: 8, b: "y"}
		) @dir(k: 0) { sub }
	}`
	// Structural difference (extra field).
	const denseExtraField = `query Q($v: Int = 7) {
		f(i: 42) @dir(k: 9) { sub extra }
	}`

	ignore := parser.Options{Ignore: parser.IgnoreInputs}
	full := parser.Options{}

	// Ignoring inputs: identical structure with different values must match.
	if hash(t, ignore, dense) != hash(t, ignore, denseOtherValues) {
		t.Error("structure hash must ignore differing input values")
	}
	// Structural differences must still be observed.
	if hash(t, ignore, dense) == hash(t, ignore, denseExtraField) {
		t.Error("structure hash must still distinguish structure")
	}
	// Every argument value collapses entirely under [parser.IgnoreInputs],
	// not just the content but the type and container structure too. Scalars of any type,
	// lists of any length (incl. empty), input objects with any fields, and
	// variable usages (nested or not) all become equivalent to a bare value.
	base := hash(t, ignore, `{ f(x: 1) }`)
	for _, v := range []string{
		`"1"`, `1.0`, `true`, `false`, `null`, `ENUM`, `"""blk"""`, // scalars
		`$v`,                           // a variable usage is just a value
		`[1, 2]`, `[1, 4, 6, 1]`, `[]`, // lists of any length
		`{a: 1}`, `{k: "ok", y: 42}`, `{}`, // objects with any fields
		`[$a, 1]`, `{k: $v}`, // nested variables collapse too
		`[[[1]]]`, `{a: {b: {c: 1}}}`, // any depth collapses
	} {
		if hash(t, ignore, `{ f(x: `+v+`) }`) != base {
			t.Errorf("value %s should collapse to a bare value under IgnoreInputs", v)
		}
	}
	// The variable signature is kept, though: a query declaring a variable
	// differs from one that doesn't (the boundary with [parser.IgnoreVariables]).
	if hash(t, ignore, `query ($v: Int) { f }`) == hash(t, ignore, `query { f }`) {
		t.Error("IgnoreInputs must keep the variable signature")
	}
	// Full hash still differs from structure hash when values are present.
	if hash(t, full, dense) == hash(t, ignore, dense) {
		t.Error("full and structure hashes must differ when inputs are hashed")
	}
	// A query without any input values hashes the same either way.
	const noInputs = `{ a { b c } ...F }`
	if hash(t, full, noInputs) != hash(t, ignore, noInputs) {
		t.Error("without input values, full and structure hashes must match")
	}
	// An ignored value is still parsed, so it must still be valid.
	if _, err := parse(ignore, `{ f(x: 01) }`); err.Err != parser.ErrMalformedNumber {
		t.Errorf("an ignored value must still be validated; received: %v", err)
	}
	if _, err := parse(ignore, `query Q($x: Int = $y) { f }`); err.Err !=
		parser.ErrUnexpectedVariable {
		t.Errorf("an ignored value must still be validated; received: %v", err)
	}
}

func TestParseIgnoreVariables(t *testing.T) {
	ignoreVars := parser.Options{Ignore: parser.IgnoreVariables}

	// [parser.IgnoreVariables] is a superset of [parser.IgnoreInputs]:
	// variable definitions, variable usages AND literal input values all collapse.
	// The `@dep` directive and default value on the base also exercise the
	// parse-but-don't-hash path.
	base := hash(t, ignoreVars, `query Q($x: Int = 1 @dep) { f(a: $x) }`)
	for _, q := range []string{
		`query Q($y: String) { f(a: $y) }`,             // different variable
		`query Q { f(a: 1) }`,                          // literal instead of variable
		`query Q { f(a: "different") }`,                // different literal type
		`query Q("Doc" $y: [Int!]! = [1]) { f(a: 2) }`, // description and list default
	} {
		if hash(t, ignoreVars, q) != base {
			t.Errorf("IgnoreVariables should produce the base hash for %q", q)
		}
	}

	// A parameterized operation matches its unparameterized form.
	if hash(t, ignoreVars, `query Q($x: Int) { f }`) !=
		hash(t, ignoreVars, `query Q { f }`) {
		t.Error("IgnoreVariables must ignore the variable definition signature")
	}

	// Superset check: whatever [parser.IgnoreInputs] makes equal,
	// [parser.IgnoreVariables] does too.
	inputs := parser.Options{Ignore: parser.IgnoreInputs}
	q1, q2 := `{ f(a: 1, b: ENUM) }`, `{ f(a: 2, b: OTHER) }`
	if hash(t, inputs, q1) != hash(t, inputs, q2) {
		t.Fatal("precondition: IgnoreInputs should equate q1 and q2")
	}
	if hash(t, ignoreVars, q1) != hash(t, ignoreVars, q2) {
		t.Error("IgnoreVariables must be a superset of IgnoreInputs")
	}

	// Structure is still observed.
	if hash(t, ignoreVars, `{ f(a: 1) }`) ==
		hash(t, ignoreVars, `{ g(a: 1) }`) {
		t.Error("IgnoreVariables must still distinguish structure")
	}

	// An ignored variable definition is still parsed, so it must still be valid.
	for _, q := range []string{
		`query Q($x) { f }`, `query Q($x: ) { f }`, `query Q($) { f }`,
		`query Q($x: Int = 01) { f }`, `query Q($x: Int = $y) { f }`,
	} {
		if _, err := parse(ignoreVars, q); err.Err == nil {
			t.Errorf("an ignored variable definition must still be validated: %q", q)
		}
	}
}

// TestParseInputTypes asserts that every input type produces the same result.
func TestParseInputTypes(t *testing.T) {

	const input = `query Q($x: [Int!]! = [1, 2]) { f(a: "s") @d { b } }`
	expect, err := parse(parser.Options{}, input)
	if err.Err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	check := func(name, got string, e parser.Error) {
		t.Helper()
		if e.Err != nil {
			t.Errorf("%s: unexpected error: %v", name, e)
		}
		if got != expect {
			t.Errorf("%s: expected stream %q; received %q", name, expect, got)
		}
	}

	{
		r := new(recorder)
		e := parser.Parse(r, parser.Options{}, []byte(input))
		check("[]byte", r.String(), e)
	}
	{
		r := new(recorder)
		e := parser.NewParser[string](0, 0).Parse(r, parser.Options{}, input)
		check("Parser[string]", r.String(), e)
	}
	{
		r := new(recorder)
		e := parser.NewParser[[]byte](0, 0).Parse(r, parser.Options{}, []byte(input))
		check("Parser[[]byte]", r.String(), e)
	}

	// An empty input is no document, whatever its type.
	for _, e := range []parser.Error{
		parser.Parse(new(recorder), parser.Options{}, ""),
		parser.Parse(new(recorder), parser.Options{}, []byte(nil)),
		parser.Parse(new(recorder), parser.Options{}, []byte{}),
	} {
		if e.Err != parser.ErrUnexpectedEOF {
			t.Errorf("expected %v; received: %v", parser.ErrUnexpectedEOF, e)
		}
	}
}

// TestParserReuse asserts that a reused parser produces the result of a fresh
// one, whatever it read before, and that it stops allocating.
func TestParserReuse(t *testing.T) {
	inputs := []string{
		`{f}`,
		`query Q($x: Int = 1) { f(a: [1, {k: "s"}]) @d }`,
		`{f(a:"` + strings.Repeat("x", 8192) + `")}`,                       // Grows the buffer.
		"{f(a:" + strings.Repeat("[", 64) + strings.Repeat("]", 64) + ")}", // Grows the stack.
		`{`, // An error must not leave the parser in a bad state.
		`fragment F on T { f } { ...F }`,
	}

	p := parser.NewParser[string](1, 1)
	for round := range 3 {
		for _, input := range inputs {
			r, fresh := new(recorder), new(recorder)
			errReuse := p.Parse(r, parser.Options{}, input)
			errFresh := parser.NewParser[string](0, 0).Parse(
				fresh, parser.Options{}, input,
			)
			if errReuse != errFresh {
				t.Errorf("round %d: error %v; want %v; input: %.32q",
					round, errReuse, errFresh, input)
			}
			if r.String() != fresh.String() {
				t.Errorf("round %d: stream differs for %.32q", round, input)
			}
		}
	}

	// A warmed-up parser allocates nothing.
	h := internal.NoopHash{}
	const doc = `query Q($x: Int = 1) { f(a: [1, {k: "s"}]) @d { b c } }`
	_ = p.Parse(h, parser.Options{}, doc)
	if n := testing.AllocsPerRun(100, func() {
		_ = p.Parse(h, parser.Options{}, doc)
	}); n != 0 {
		t.Errorf("expected no allocations; received %v", n)
	}
}

// TestParseConcurrent asserts that the buffers the package-level [parser.Parse]
// takes from its pool are never shared between goroutines.
func TestParseConcurrent(t *testing.T) {
	inputs := []string{
		`{f}`,
		`query Q($x: Int = 1) { f(a: [1, {k: "s"}]) @d { b } }`,
		`fragment F on T { a b } { ...F }`,
		`{f(a:"` + strings.Repeat("x", 6000) + `")}`,
		`{`,
	}
	want := make([]string, len(inputs))
	for i, input := range inputs {
		s, err := parse(parser.Options{}, input)
		want[i] = s + "|" + fmt.Sprint(err.Err)
	}

	const goroutines = 8
	errs := make(chan string, goroutines*len(inputs))
	done := make(chan struct{})
	for range goroutines {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 200 {
				for i, input := range inputs {
					r := new(recorder)
					err := parser.Parse(r, parser.Options{}, input)
					if got := r.String() + "|" + fmt.Sprint(err.Err); got != want[i] {
						errs <- fmt.Sprintf("input %q: %q; want %q",
							input, got, want[i])
						return
					}
				}
			}
		}()
	}
	for range goroutines {
		<-done
	}
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// failWriter fails every write, like a file that ran out of space.
type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

// TestParseWriteError asserts that the error of the destination reaches the
// caller.
func TestParseWriteError(t *testing.T) {
	wantErr := errors.New("no space left on device")
	w := failWriter{err: wantErr}

	for _, input := range []string{
		`{f}`,
		`query Q($x: Int = 1) { f(a: [1, {k: "s"}]) @d { b } }`,
		`{f(a:"` + strings.Repeat("x", 9000) + `")}`, // Outgrows the buffer.
	} {
		e := parser.Parse(w, parser.Options{}, input)
		if e.Err != wantErr {
			t.Errorf("expected %v; received: %v; input: %.32q", wantErr, e, input)
		}
		if !errors.Is(e, wantErr) {
			t.Error("expected errors.Is to match the write error")
		}
		// A write error has no position, so the message stays the message of the writer.
		if e.Offset != -1 {
			t.Errorf("expected offset -1; received %+v", e)
		}
		if line, column := parser.Position(input, e.Offset); line != 0 || column != 0 {
			t.Errorf("expected no position; received line %d, column %d", line, column)
		}
		if e.Error() != wantErr.Error() {
			t.Errorf("expected message %q; received %q", wantErr, e.Error())
		}
	}

	// An invalid document is rejected before anything is written, so the syntax
	// error wins over the write error.
	if e := parser.Parse(w, parser.Options{}, `{`); e.Err != parser.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", parser.ErrUnexpectedEOF, e)
	}

	// The parser stays usable after a failed write.
	r := new(recorder)
	if e := parser.Parse(r, parser.Options{}, `{f}`); e.Err != nil {
		t.Errorf("unexpected error: %v", e)
	}
	if want := stream(
		parser.HPrefQuery, parser.HPrefSelectionSet,
		parser.HPrefField, "f", parser.HPrefSelectionSetEnd,
	); r.String() != want {
		t.Errorf("expected stream %q; received %q", want, r.String())
	}
}

// TestPosition covers the offsets [parser.Position] takes that no [parser.Error] carries.
func TestPosition(t *testing.T) {
	const src = "{\n\tf\n}"

	// A negative offset is what an error without a position carries.
	for _, offset := range []int{-1, -2, math.MinInt} {
		if line, column := parser.Position(src, offset); line != 0 || column != 0 {
			t.Errorf("offset %d: expected 0, 0; received %d, %d",
				offset, line, column)
		}
	}

	// An offset past the end is clamped to the end.
	end, endColumn := parser.Position(src, len(src))
	for _, offset := range []int{len(src) + 1, math.MaxInt} {
		line, column := parser.Position(src, offset)
		if line != end || column != endColumn {
			t.Errorf("offset %d: expected %d, %d; received %d, %d",
				offset, end, endColumn, line, column)
		}
	}

	// The first byte of an empty document is line 1, column 1.
	if line, column := parser.Position("", 0); line != 1 || column != 1 {
		t.Errorf("expected 1, 1; received %d, %d", line, column)
	}
}

// TestIgnoreDocExamples pins the examples documented on [parser.IgnoreInputs] and
// [parser.IgnoreVariables]. A doc example is a claim about the hash,
// so it belongs in a test.
func TestIgnoreDocExamples(t *testing.T) {
	equal := func(t *testing.T, ignore parser.Ignore, documents ...string) {
		t.Helper()
		o := parser.Options{Ignore: ignore}
		want := hash(t, o, documents[0])
		for _, d := range documents[1:] {
			if got := hash(t, o, d); got != want {
				t.Errorf("expected %q to hash like %q", d, documents[0])
			}
		}
	}
	differ := func(t *testing.T, ignore parser.Ignore, a, b string) {
		t.Helper()
		o := parser.Options{Ignore: ignore}
		if hash(t, o, a) == hash(t, o, b) {
			t.Errorf("expected %q and %q to differ", a, b)
		}
	}

	// [parser.IgnoreInputs].
	equal(t, parser.IgnoreInputs,
		`{ user(id: 1, role: ADMIN) { name } }`,
		`{ user(id: 42, role: GUEST) { name } }`,
		`{ user(id: "42", role: GUEST) { name } }`)
	differ(t, parser.IgnoreInputs,
		`{ user(id: 1, role: ADMIN) { name } }`,
		`query($id: ID) { user(id: $id, role: GUEST) { name } }`)
	// The name of an argument is kept, so dropping the argument still differs.
	differ(t, parser.IgnoreInputs, `{ f(x: 1) }`, `{ f }`)

	// [parser.IgnoreVariables].
	equal(t, parser.IgnoreVariables,
		`query Q($x: Int = 1) { f(a: $x) }`,
		`query Q($y: String) { f(a: $y) }`,
		`query Q { f(a: 1) }`)
	equal(t, parser.IgnoreVariables,
		`query Q($x: Int) { f(x: $x) }`,
		`query Q { f(x: 1) }`)
	differ(t, parser.IgnoreVariables, `{ f(x: 1) }`, `{ f }`)
}
