package parser_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/parser"
)

// NoopHash is a no-op hasher for testing purposes.
type NoopHash struct{}

func (NoopHash) Write(d []byte) (int, error) { return len(d), nil }
func (NoopHash) Reset()                      { panic("not expected to be called.") }
func (NoopHash) Sum([]byte) []byte           { panic("not expected to be called.") }

var _ parser.Hash = NoopHash{}

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
		err := parser.ReadDocument(NoopHash{}, []byte(input))
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
		suffix, err := parser.ReadDefinition(NoopHash{}, []byte(input))
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
		suffix, err := parser.ReadSelectionSet(NoopHash{}, []byte(input))
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
		a, suffix, err := parser.ReadArguments(NoopHash{}, []byte(input))
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
		a, suffix, err := parser.ReadDirectives(NoopHash{}, []byte(input))
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
		raw, valueType, suffix, err := parser.ReadValue(NoopHash{}, []byte(s))
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

		f(t, `""`, parser.ValueTypeString, suffix, nil, `""`+suffix)
		f(t, `"\""`, parser.ValueTypeString, suffix, nil, `"\""`+suffix)
		f(t, `"\\"`, parser.ValueTypeString, suffix, nil, `"\\"`+suffix)
		f(t, `"\b"`, parser.ValueTypeString, suffix, nil, `"\b"`+suffix)
		f(t, `"\f"`, parser.ValueTypeString, suffix, nil, `"\f"`+suffix)
		f(t, `"\n"`, parser.ValueTypeString, suffix, nil, `"\n"`+suffix)
		f(t, `"\r"`, parser.ValueTypeString, suffix, nil, `"\r"`+suffix)
		f(t, `"\t"`, parser.ValueTypeString, suffix, nil, `"\t"`+suffix)
		f(t, `"\uabcd"`, parser.ValueTypeString, suffix, nil, `"\uabcd"`+suffix)
		f(t, `"\uABCD"`, parser.ValueTypeString, suffix, nil, `"\uABCD"`+suffix)
		f(t, `"\u1234"`, parser.ValueTypeString, suffix, nil, `"\u1234"`+suffix)
		f(t, `"\u5678"`, parser.ValueTypeString, suffix, nil, `"\u5678"`+suffix)
		f(t, `"\u90aA"`, parser.ValueTypeString, suffix, nil, `"\u90aA"`+suffix)
		f(t, `"\u3053\u3093\u306b\u3061\u306f"`, parser.ValueTypeString, suffix, nil,
			`"\u3053\u3093\u306b\u3061\u306f"`+suffix)
		f(t, `"ok"`, parser.ValueTypeString, suffix, nil, `"ok"`+suffix)
		f(t, `"one two\t\nthree 123"`, parser.ValueTypeString, suffix, nil,
			`"one two\t\nthree 123"`+suffix)
		f(t, `"ツ"`, parser.ValueTypeString, suffix, nil,
			`"ツ"`+suffix)
		f(t, `"ツ\n"`, parser.ValueTypeString, suffix, nil,
			`"ツ\n"`+suffix)
		f(t, `"ツ ёж ïх жэ こんにちは\n"`, parser.ValueTypeString, suffix, nil,
			`"ツ ёж ïх жэ こんにちは\n"`+suffix)
	}

	{ // Block strings (https://spec.graphql.org/October2021/#sec-String-Value).
		f(t, ``, parser.ValueTypeStringBlock, `uGGGG"""`+suffix, parser.ErrUnexpectedToken,
			`"""\uGGGG"""`+suffix)
		f(t, "", parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF, `"""\"""`)
		f(t, "", parser.ValueTypeStringBlock, "", parser.ErrUnexpectedEOF, `"""`)

		f(t, `""""""`, parser.ValueTypeStringBlock, suffix, nil,
			`""""""`+suffix)
		f(t, `"""\""""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\""""`+suffix)
		f(t, `"""\\"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\\"""`+suffix)
		f(t, `"""\b"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\b"""`+suffix)
		f(t, `"""\f"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\f"""`+suffix)
		f(t, `"""\n"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\n"""`+suffix)
		f(t, `"""\r"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\r"""`+suffix)
		f(t, `"""\t"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\t"""`+suffix)
		f(t, `"""\uabcd"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\uabcd"""`+suffix)
		f(t, `"""\uABCD"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\uABCD"""`+suffix)
		f(t, `"""\u1234"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u1234"""`+suffix)
		f(t, `"""\u5678"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u5678"""`+suffix)
		f(t, `"""\u90aA"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u90aA"""`+suffix)
		f(t, `"""\u3053\u3093\u306b\u3061\u306f"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""\u3053\u3093\u306b\u3061\u306f"""`+suffix)
		f(t, `"""ok"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ok"""`+suffix)
		f(t, `"""one two\t\nthree 123"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""one two\t\nthree 123"""`+suffix)
		f(t, `"""ツ"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ"""`+suffix)
		f(t, `"""ツ\n"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ\n"""`+suffix)
		f(t, `"""ツ ёж ïх жэ こんにちは\n"""`, parser.ValueTypeStringBlock, suffix, nil,
			`"""ツ ёж ïх жэ こんにちは\n"""`+suffix)
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

var testUnexpectedEOF = []string{
	"",
	"{",
	"query",
	"mutation",
	"subscription",
	"fragment",
	"fragment F",
	"fragment F on",
	"fragment F on X",
	"fragment F on X @",
	"fragment F on X @dir",
	"fragment F on X @dir {",
	"query Foo",
	"query Foo (",
	"query Foo ($",
	"query Foo ($v",
	"query Foo ($v:",
	"query Foo ($v:T",
	"query Foo ($v:T@",
	"query Foo ($v:T@dir",
	"query Foo ($v:T@dir(",
	"query Foo ($v:T@dir(x",
	"query Foo ($v:T@dir(x:",
	"query Foo ($v:T=",
	`query Foo ($v:T="`,
	`query Foo ($v:T="\`,
	`query Foo ($v:T="\u`,
	`query Foo ($v:T=""`,
	`query Foo ($v:T="""`,
	`query Foo ($v:T="""\`,
	`query Foo ($v:T="""\u`,
	"query Foo ($v:T=[",
	"query Foo ($v:T=[1",
	"query Foo ($v:T={",
	"query Foo ($v:T={x",
	"query Foo ($v:T={x:",
	"query Foo ($v:T={x:1",
	"query Foo ($v:T=-",
	"query Foo ($v:T=12",
	"query Foo ($v:T=12.",
	"query Foo ($v:T=12.3e",
	"query Foo ($v:T=12.3E",
	"query Foo ($v:T=12.3E+",
	"query Foo ($v:T=12.3E-",
	"query Foo ($v:T=12.3E-4",
	"query Foo ($v:[",
	"query Foo ($v:[T",
	"query Foo ($v:[T]",
	"query Foo ($v:[T]!",
	"query Foo ($v:[T]! $v2",
	"query Foo ($v:[T]!)",
	"query Foo ($v:[T]!) {",
	"{ ",
	"{ foo",
	"{ foo: ",
	"{ foo: bar",
	"{ foo(",
	"{ foo(v",
	"{ foo(v:",
	"{ foo(v:$",
	"{ foo(v:$v",
	"{ foo(v:$v)",
	"{ foo(v:$v) {",
	"{ foo(v:$v) {...",
	"{ foo(v:$v) {...on",
	"{ foo(v:$v) {...on T",
	"{ foo(v:$v) {...T",
	"{ foo @",
	"{ foo @dir",
	"{ foo @dir(",
	"{ foo @dir(x",
	"{ foo @dir(x:",
	"{ foo @dir(x:3",
	"{ foo @dir(x:3)",
}

// TestReadDocumentErrEOF tests all possible EOF situations.
func TestReadDocumentErrEOF(t *testing.T) {
	for _, s := range testUnexpectedEOF {
		t.Helper()
		if err := parser.ReadDocument(NoopHash{}, []byte(s)); err == nil {
			t.Errorf("expected ErrUnexpectedEOF")
		} else if !errors.Is(err, parser.ErrUnexpectedEOF) {
			t.Errorf("expected ErrUnexpectedEOF; received: %v", err)
		}

		// The queries that end with a string with an unfinished escape sequence
		// should would produce ErrUnexpectedToken, skip those.
		if strings.HasSuffix(s, `"\`) ||
			strings.HasSuffix(s, `"\u`) ||
			strings.HasSuffix(s, `"""\`) ||
			strings.HasSuffix(s, `"""\u`) {
			continue
		}

		err := parser.ReadDocument(NoopHash{}, []byte(s+"\n"))
		if !errors.Is(err, parser.ErrUnexpectedEOF) {
			t.Errorf("(with ignorable suffix) expected EOF error; received: %v", err)
		}
	}
}

var testErrUnexpectedToken = []string{
	"?",
	"{?",
	"{x?",
	"{x:?",
	"{x: ?",
	"{x:y}?",
	"query?",
	"query ?",
	"mutation ?",
	"subscription ?",
	"fragment on",
	"fragment ?",
	"fragment F?",
	"fragment F ?",
	"fragment F on?",
	"fragment F on T?",
	"fragment F on [",
	"fragment F on T @?",
	"fragment F on T @dir?",
	"fragment F on T @dir(?",
	"query Foo?",
	"query Foo(?",
	"query Foo($?",
	"query Foo($d?",
	"query Foo($d:?",
	"query Foo($d:[?",
	"query Foo($d:[T?",
	"query Foo($d:[T]@?",
	"query Foo($d:[T]@dir?",
	"query Foo($d:[T]@dir(?",
	"query Foo($d:[T]@dir(x?",
	"query Foo($d:[T]@dir(x:?",
	"query Foo($d:[T]?",
	"query Foo($d:[T]!?",
	"query Foo($d:[T]!=?",
	"query Foo($d:[T]=2?",
	`query Foo($d:[T]="\?`,
	`query Foo($d:[T]="""\?`,
	// `query Foo($d:[T]="\u?`, // This Produces ErrUnexpectedEOF
	// `query Foo($d:[T]="""\u?`, // This Produces ErrUnexpectedEOF
	"query Foo @?",
	"query Foo @dir?",
	"query Foo @dir(?",
	"query Foo {?",
	"query Foo {f?",
	"query Foo {...?",
	"query Foo {...[",
	"query Foo {...T?",
	"query Foo {...T@?",
	"query Foo {...T@dir?",
	"query Foo {...T@dir(?",
	"query Foo {...T!?",
	"query Foo {...on?",
	"query Foo {...on ?",
	"query Foo {...on T?",
	"query Foo {...on T@?",
	"query Foo {...on T@dir?",
	"query Foo {...on T@dir(?",
	"query Foo {...@?",
	"query Foo {...@dir?",
	"query Foo {...@dir(?",
	"query Foo {...@dir(x?",
	"query Foo {...@dir(x:?",
	"query Foo {...@dir(x:-?",
	"query Foo {...@dir(x:-1?",
	"query Foo {...@dir(x:-1.?",
	"query Foo {...@dir(x:-1.2?",
	"query Foo {...@dir(x:-1.2?",
	"query Foo {...@dir(x:-1.e",
	"query Foo {...@dir(x:-1.E",
	"query Foo {...@dir(x:-1.2e?",
	"query Foo {...@dir(x:-1.2e-?",
	"query Foo {...@dir(x:-1.2e-4?",
	"query Foo {...@dir(x:[?",
	"query Foo {...@dir(x:{?",
	"query Foo {...@dir(x:{y?",
	"query Foo {...@dir(x:{y:?",
	"query Foo {...@dir(x:{y:{?",
}

// TestReadDocumentErrUnexpectedToken tests all possible unexpected token situations.
func TestReadDocumentErrUnexpectedToken(t *testing.T) {
	for _, s := range testErrUnexpectedToken {
		err := parser.ReadDocument(NoopHash{}, []byte(s))
		if !errors.Is(err, parser.ErrUnexpectedToken) {
			t.Errorf("expected ErrUnexpectedToken; received: %v (input: %q)", err, s)
		}
	}
}
