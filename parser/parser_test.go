package parser_test

import (
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

	f(t, "(", "(")
	f(t, "{", "{")
	f(t, "xyz", "xyz")
}

func TestReadDocument(t *testing.T) {
	f := func(t *testing.T, expectErr error, input string) {
		t.Helper()
		err := parser.ReadDocument(internal.NoopHash{}, []byte(input))
		if expectErr != err {
			t.Errorf("expected err: %v; received err: %v", expectErr, err)
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
			t.Errorf("expected valueType: %q; received valueType: %q", expectType, valueType)
		}
		if expectSuffix != string(suffix) {
			t.Errorf("expected suffix: %q; received suffix: %q", expectSuffix, suffix)
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

	{ // NullValue (https://spec.graphql.org/October2021/#sec-Null-Value).
		f(t, "null", parser.ValueTypeNull, suffix, nil, "null"+suffix)
	}

	{ // BooleanValue (https://spec.graphql.org/October2021/#sec-Boolean-Value).
		f(t, "true", parser.ValueTypeBooleanTrue, suffix, nil, "true"+suffix)
		f(t, "false", parser.ValueTypeBooleanFalse, suffix, nil, "false"+suffix)
	}

	{ // EnumValue (https://spec.graphql.org/October2021/#sec-Enum-Value).
		f(t, "x", parser.ValueTypeEnum, suffix, nil, "x"+suffix)
		f(t, "foo", parser.ValueTypeEnum, suffix, nil, "foo"+suffix)
		f(t, "Bar", parser.ValueTypeEnum, suffix, nil, "Bar"+suffix)
		f(t, "_x", parser.ValueTypeEnum, suffix, nil, "_x"+suffix)
		f(t, "_0", parser.ValueTypeEnum, suffix, nil, "_0"+suffix)
	}

	{ // IntValue (https://spec.graphql.org/October2021/#sec-Int-Value).
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

	{ // FloatValue (https://spec.graphql.org/October2021/#sec-Float-Value).
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
		f(t, "0.1e+1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e+1234567890"+suffix)
		f(t, "0.1e-1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1e-1234567890"+suffix)
		f(t, "0.1E+1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1E+1234567890"+suffix)
		f(t, "0.1E-1234567890", parser.ValueTypeFloat, suffix, nil,
			"0.1E-1234567890"+suffix)
		f(t, "10000000000000000000000000.0e+23", parser.ValueTypeFloat, suffix, nil,
			"10000000000000000000000000.0e+23"+suffix)
		f(t, "-10000000000000000000000000.0E+23", parser.ValueTypeFloat, suffix, nil,
			"-10000000000000000000000000.0E+23"+suffix)
	}

	{ // Single-line strings (https://spec.graphql.org/October2021/#sec-String-Value).
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

	{ // Block strings (https://spec.graphql.org/October2021/#sec-String-Value).
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
	}

	{ // ListValue (https://spec.graphql.org/October2021/#sec-List-Value).
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

	{ // InputObject (https://spec.graphql.org/October2021/#sec-Input-Object-Values).
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
		// should would produce ErrUnexpectedToken, skip those.
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
	f(t, []string{"abc\n", " def"}, "abc\n def", 0)
	f(t, []string{"abc\n", "\n", " def"}, "abc\n\n def", 0)
	f(t, []string{"abc\n", " \n", " def"}, "abc\n \n def", 0)
	f(t, []string{"abc\n", " \n", " def"}, "\nabc\n \n def", 0)

	// First line no prefix.
	f(t, []string{" abc\n", "\n", "def"}, " abc\n \n def", 1)
	f(t, []string{" abc\n", " \n", "def"}, " abc\n  \n def", 1)
	f(t, []string{"\tabc\n", "\t\n", "def"}, "\tabc\n\t\t\n\tdef", 1)

	// Empty (this should be handled by the parser func,
	// because the parser func needs to return ""/nil for this input).
	// f(t, nil, "\n \n \n ", 1)
	// f(t, nil, "\n\t \n\t \n\t ", 2)

	// Trailing whitespace (again, parser func needs to return no trailing empty lines).
	// f(t, []string{"x\n"}, "x\n", 0)

	f(t, []string{"ж\n", "ツ\n", "\\"}, "\nж\nツ\n\\", 0)
	f(t, []string{"ж\n", "ツ\n", "\\"}, "\n ж\n ツ\n \\", 1)
	f(t, []string{"ж\n", "ツ\n", "\\"}, "\n  ж\n  ツ\n  \\", 2)
	f(t, []string{"ж\n", "ツ\n", "\\"}, "\n   ж\n   ツ\n   \\", 3)
	f(t, []string{"ж\n", "ツ\n", "\\"}, "\n\t\t\tж\n\t\t\tツ\n\t\t\t\\", 3)
	f(t, []string{"line one.\n", "\tline two.\n", "line three."},
		"line one.\n\t\t\t\t\tline two.\n\t\t\t\tline three.", 4)

	t.Run("break", func(t *testing.T) {
		var r []string
		for s := range parser.IterateBlockStringLines([]byte("foo\nbar"), 0) {
			r = append(r, string(s))
			break
		}
		if !slices.Equal([]string{"foo\n"}, r) {
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
		if !slices.Equal([]string{"foo\n"}, r) {
			t.Errorf("expected only foo, received: %#v", r)
		}
	})
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
}

// TestHPrefInStringValue makes sure none of the parser.HPref separators
// can appear in string values without resulting in ErrUnexpectedToken
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
