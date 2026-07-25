package parser_test

import (
	"crypto/sha1"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/internal"
	"github.com/romshark/gqlhash/parser"
)

func TestSkipIgnorables(t *testing.T) {
	f := func(t *testing.T, expect, input string) {
		t.Helper()
		a := parser.SkipIgnorables([]byte(input))
		if expect != string(a) {
			t.Errorf("expected %q; received: %q", expect, a)
		}
	}

	f(t, "", "")

	f(t, "", ",")
	f(t, "xyz", ",xyz")
	f(t, "xyz", " ,\t\r\nxyz")
	f(t, "", "# this should be skipped")
	f(t, "", "# this should be skipped\n\n\t # and this\n\t")
	f(t, "but not this", "# this should be skipped\n\n\t # and this\n\tbut not this")
	// Bare CR is a LineTerminator, so it ends the comment just like LF.
	f(t, "but not this", "# this should be skipped\rbut not this")

	f(t, "(", "(")
	f(t, "{", "{")
	f(t, "xyz", "xyz")
}

func TestReadDocument(t *testing.T) {
	f := func(t *testing.T, expectErr error, input string) {
		t.Helper()
		err := parser.ReadDocument(internal.NoopHash{}, []byte(input))
		if expectErr != err {
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
}

func TestReadDefinition(t *testing.T) {
	f := func(t *testing.T, expectSuffix string, expectErr error, input string) {
		t.Helper()
		suffix, err := parser.ReadDefinition(internal.NoopHash{}, []byte(input))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected %q; received: %q", expectSuffix, suffix)
		}
	}

	f(t, "", parser.ErrUnexpectedEOF, "")
	f(t, "()", parser.ErrUnexpectedToken, "()")

	suffix := ","
	f(t, suffix, nil, "{ anonymousOperation }"+suffix)
	f(t, suffix, nil, `fragment UserInfo on User {
		__typename
		... on Admin { privileges { id name } }
		... on Customer { id email }
	}`+suffix)
	f(t, suffix, nil, "mutation {likeStory(storyID: 12345) {story {likeCount}}}"+suffix)
	f(t, suffix, nil, "query Stories { stories ( limit : 5 ) { id } }"+suffix)
	f(t, suffix, nil, "subscription Updates { updates }"+suffix)
}

func TestReadSelectionSet(t *testing.T) {
	f := func(t *testing.T, expectSuffix string, expectErr error, input string) {
		t.Helper()
		suffix, err := parser.ReadSelectionSet(internal.NoopHash{}, []byte(input))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected %q; received: %q", expectSuffix, suffix)
		}
	}

	f(t, "", parser.ErrUnexpectedEOF, "")
	f(t, "()", parser.ErrUnexpectedToken, "()")

	suffix := ","
	f(t, suffix, nil, "{ foo }"+suffix)
	f(t, suffix, nil, "{ foo bar bazz }"+suffix)
	f(t, suffix, nil, "{ foo ...Foo bazz }"+suffix)
	f(t, suffix, nil, `{
		foo
		... @ include ( if : $this ) {
			included
		}
		bazz
	}`+suffix)
	f(t, suffix, nil, `{
		foo @directive
		...Foo @directive
		... on Bar @directive {
			fraz @directive
		}
		mazz : bazz @directive
	}`+suffix)
	f(t, suffix, nil, "{ likeStory ( storyID: 12345 ) { story { likeCount } } }"+suffix)
}

func TestReadArguments(t *testing.T) {
	f := func(t *testing.T, expect, expectSuffix string, expectErr error, input string) {
		t.Helper()
		a, suffix, err := parser.ReadArguments(internal.NoopHash{}, []byte(input))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected %q; received: %q", expectSuffix, suffix)
		}
		if expect != string(a) {
			t.Errorf("expected %q; received: %q", expect, a)
		}
	}

	f(t, "", "", parser.ErrUnexpectedEOF, "")
	f(t, "", "{", parser.ErrUnexpectedToken, "{")
	f(t, "", ")", parser.ErrUnexpectedToken, "()")

	f(t, "(life:42)", "", nil, "(life:42)")
	f(t, "(x: 4.13\ny : 62.0)", "", nil, "(x: 4.13\ny : 62.0)")
	f(t, `(foo:"bar",bazz:"fuzz")`, "", nil, `(foo:"bar",bazz:"fuzz")`)
}

func TestReadDirectives(t *testing.T) {
	f := func(t *testing.T, expect, expectSuffix string, expectErr error, input string) {
		t.Helper()
		a, suffix, err := parser.ReadDirectives(internal.NoopHash{}, []byte(input))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected %q; received: %q", expectSuffix, suffix)
		}
		if expect != string(a) {
			t.Errorf("expected %q; received: %q", expect, a)
		}
	}

	// Not a directives list (since directives are optional).
	f(t, "", "", nil, "")
	f(t, "", "{", nil, "{")
	f(t, "", "directive", nil, "directive")
	f(t, "", "(foo:42)", nil, "(foo:42)")

	// Malformed directives.
	f(t, "", "(foo:42)", parser.ErrUnexpectedToken, "@(foo:42)")
	f(t, "", "", parser.ErrUnexpectedEOF, "@")

	f(t, "@directive(life:42)", "", nil,
		"@directive(life:42)")
	{
		input := "@translation(\n" +
			"\tlang: {\n\t\tcode: DE,\n\t\tabbr: true\n\t},\n" +
			"\tapplyFilters: true\n)\n" +
			"@flip @rel(direction: XYZ)@public"
		f(t, input, "{foo}", nil, input+"{foo}")
	}
}

func TestHasPrefix(t *testing.T) {
	f := func(t *testing.T, s, prefix string) {
		t.Helper()
		a, e := parser.HasPrefix([]byte(s), prefix), strings.HasPrefix(s, prefix)
		if a != e {
			t.Errorf("expected %t; received: %t", e, a)
		}
	}

	f(t, "", "")
	f(t, "", "prefix")
	f(t, "prefix", "prefix")
	f(t, "prefixsuffix", "prefix")
	f(t, "prefixsuffix", "suffix")
}

func TestReadName(t *testing.T) {
	f := func(t *testing.T, expectName, expectSuffix string, expectErr error, s string) {
		t.Helper()
		name, suffix, err := parser.ReadName([]byte(s))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectName != string(name) {
			t.Errorf("expected name: %q; received name: %q", expectName, name)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected suffix: %q; received suffix: %q", expectSuffix, suffix)
		}
	}

	// Errors.
	f(t, "", "", parser.ErrUnexpectedEOF, "")
	f(t, "", "(", parser.ErrUnexpectedToken, "(")
	f(t, "", "{", parser.ErrUnexpectedToken, "{")
	f(t, "", "ж", parser.ErrUnexpectedToken, "ж")
	f(t, "", "ツ", parser.ErrUnexpectedToken, "ツ")
	f(t, "", "@", parser.ErrUnexpectedToken, "@")

	// Different suffixes.
	f(t, "x", "", nil, "x") // No suffix.
	f(t, "x", " ", nil, "x ")
	f(t, "x", " space", nil, "x space")
	f(t, "x", ",comma", nil, "x,comma")
	f(t, "x", "\nline-break", nil, "x\nline-break")
	f(t, "x", "\ttab", nil, "x\ttab")
	f(t, "x", "(left parenthesis", nil, "x(left parenthesis")
	f(t, "x", "юникoд", nil, "xюникoд")
	f(t, "x", "-dash", nil, "x-dash")

	{ // Different names.
		const suffix = " suffix"
		f(t, "name", suffix, nil, "name"+suffix)
		f(t, "_0", suffix, nil, "_0"+suffix)
		f(t, "_name", suffix, nil, "_name"+suffix)
		f(t, "__typename", suffix, nil, "__typename"+suffix)
		f(t, "fooBar", suffix, nil, "fooBar"+suffix)
		f(t, "foo_Bar42", suffix, nil, "foo_Bar42"+suffix)
	}
}

func TestReadType(t *testing.T) {
	f := func(
		t *testing.T,
		expectRaw string,
		expectNullable bool,
		expectArray bool,
		expectSuffix string,
		expectErr error,
		s string,
	) {
		t.Helper()
		raw, nullable, array, suffix, err := parser.ReadType([]byte(s))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectRaw != string(raw) {
			t.Errorf("expected raw: %q; received raw: %q", expectRaw, raw)
		}
		if expectNullable != nullable {
			t.Errorf("expected nullable: %t", expectNullable)
		}
		if expectArray != array {
			t.Errorf("expected array: %t", expectArray)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected suffix: %q; received suffix: %q", expectSuffix, suffix)
		}
	}

	// Errors (always nullable by default).
	f(t, "", true, false, "", parser.ErrUnexpectedEOF, "")
	f(t, "", true, false, "(", parser.ErrUnexpectedToken, "(")
	f(t, "", true, false, "{", parser.ErrUnexpectedToken, "{")
	f(t, "", true, false, "ж", parser.ErrUnexpectedToken, "ж")
	f(t, "", true, false, "ツ", parser.ErrUnexpectedToken, "ツ")
	f(t, "", true, false, "@", parser.ErrUnexpectedToken, "@")
	f(t, "", true, true, "]", parser.ErrUnexpectedToken, "[]")

	// Different suffixes.
	f(t, "x", true, false, "", nil, "x") // No suffix
	f(t, "x", true, false, " ", nil, "x ")
	f(t, "x", true, false, " space", nil, "x space")
	f(t, "x", true, false, ",comma", nil, "x,comma")
	f(t, "x", true, false, "\nline-break", nil, "x\nline-break")
	f(t, "x", true, false, "\ttab", nil, "x\ttab")
	f(t, "x", true, false, "(left parenthesis", nil, "x(left parenthesis")
	f(t, "x", true, false, "юникoд", nil, "xюникoд")
	f(t, "x", true, false, "-dash", nil, "x-dash")

	{ // Different types.
		const suffix = " suffix"
		f(t, "type", true, false, suffix, nil, "type"+suffix)
		f(t, "type!", false, false, suffix, nil, "type!"+suffix)
		f(t, "Type", true, false, suffix, nil, "Type"+suffix)
		f(t, "Type42", true, false, suffix, nil, "Type42"+suffix)
		f(t, "Type_42", true, false, suffix, nil, "Type_42"+suffix)
		f(t, "_Type_42", true, false, suffix, nil, "_Type_42"+suffix)
		f(t, "[_Type_42]", true, true, suffix, nil, "[_Type_42]"+suffix)
		f(t, "[_Type_42]!", false, true, suffix, nil, "[_Type_42]!"+suffix)
	}
}

func TestReadValue(t *testing.T) {
	f := func(
		t *testing.T,
		expectRaw string,
		expectType parser.ValueType,
		expectSuffix string,
		expectErr error,
		s string,
	) {
		t.Helper()
		raw, valueType, suffix, err := parser.ReadValue(internal.NoopHash{}, []byte(s))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
		}
		if expectRaw != string(raw) {
			t.Errorf("expected raw: %q; received raw: %q", expectRaw, raw)
		}
		if expectType != valueType {
			t.Errorf("expected valueType: %v; received valueType: %v",
				expectType, valueType)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected suffix: %q; received suffix: %q", expectSuffix, suffix)
		}
	}

	// fErr asserts only the returned error, used for malformed inputs where the
	// exact raw/suffix of the error recovery isn't specified by the grammar.
	fErr := func(t *testing.T, expectErr error, s string) {
		t.Helper()
		_, _, _, err := parser.ReadValue(internal.NoopHash{}, []byte(s))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v; input: %q",
				expectErr, err, s)
		}
	}

	// Errors (always nullable by default).
	f(t, "", 0, "", parser.ErrUnexpectedEOF, "")
	f(t, "", 0, "(", parser.ErrUnexpectedToken, "(")
	f(t, "", 0, "ж", parser.ErrUnexpectedToken, "ж")
	f(t, "", 0, "ツ", parser.ErrUnexpectedToken, "ツ")
	f(t, "", 0, "@", parser.ErrUnexpectedToken, "@")

	f(t, "0", parser.ValueTypeInt, "", nil, "0") // No suffix.
	f(t, "0", parser.ValueTypeInt, " ", nil, "0 ")
	f(t, "0", parser.ValueTypeInt, " space", nil, "0 space")
	f(t, "0", parser.ValueTypeInt, ",comma", nil, "0,comma")
	f(t, "0", parser.ValueTypeInt, "\nline-break", nil, "0\nline-break")
	f(t, "0", parser.ValueTypeInt, "\ttab", nil, "0\ttab")
	f(t, "0", parser.ValueTypeInt, "(left parenthesis", nil, "0(left parenthesis")
	f(t, "0", parser.ValueTypeInt, "юникoд", nil, "0юникoд")
	f(t, "0", parser.ValueTypeInt, "-dash", nil, "0-dash")

	const suffix = " suffix"

	// Supplementary-plane (astral) characters (https://www.unicode.org/roadmaps/smp),
	// each a 4-byte UTF-8 sequence:
	// U+1F4A9 PILE OF POO and U+1D11E MUSICAL SYMBOL G CLEF.
	// Used to exercise the full Unicode SourceCharacter range
	// (https://spec.graphql.org/September2025/#SourceCharacter).
	astral1, astral2 := string(rune(0x1F4A9)), string(rune(0x1D11E))

	{ // NullValue (https://spec.graphql.org/September2025/#sec-Null-Value).
		f(t, "null", parser.ValueTypeNull, suffix, nil, "null"+suffix)
	}

	{ // BooleanValue (https://spec.graphql.org/September2025/#sec-Boolean-Value).
		f(t, "true", parser.ValueTypeBooleanTrue, suffix, nil, "true"+suffix)
		f(t, "false", parser.ValueTypeBooleanFalse, suffix, nil, "false"+suffix)
	}

	{ // EnumValue (https://spec.graphql.org/September2025/#sec-Enum-Value).
		f(t, "x", parser.ValueTypeEnum, suffix, nil, "x"+suffix)
		f(t, "foo", parser.ValueTypeEnum, suffix, nil, "foo"+suffix)
		f(t, "Bar", parser.ValueTypeEnum, suffix, nil, "Bar"+suffix)
		f(t, "_x", parser.ValueTypeEnum, suffix, nil, "_x"+suffix)
		f(t, "_0", parser.ValueTypeEnum, suffix, nil, "_0"+suffix)

		f(t, "nullable", parser.ValueTypeEnum, suffix, nil, "nullable"+suffix)
		f(t, "trueStory", parser.ValueTypeEnum, suffix, nil, "trueStory"+suffix)
		f(t, "falseFlag", parser.ValueTypeEnum, suffix, nil, "falseFlag"+suffix)
	}

	{ // IntValue (https://spec.graphql.org/September2025/#sec-Int-Value).
		f(t, "0", parser.ValueTypeInt, suffix, nil, "0"+suffix)
		f(t, "-0", parser.ValueTypeInt, suffix, nil, "-0"+suffix)
		f(t, "42", parser.ValueTypeInt, suffix, nil, "42"+suffix)
		f(t, "-42", parser.ValueTypeInt, suffix, nil, "-42"+suffix)
		f(t, "1234567890", parser.ValueTypeInt, suffix, nil, "1234567890"+suffix)
		f(t, "-1234567890", parser.ValueTypeInt, suffix, nil, "-1234567890"+suffix)
		f(t, "10000000000000000000000000", parser.ValueTypeInt, suffix, nil,
			"10000000000000000000000000"+suffix)
		f(t, "-10000000000000000000000000", parser.ValueTypeInt, suffix, nil,
			"-10000000000000000000000000"+suffix)
	}

	{ // FloatValue (https://spec.graphql.org/September2025/#sec-Float-Value).
		f(t, "0.1", parser.ValueTypeFloat, suffix, nil,
			"0.1"+suffix)
		f(t, "-0.1", parser.ValueTypeFloat, suffix, nil,
			"-0.1"+suffix)
		f(t, "42.123", parser.ValueTypeFloat, suffix, nil,
			"42.123"+suffix)
		f(t, "-42.123", parser.ValueTypeFloat, suffix, nil,
			"-42.123"+suffix)
		f(t, "3.14159265359", parser.ValueTypeFloat, suffix, nil,
			"3.14159265359"+suffix) // 🥧
		f(t, "-3.14159265359", parser.ValueTypeFloat, suffix, nil,
			"-3.14159265359"+suffix)
		f(t, "10000000000000000000000000.0", parser.ValueTypeFloat, suffix, nil,
			"10000000000000000000000000.0"+suffix)
		f(t, "-10000000000000000000000000.0", parser.ValueTypeFloat, suffix, nil,
			"-10000000000000000000000000.0"+suffix)
		f(t, "0.1e1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e1234567890"+suffix)

		f(t, "0.1e1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e1234567890"+suffix)
		f(t, "0.1e+1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e+1234567890"+suffix)
		f(t, "0.1e-1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e-1234567890"+suffix)
		f(t, "0.1E+1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1E+1234567890"+suffix)
		f(t, "0.1E-1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1E-1234567890"+suffix)

		f(t, "1e1234567890", parser.ValueTypeFloat, suffix, nil,
			"1e1234567890"+suffix)
		f(t, "1e+1234567890", parser.ValueTypeFloat, suffix, nil,
			"1e+1234567890"+suffix)
		f(t, "1e-1234567890", parser.ValueTypeFloat, suffix, nil,
			"1e-1234567890"+suffix)
		f(t, "1E+1234567890", parser.ValueTypeFloat, suffix, nil,
			"1E+1234567890"+suffix)
		f(t, "1E-1234567890", parser.ValueTypeFloat, suffix, nil,
			"1E-1234567890"+suffix)

		f(t, "10000000000000000000000000.0e+23", parser.ValueTypeFloat, suffix, nil,
			"10000000000000000000000000.0e+23"+suffix)
		f(t, "-10000000000000000000000000.0E+23", parser.ValueTypeFloat, suffix, nil,
			"-10000000000000000000000000.0E+23"+suffix)

		// The exponent needs at least one digit after e/E and the optional sign.
		fErr(t, parser.ErrUnexpectedToken, "1e"+suffix)
		fErr(t, parser.ErrUnexpectedToken, "1E"+suffix)
		fErr(t, parser.ErrUnexpectedToken, "1e+"+suffix)
		fErr(t, parser.ErrUnexpectedToken, "1e-"+suffix)
		fErr(t, parser.ErrUnexpectedToken, "1.0e"+suffix)
		fErr(t, parser.ErrUnexpectedToken, "1.0E-"+suffix)
	}

	{ // Single-line strings (https://spec.graphql.org/September2025/#sec-String-Value).
		f(t, ``, parser.ValueTypeString, `uGGGG"`+suffix, parser.ErrUnexpectedToken,
			`"\uGGGG"`+suffix)
		f(t, "", parser.ValueTypeString, "", parser.ErrUnexpectedEOF, `"\"`)
		f(t, "", parser.ValueTypeString, "", parser.ErrUnexpectedEOF, `"`)
		f(t, "", parser.ValueTypeString, `\k"`+suffix, parser.ErrUnexpectedToken,
			`"\k"`+suffix)

		f(t, ``, parser.ValueTypeString, suffix, nil, `""`+suffix)
		f(t, `\"`, parser.ValueTypeString, suffix, nil, `"\""`+suffix)
		f(t, `\\`, parser.ValueTypeString, suffix, nil, `"\\"`+suffix)
		f(t, `\b`, parser.ValueTypeString, suffix, nil, `"\b"`+suffix)
		f(t, `\f`, parser.ValueTypeString, suffix, nil, `"\f"`+suffix)
		f(t, `\n`, parser.ValueTypeString, suffix, nil, `"\n"`+suffix)
		f(t, `\r`, parser.ValueTypeString, suffix, nil, `"\r"`+suffix)
		f(t, `\t`, parser.ValueTypeString, suffix, nil, `"\t"`+suffix)
		f(t, `\uabcd`, parser.ValueTypeString, suffix, nil, `"\uabcd"`+suffix)
		f(t, `\uABCD`, parser.ValueTypeString, suffix, nil, `"\uABCD"`+suffix)
		f(t, `\u1234`, parser.ValueTypeString, suffix, nil, `"\u1234"`+suffix)
		f(t, `\u5678`, parser.ValueTypeString, suffix, nil, `"\u5678"`+suffix)
		f(t, `\u90aA`, parser.ValueTypeString, suffix, nil, `"\u90aA"`+suffix)
		f(t, `\u3053\u3093\u306b\u3061\u306f`, parser.ValueTypeString, suffix, nil,
			`"\u3053\u3093\u306b\u3061\u306f"`+suffix)

		// Variable-width Unicode escape sequence `\u{HexDigit+}`, according to
		// September 2025 spec (https://spec.graphql.org/September2025/#EscapedUnicode).
		f(t, `\u{0}`, parser.ValueTypeString, suffix, nil, `"\u{0}"`+suffix)
		f(t, `\u{41}`, parser.ValueTypeString, suffix, nil, `"\u{41}"`+suffix)
		f(t, `\u{1F4A9}`, parser.ValueTypeString, suffix, nil, `"\u{1F4A9}"`+suffix)
		f(t, `\u{1f4a9}`, parser.ValueTypeString, suffix, nil, `"\u{1f4a9}"`+suffix)
		f(t, `\u{10FFFF}`, parser.ValueTypeString, suffix, nil, `"\u{10FFFF}"`+suffix)
		f(t, `a\u{1F600}b`, parser.ValueTypeString, suffix, nil, `"a\u{1F600}b"`+suffix)

		// StringCharacter :: \u EscapedUnicode asserts the escaped value is within
		// the Unicode scalar value range (<= 0xD7FF or 0xE000..0x10FFFF), so
		// out-of-range and surrogate escapes are a parse error even though their
		// syntax is valid (https://spec.graphql.org/September2025/#StringCharacter).
		fErr(t, parser.ErrUnexpectedToken, `"\u{110000}"`+suffix)   // Above U+10FFFF.
		fErr(t, parser.ErrUnexpectedToken, `"\u{FFFFFFFF}"`+suffix) // Far above U+10FFFF.
		fErr(t, parser.ErrUnexpectedToken, `"\u{D800}"`+suffix)     // Leading surrogate.
		fErr(t, parser.ErrUnexpectedToken, `"\u{DFFF}"`+suffix)     // Trailing surrogate.

		// The legacy pair production `\u XXXX \u XXXX` is the only way a surrogate
		// may appear: a leading surrogate must be followed by a trailing one, and
		// otherwise both halves must each be scalar values.
		// pairPoo is the surrogate pair for U+1F4A9 PILE OF POO, written as a
		// concatenation so the two escapes stay literal in the source.
		const pairPoo = `\uD83D` + `\uDCA9`
		f(t, pairPoo, parser.ValueTypeString, suffix, nil, `"`+pairPoo+`"`+suffix)
		// Lone leading.
		fErr(t, parser.ErrUnexpectedToken, `"\uD800"`+suffix)
		// Lone trailing.
		fErr(t, parser.ErrUnexpectedToken, `"\uDC00"`+suffix)
		// Leading + leading.
		fErr(t, parser.ErrUnexpectedToken, `"\uD800\uD800"`+suffix)
		// Trailing + trailing.
		fErr(t, parser.ErrUnexpectedToken, `"\uDC00\uDC00"`+suffix)
		// Reversed pair.
		fErr(t, parser.ErrUnexpectedToken, `"\uDC00\uD800"`+suffix)
		// Leading, not paired.
		fErr(t, parser.ErrUnexpectedToken, `"\uD800a"`+suffix)

		// Malformed variable-width escapes must be rejected (September 2025).
		fErr(t, parser.ErrUnexpectedToken, `"\u{}"`+suffix)     // No hex digits.
		fErr(t, parser.ErrUnexpectedToken, `"\u{G}"`+suffix)    // Non-hex digit.
		fErr(t, parser.ErrUnexpectedToken, `"\u{1F4A9"`+suffix) // Missing closing brace.
		fErr(t, parser.ErrUnexpectedEOF, `"\u{1F4A9`)           // Unterminated at EOF.

		// Fixed-width escape truncated by EOF before four hex digits.
		fErr(t, parser.ErrUnexpectedEOF, `"\uAB`)

		// Literal supplementary-plane (astral, 4-byte UTF-8) characters.
		// SourceCharacter spans the full Unicode range (September 2025); gqlhash
		// handles them since it operates on raw UTF-8 bytes.
		f(t, astral1, parser.ValueTypeString, suffix, nil, `"`+astral1+`"`+suffix)
		f(t, "a"+astral2+"b", parser.ValueTypeString, suffix, nil,
			`"a`+astral2+`b"`+suffix)

		f(t, `ok`, parser.ValueTypeString, suffix, nil, `"ok"`+suffix)
		f(t, `one two\t\nthree 123`, parser.ValueTypeString, suffix, nil,
			`"one two\t\nthree 123"`+suffix)
		f(t, `ツ`, parser.ValueTypeString, suffix, nil,
			`"ツ"`+suffix)
		f(t, `ツ\n`, parser.ValueTypeString, suffix, nil,
			`"ツ\n"`+suffix)
		f(t, `ツ ёж ïх жэ こんにちは\n`, parser.ValueTypeString, suffix, nil,
			`"ツ ёж ïх жэ こんにちは\n"`+suffix)
	}

	{ // Block strings (https://spec.graphql.org/September2025/#sec-String-Value).
		f(t, ``, parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF, `"""`)
		f(t, ``, parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF,
			`"""\"""`+suffix)
		f(t, ``, parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF,
			`"""\""""`+suffix)
		f(t, ``, parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF,
			`"""\\"""`+suffix)

		// Empty block string.
		f(t, ``, parser.ValueTypeStringBlock, suffix, nil,
			`""""""`+suffix)

		// Empty block string filled with just tabs, spaces and line-breaks.
		f(t, "", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n"+`"""`+suffix)
		f(t, "", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n \t\n \t\n"+`"""`+suffix)
		f(t, "", parser.ValueTypeStringBlock, suffix, nil,
			`"""    """`+suffix)

		// Empty block string because prefix is stripped.
		f(t, "", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n   "+`"""`+suffix)

		// Empty block string because prefix is stripped.
		f(t, "", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n\t"+`"""`+suffix)

		// Empty block string followed by unclosed string.
		f(t, "", parser.ValueTypeStringBlock, `"`+suffix, nil,
			`"""""""`+suffix)

		// Empty block string followed by string.
		f(t, "", parser.ValueTypeStringBlock, `""`+suffix, nil,
			`""""""""`+suffix)

		// Empty block string followed by unclosed block string.
		f(t, "", parser.ValueTypeStringBlock, `""`+suffix, nil,
			`""""""""`+suffix)

		// Terminators
		f(t, "line1\nline2", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"line1\nline2\n"+`"""`+suffix)

		f(t, `\uGGGG`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\uGGGG"""`+suffix)
		f(t, "\n\\\"", parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n\\\"\n"+`"""`+suffix)
		f(t, `\\`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\\`+"\n"+`"""`+suffix)
		f(t, `\b`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\b"""`+suffix)
		f(t, `\f`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\f"""`+suffix)
		f(t, `\n`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\n"""`+suffix)
		f(t, `\r`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\r"""`+suffix)
		f(t, `\t`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\t"""`+suffix)
		f(t, `\uabcd`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\uabcd"""`+suffix)
		f(t, `\uABCD`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\uABCD"""`+suffix)
		f(t, `\u1234`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u1234"""`+suffix)
		f(t, `\u5678`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u5678"""`+suffix)
		f(t, `\u90aA`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u90aA"""`+suffix)
		f(t, `\u3053\u3093\u306b\u3061\u306f`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u3053\u3093\u306b\u3061\u306f"""`+suffix)

		// Escape sequences are not interpreted in block strings, so the
		// variable-width `\u{...}` form is just literal characters and parses unchanged.
		// Literal astral characters are handled as raw UTF-8 bytes.
		f(t, `\u{1F4A9}`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u{1F4A9}"""`+suffix)
		f(t, astral1, parser.ValueTypeStringBlock, suffix, nil,
			`"""`+astral1+`"""`+suffix)

		f(t, `ok`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ok"""`+suffix)
		f(t, `one two\t\nthree 123`, parser.ValueTypeStringBlock, suffix, nil,
			`"""one two\t\nthree 123"""`+suffix)
		f(t, `ツ`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ"""`+suffix)
		f(t, `ツ\n`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ\n"""`+suffix)
		f(t, `ツ ёж ïх жэ こんにちは\n`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ ёж ïх жэ こんにちは\n"""`+suffix)

		// Empty line suffix.
		f(t, "foo",
			parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"foo\n\n\t\n  \n\n  "+`"""`+suffix)
		f(t, "foo  ",
			parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"foo  \n\n\t\n  \n\n  "+`"""`+suffix)

		f(t, "line one.\n\t\t\t\t\tline two.\n\t\t\t\tline three.",
			parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"line one.\n\t\t\t\t\tline two.\n\t\t\t\tline three.\n"+`"""`+suffix)

		// A leading blank line is dropped and the common indentation stripped,
		// so this block string encodes just "foo".
		f(t, "\n\n    foo",
			parser.ValueTypeStringBlock, suffix, nil,
			`"""`+"\n\n    foo\n    "+`"""`+suffix)
	}

	{ // ListValue (https://spec.graphql.org/September2025/#sec-List-Value).
		f(t, "[]", parser.ValueTypeList, suffix, nil, "[]"+suffix)
		f(t, "[ ]", parser.ValueTypeList, suffix, nil, "[ ]"+suffix)
		f(t, "[,]", parser.ValueTypeList, suffix, nil, "[,]"+suffix)
		f(t, "[12,13 3.14]", parser.ValueTypeList, suffix, nil,
			"[12,13 3.14]"+suffix)
		f(t, `["text" 1 EnumVal]`, parser.ValueTypeList, suffix, nil,
			`["text" 1 EnumVal]`+suffix)
		f(t, `["text" 1 EnumVal ,,,]`, parser.ValueTypeList, suffix, nil,
			`["text" 1 EnumVal ,,,]`+suffix)
	}

	{ // InputObject (https://spec.graphql.org/September2025/#sec-Input-Object-Values).
		f(t, "{}", parser.ValueTypeInputObject, suffix, nil, "{}"+suffix)
		f(t, "{ }", parser.ValueTypeInputObject, suffix, nil, "{ }"+suffix)
		f(t, "{,}", parser.ValueTypeInputObject, suffix, nil, "{,}"+suffix)
		{
			value := `{foo:12,Bar: "13"  __bazz:  3.14}`
			f(t, value, parser.ValueTypeInputObject, suffix, nil, value+suffix)
		}
		{
			value := "{flipAxis : {\n" +
				"\tx: Y_AXIS , # flip x->y\n" +
				"\ty: Z_AXIS , # flip y->z\n" +
				"\tz: null     # don't flip\n" +
				"}}"
			f(t, value, parser.ValueTypeInputObject, suffix, nil, value+suffix)
		}
	}
}

// TestReadDocumentErrEOF tests all possible EOF situations.
func TestReadDocumentErrEOF(t *testing.T) {
	for _, s := range internal.TestUnexpectedEOF {
		t.Helper()
		if err := parser.ReadDocument(internal.NoopHash{}, []byte(s)); err == nil {
			t.Errorf("expected %v", parser.ErrUnexpectedEOF)
		} else if !errors.Is(err, parser.ErrUnexpectedEOF) {
			t.Errorf("expected %v; received: %v", parser.ErrUnexpectedEOF, err)
		}

		// The queries that end with a string with an unfinished escape sequence
		// should would produce [parser.ErrUnexpectedToken], skip those.
		if strings.HasSuffix(s, `"\`) ||
			strings.HasSuffix(s, `"\u`) ||
			strings.HasSuffix(s, `"""\`) ||
			strings.HasSuffix(s, `"""\u`) {
			continue
		}

		in := s + "\n"
		err := parser.ReadDocument(internal.NoopHash{}, []byte(in))
		if !errors.Is(err, parser.ErrUnexpectedEOF) {
			t.Errorf(
				"(with ignorable suffix) expected %v; received: %v in %q",
				parser.ErrUnexpectedEOF, err, in,
			)
		}
	}
}

// TestReadDocumentErrUnexpectedToken tests all possible unexpected token situations.
func TestReadDocumentErrUnexpectedToken(t *testing.T) {
	for _, s := range internal.TestErrUnexpectedToken {
		err := parser.ReadDocument(internal.NoopHash{}, []byte(s))
		if !errors.Is(err, parser.ErrUnexpectedToken) {
			t.Errorf("expected ErrUnexpectedToken; received: %v (input: %q)", err, s)
		}
	}
}

func TestIterateBlockStringLines(t *testing.T) {
	f := func(t *testing.T, expect []string, input string, prefixLen int) {
		t.Helper()
		var r []string
		for s := range parser.IterateBlockStringLines([]byte(input), prefixLen) {
			r = append(r, string(s))
		}
		if !slices.Equal(expect, r) {
			t.Errorf("expected: %#v; received: %#v", expect, r)
		}
	}

	f(t, nil, "", 0)
	f(t, []string{"abc"}, "abc", 0)
	f(t, []string{"abc def"}, "abc def", 0)
	f(t, []string{"abc", " def"}, "abc\n def", 0)
	f(t, []string{"abc", "", " def"}, "abc\n\n def", 0)
	f(t, []string{"abc", " ", " def"}, "abc\n \n def", 0)
	f(t, []string{"abc", " ", " def"}, "\nabc\n \n def", 0)

	// CR and CRLF are LineTerminators too, and CRLF is a single one
	// (https://spec.graphql.org/September2025/#LineTerminator).
	f(t, []string{"abc", " def"}, "abc\r\n def", 0)
	f(t, []string{"abc", " def"}, "abc\r def", 0)
	f(t, []string{"abc", "", " def"}, "abc\r\n\r\n def", 0)
	f(t, []string{"a", "b", "c", "d"}, "a\r\nb\rc\nd", 0)

	// First line no prefix.
	f(t, []string{" abc", "", "def"}, " abc\n \n def", 1)
	f(t, []string{" abc", " ", "def"}, " abc\n  \n def", 1)
	f(t, []string{"\tabc", "\t", "def"}, "\tabc\n\t\t\n\tdef", 1)

	// Empty (this should be handled by the parser func,
	// because the parser func needs to return ""/nil for this input).
	// f(t, nil, "\n \n \n ", 1)
	// f(t, nil, "\n\t \n\t \n\t ", 2)

	// Trailing whitespace (again, parser func needs to return no trailing empty lines).
	// f(t, []string{"x\n"}, "x\n", 0)

	f(t, []string{"ж", "ツ", "\\"}, "\nж\nツ\n\\", 0)
	f(t, []string{"ж", "ツ", "\\"}, "\n ж\n ツ\n \\", 1)
	f(t, []string{"ж", "ツ", "\\"}, "\n  ж\n  ツ\n  \\", 2)
	f(t, []string{"ж", "ツ", "\\"}, "\n   ж\n   ツ\n   \\", 3)
	f(t, []string{"ж", "ツ", "\\"}, "\n\t\t\tж\n\t\t\tツ\n\t\t\t\\", 3)
	f(t, []string{"line one.", "\tline two.", "line three."},
		"line one.\n\t\t\t\t\tline two.\n\t\t\t\tline three.", 4)

	t.Run("break", func(t *testing.T) {
		var r []string
		for s := range parser.IterateBlockStringLines([]byte("foo\nbar"), 0) {
			r = append(r, string(s))
			break
		}
		if !slices.Equal([]string{"foo"}, r) {
			t.Errorf("expected only foo, received: %#v", r)
		}
	})

	t.Run("break2", func(t *testing.T) {
		var r []string
		for s := range parser.IterateBlockStringLines([]byte("foo\nbar"), 0) {
			if len(r) == 1 {
				break
			}
			r = append(r, string(s))
		}
		if !slices.Equal([]string{"foo"}, r) {
			t.Errorf("expected only foo, received: %#v", r)
		}
	})
}

func TestReadStringBlockAfterQuotesCommonIndent(t *testing.T) {
	// A pair of quotes is ordinary block-string content. This first content
	// line must therefore participate in common-indent calculation.
	// https://spec.graphql.org/September2025/#BlockStringValue()
	input := "\n  x\"\"\n    y\n\"\"\"suffix"

	_, prefixLen, suffix, err := parser.ReadStringBlockAfterQuotes([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prefixLen != 2 {
		t.Errorf("expected common indent 2; received: %d", prefixLen)
	}
	if string(suffix) != "suffix" {
		t.Errorf("expected suffix %q; received: %q", "suffix", suffix)
	}
}

func TestTrimEmptyLinesSuffix(t *testing.T) {
	f := func(t *testing.T, expect, input string) {
		t.Helper()
		a := parser.TrimEmptyLinesSuffix([]byte(input))
		if expect != string(a) {
			t.Errorf("expected: %q; received: %q", expect, string(a))
		}
	}

	f(t, "", "")
	f(t, "", "   \n  \n")
	f(t, "", " \t\t  \n\t  \n \t")
	f(t, "foo", "foo")
	f(t, "foo", "foo\n")
	f(t, "foo", "foo\n  ")
	f(t, "foo", "foo\n  \t\n\n  ")
	f(t, "foo  ", "foo  \n  ")
	f(t, "foo\t \t", "foo\t \t\n  ")
	f(t, "foo\t \t", "foo\t \t\n  \n   \n\t\n")

	// Unicode.
	f(t, "ツ ж", "ツ ж")
	f(t, "ツ ж", "ツ ж\n")
	f(t, "ツ ж", "ツ ж\n  ")
	f(t, "ツ ж", "ツ ж\n  \t\n\n  ")
	f(t, "ツ ж  ", "ツ ж  \n  ")
	f(t, "ツ ж\t \t", "ツ ж\t \t\n  ")
	f(t, "ツ ж\t \t", "ツ ж\t \t\n  \n   \n\t\n")

	// GraphQL Whitespace includes only space and horizontal tab. A non-breaking
	// space (U+00A0) is content, so it must not be treated as an empty line.
	f(t, "\u00a0", "\u00a0")
	f(t, "foo\n\u00a0", "foo\n\u00a0")
}

// TestHPrefInStringValue makes sure none of the parser.HPref separators
// can appear in string values without resulting in [parser.ErrUnexpectedToken]
func TestHPrefInStringValue(t *testing.T) {
	f := func(t *testing.T, hpref []byte) {
		t.Helper()
		{
			s := `{f(a:"` + string(hpref) + `")}`

			if expectLen := len(`{f(a:"`) + 1 + len(`")}`); len(s) != expectLen {
				t.Fatalf(
					"expected string value slice len: %d; received: %d",
					expectLen, len(s),
				)
			}

			err := parser.ReadDocument(internal.NoopHash{}, []byte(s))
			if err != parser.ErrUnexpectedToken {
				t.Errorf(
					"hpref %v must not be valid within a string value: %q; "+
						"expected: %v; received: %v",
					hpref, s, parser.ErrUnexpectedToken, err,
				)
			}
		}
		{
			s := `{f(a:"""` + string(hpref) + `""")}`

			if expectLen := len(`{f(a:"""`) + 1 + len(`""")}`); len(s) != expectLen {
				t.Fatalf(
					"expected block string value slice len: %d; received: %d",
					expectLen, len(s),
				)
			}

			err := parser.ReadDocument(internal.NoopHash{}, []byte(s))
			if err != parser.ErrUnexpectedToken {
				t.Errorf(
					"hpref %v must not be valid within a block string value: %q; "+
						"expected: %v; received: %v",
					hpref, s, parser.ErrUnexpectedToken, err,
				)
			}
		}
	}

	f(t, parser.HPrefQuery)
	f(t, parser.HPrefMutation)
	f(t, parser.HPrefSubscription)
	f(t, parser.HPrefFragmentDefinition)
	f(t, parser.HPrefVariableDefinition)
	f(t, parser.HPrefDirective)
	f(t, parser.HPrefField)
	f(t, parser.HPrefType)
	f(t, parser.HPrefFieldAliasedName)
	f(t, parser.HPrefFragmentSpread)
	f(t, parser.HPrefInlineFragment)
	f(t, parser.HPrefArgument)
	f(t, parser.HPrefSelectionSet)
	f(t, parser.HPrefSelectionSetEnd)
	f(t, parser.HPrefValueInputObject)
	f(t, parser.HPrefValueInputObjectField)
	f(t, parser.HPrefInputObjectEnd)
	f(t, parser.HPrefValueNull)
	f(t, parser.HPrefValueTrue)
	f(t, parser.HPrefValueFalse)
	f(t, parser.HPrefValueInteger)
	f(t, parser.HPrefValueFloat)
	f(t, parser.HPrefValueEnum)
	f(t, parser.HPrefValueString)
	f(t, parser.HPrefValueList)
	f(t, parser.HPrefValueListEnd)
	f(t, parser.HPrefValueVariable)
}

func hash(t *testing.T, options parser.Options, s string) string {
	t.Helper()
	h := sha1.New()
	if err := parser.ReadDocumentWithOptions(h, options, []byte(s)); err != nil {
		t.Fatalf("ReadDocumentWithOptions(%q): %v", s, err)
	}
	return string(h.Sum(nil))
}

func TestReadDocumentIgnoreInputs(t *testing.T) {
	// Exercises every value kind so the
	// [parser.Options.IgnoreInputs] branches are covered.
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

	ignore := parser.Options{IgnoreInputs: true}
	full := parser.Options{}

	// Ignoring inputs: identical structure with different values must match.
	if hash(t, ignore, dense) != hash(t, ignore, denseOtherValues) {
		t.Error("structure hash must ignore differing input values")
	}
	// Structural differences must still be observed.
	if hash(t, ignore, dense) == hash(t, ignore, denseExtraField) {
		t.Error("structure hash must still distinguish structure")
	}
	// Every argument value collapses entirely under [parser.Options.IgnoreInputs],
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
	} {
		if hash(t, ignore, `{ f(x: `+v+`) }`) != base {
			t.Errorf("value %s should collapse to a bare value under IgnoreInputs", v)
		}
	}
	// The variable signature is kept, though: a query declaring a variable
	// differs from one that doesn't (the boundary with [parser.Options.IgnoreVariables]).
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
}

func TestReadDocumentIgnoreVariables(t *testing.T) {
	ignoreVars := parser.Options{IgnoreVariables: true}

	// [parser.Options.IgnoreVariables] is a superset of [parser.Options.IgnoreInputs]:
	// variable definitions, variable usages AND literal input values all collapse.
	// The `@dep` directive and default value on the base also exercise the
	// parse-but-don't-hash path.
	base := hash(t, ignoreVars, `query Q($x: Int = 1 @dep) { f(a: $x) }`)
	for _, q := range []string{
		`query Q($y: String) { f(a: $y) }`, // different variable
		`query Q { f(a: 1) }`,              // literal instead of variable
		`query Q { f(a: "different") }`,    // different literal type
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

	// Superset check: whatever [parser.Options.IgnoreInputs] makes equal,
	// [parser.Options.IgnoreVariables] does too.
	inputs := parser.Options{IgnoreInputs: true}
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
}

// TestExportedWrappersDelegate exercises the thin exported wrappers that are
// otherwise only reached indirectly through [parser.ReadDocument].
func TestExportedWrappersDelegate(t *testing.T) {
	h := sha1.New()
	if _, err := parser.ReadOperationDefinition(h, []byte("query Q { f }")); err != nil {
		t.Fatalf("ReadOperationDefinition: %v", err)
	}
	if _, err := parser.ReadVariableDefinitionsAfterParenthesis(
		h, []byte("$x: Int)"),
	); err != nil {
		t.Fatalf("ReadVariableDefinitionsAfterParenthesis: %v", err)
	}
}
