package gqlhash_test

import (
	"crypto/sha1"
	_ "embed"
	"fmt"
	"slices"
	"testing"

	"github.com/romshark/gqlhash"
	"github.com/romshark/gqlhash/internal"
	"github.com/romshark/gqlhash/parser"

	vektah "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// bom is the UTF-8 encoding of U+FEFF, which is Ignored
// (https://spec.graphql.org/September2025/#UnicodeBOM).
const bom = "\xef\xbb\xbf"

// MockHash is a mock hasher that's recording all writes for testing purposes.
type MockHash struct{ Records []string }

func (m *MockHash) Write(data []byte) (int, error) {
	m.Records = append(m.Records, string(data))
	return len(data), nil
}

func (m *MockHash) Reset() {
	m.Records = m.Records[:0]
}

func (m *MockHash) Sum(b []byte) []byte {
	h := sha1.New()
	for _, s := range m.Records {
		_, _ = h.Write([]byte(s))
	}
	return h.Sum(b)
}

var _ parser.Hash = new(MockHash)

type HashTest struct {
	Name          string
	Inputs        []string
	ExpectRecords []string
}

var hashTests = []HashTest{
	{
		Name: "anonymous one field",
		Inputs: []string{
			"{foo}", "{ foo }", "query { foo }",
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "foo",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "anonymous two fields",
		Inputs: []string{
			"{foo bar}", "{ foo  bar }", "query{foo,bar}",
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "foo",
			parser.HPrefField, "bar",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "block strings",
		Inputs: []string{
			`mutation GQL { addStandard ( name : "GraphQL" description:"""
				line one.
					line two.
				line three.
			""")  }`,
			`mutation GQL { addStandard ( name : "GraphQL" description:"""line one.
					line two.
				line three.""")  }`,
			`mutation GQL{addStandard(name:"GraphQL",description:"""line one.
					line two.
				line three.""")}`,
			`mutation GQL{addStandard(name:"GraphQL",description:"""
	line one.
		line two.
	line three.""")}`,
			`mutation GQL{addStandard(name:"GraphQL",description:"""
  line one.
  	line two.
  line three.""")}`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefMutation,
			"GQL",
			parser.HPrefSelectionSet,
			parser.HPrefField, "addStandard",
			parser.HPrefArgument, "name",
			parser.HPrefValueString, "GraphQL",
			parser.HPrefArgument, "description",
			// Block string lines are written individually and joined by a line
			// feed, so that LF, CRLF and CR sources all hash alike.
			parser.HPrefValueString,
			"line one.", "\n", "\tline two.", "\n", "line three.",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		// A blank middle line must not defeat dedentation: all three inputs
		// encode the same block string value "a\n\nb".
		Name: "block string blank line dedent",
		Inputs: []string{
			// Indented, empty blank middle line.
			`{ f(x: """` + "\n    a\n\n    b\n    " + `""") }`,
			// Already dedented.
			`{ f(x: """` + "\na\n\nb\n" + `""") }`,
			// Indented, blank middle line filled with spaces.
			`{ f(x: """` + "\n    a\n    \n    b\n    " + `""") }`,
			// Extra leading blank line is trimmed like the first one.
			`{ f(x: """` + "\n\n    a\n\n    b\n    " + `""") }`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "f",
			parser.HPrefArgument, "x",
			// The blank middle line is yielded as an empty line between two
			// line feed separators.
			parser.HPrefValueString, "a", "\n", "", "\n", "b",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "subscription with vars",
		Inputs: []string{
			`subscription Updates (
				$x : T = "жツ"
			) @ ok  {
				updates (
					channel : $x,
					limit : 5,
				) {
					id
				}
			}`,
			`subscription Updates($x:T="жツ") @ok{updates(channel:$x limit:5){id}}`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefSubscription,
			"Updates",
			parser.HPrefVariableDefinition, "x",
			parser.HPrefType, "T",
			parser.HPrefValueString, "жツ",
			parser.HPrefDirective, "ok",
			parser.HPrefSelectionSet,
			parser.HPrefField, "updates",
			parser.HPrefArgument, "channel",
			parser.HPrefValueVariable, `x`,
			parser.HPrefArgument, "limit",
			parser.HPrefValueInteger, `5`,
			parser.HPrefSelectionSet,
			parser.HPrefField, "id",
			parser.HPrefSelectionSetEnd,
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		// Only the canonical type is written, so the Ignored tokens that may
		// appear between the tokens of a type reference don't change the hash
		// (https://spec.graphql.org/September2025/#Type).
		Name: "variable type formatting",
		Inputs: []string{
			`query Q($x:[Int!]!){f}`,
			`query Q ( $x : [ Int ! ] ! ) { f }`,
			"query Q($x: [\n\t# comment\n\tInt !,\n] !) { f }",
			`query Q($x: ` + bom + `[` + bom + `Int` + bom +
				`!` + bom + `]` + bom + `!` + bom + `) { f }`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			"Q",
			parser.HPrefVariableDefinition, "x",
			parser.HPrefType, "[", "Int", "!", "]", "!",
			parser.HPrefSelectionSet,
			parser.HPrefField, "f",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "directives with vals",
		Inputs: []string{
			`{
				x @ translate (
					lang : {
						codes : [
							EN
							DE
							FR
							IT
						] 
					}
				)
			}`,
			`{x @translate(lang:{codes:[EN,DE,FR,IT]})}`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "x",
			parser.HPrefDirective, "translate",
			parser.HPrefArgument, "lang",
			parser.HPrefValueInputObject,
			parser.HPrefValueInputObjectField, "codes",
			parser.HPrefValueList,
			parser.HPrefValueEnum, "EN",
			parser.HPrefValueEnum, "DE",
			parser.HPrefValueEnum, "FR",
			parser.HPrefValueEnum, "IT",
			parser.HPrefValueListEnd,
			parser.HPrefInputObjectEnd,
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "spreads and inline fragments",
		Inputs: []string{
			`query {  # First comment.
				x {
					... on A {
						a # Second comment.
					}
					...F
					... @ include ( if : true ) {
						i
					}
				}
			}
			# Third comment.
			fragment F on X @dir {
				f
			}`,
			`{x{...on A{a},...F,...@include(if:true){i}}},fragment F on X@dir{f}`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "x",
			parser.HPrefSelectionSet,
			parser.HPrefInlineFragment,
			parser.HPrefType, "A",
			parser.HPrefSelectionSet,
			parser.HPrefField, "a",
			parser.HPrefSelectionSetEnd,
			parser.HPrefFragmentSpread, "F",
			parser.HPrefInlineFragment,
			parser.HPrefDirective, "include",
			parser.HPrefArgument, "if",
			parser.HPrefValueTrue,
			parser.HPrefSelectionSet,
			parser.HPrefField, "i",
			parser.HPrefSelectionSetEnd,
			parser.HPrefSelectionSetEnd,
			parser.HPrefSelectionSetEnd,
			parser.HPrefFragmentDefinition, "F",
			parser.HPrefType, "X",
			parser.HPrefDirective, "dir",
			parser.HPrefSelectionSet,
			parser.HPrefField, "f",
			parser.HPrefSelectionSetEnd,
		),
	},
	{
		Name: "float value",
		Inputs: []string{
			"{x(f:3.14)}", "{ x ( f : 3.14 ) }",
		},
		ExpectRecords: MakeRecords(
			parser.HPrefQuery,
			parser.HPrefSelectionSet,
			parser.HPrefField, "x",
			parser.HPrefArgument, "f",
			parser.HPrefValueFloat, "3.14",
			parser.HPrefSelectionSetEnd,
		),
	},
}

func TestHash(t *testing.T) {
	for _, set := range hashTests {
		t.Run(set.Name, func(t *testing.T) {
			h := new(MockHash)
			for _, input := range set.Inputs {
				if _, err := gqlhash.AppendQueryHash(
					nil, h, gqlhash.Options{}, []byte(input),
				); err.Err != nil {
					t.Errorf("unexpected error: %v; input: %q", err, input)
				}
				if slices.Compare(set.ExpectRecords, h.Records) != 0 {
					t.Errorf("expected:\n%v;\nreceived:\n%v; input: %q",
						set.ExpectRecords, h.Records, input)
				}
			}
		})
	}
}

func MakeRecords(v ...any) []string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprintf("%s", x)
	}
	return s
}

func TestCompare(t *testing.T) {
	f := func(t *testing.T, expect error, a, b string) {
		t.Helper()
		received := gqlhash.Compare(sha1.New(), gqlhash.Options{}, []byte(a), []byte(b))
		if expect != received.Err {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide nil buffer.
		received = gqlhash.CompareWithBuffer(
			nil, sha1.New(), gqlhash.Options{}, []byte(a), []byte(b),
		)
		if expect != received.Err {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide buffer that's too small in len.
		received = gqlhash.CompareWithBuffer(
			make([]byte, 1), sha1.New(), gqlhash.Options{}, []byte(a), []byte(b),
		)
		if expect != received.Err {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide buffer with len 0 and some capacity.
		received = gqlhash.CompareWithBuffer(
			make([]byte, 0, 1), sha1.New(), gqlhash.Options{}, []byte(a), []byte(b),
		)
		if expect != received.Err {
			t.Errorf("expected %v; received: %v", expect, received)
		}
	}

	f(t, nil, `{foo bar}`, `{foo bar}`)
	f(t, nil, `
		# comment
		{ foo, bar }
	`, `{foo bar}`)
	f(t, gqlhash.ErrQueriesDiffer, `{foo bar}`, `{foobar}`)

	// CR and CRLF are LineTerminators just like LF, and BlockStringValue
	// normalizes all three to LF, so a block string written with Windows or
	// classic-Mac line endings must hash like its LF equivalent
	// (https://spec.graphql.org/September2025/#BlockStringValue()).
	f(t, nil,
		`{f(s:"""`+"line1\r\nline2"+`""")}`,
		`{f(s:"""`+"line1\nline2"+`""")}`)
	f(t, nil,
		`{f(s:"""`+"line1\rline2"+`""")}`,
		`{f(s:"""`+"line1\nline2"+`""")}`)
	// Terminators may be mixed within one block string.
	f(t, nil,
		`{f(s:"""`+"a\r\nb\rc\nd"+`""")}`,
		`{f(s:"""`+"a\nb\nc\nd"+`""")}`)

	// CRLF is a single LineTerminator, not two, so it must not introduce a
	// blank line. This guards a fix that treats CR and LF independently.
	f(t, gqlhash.ErrQueriesDiffer,
		`{f(s:"""`+"a\r\nb"+`""")}`,
		`{f(s:"""`+"a\n\nb"+`""")}`)

	// Common indentation is stripped per line, so it must be recognized after
	// any LineTerminator, not just after LF.
	f(t, nil,
		`{f(s:"""`+"\r\n  line1\r\n  line2\r\n  "+`""")}`,
		`{f(s:"""`+"\n  line1\n  line2\n  "+`""")}`)
	// Leading and trailing blank lines are dropped regardless of terminator.
	f(t, nil, `{f(s:"""`+"\r\nfoo\r\n\r\n"+`""")}`, `{f(s:"""foo""")}`)
	f(t, nil, `{f(s:"""`+"foo\r\r"+`""")}`, `{f(s:"""foo""")}`)

	// Accepting CR must not weaken the rest of the control-character rule:
	// a single-line string may not contain a LineTerminator at all
	// (https://spec.graphql.org/September2025/#StringCharacter).
	f(t, gqlhash.ErrUnescapedControlChar, `{f(s:"`+"a\rb"+`")}`, `{f(s:"ab")}`)
	f(t, gqlhash.ErrUnescapedControlChar, `{f(s:"`+"a\nb"+`")}`, `{f(s:"ab")}`)
	// A block string may hold a control scalar value, so these parse and differ
	// (https://spec.graphql.org/September2025/#BlockStringCharacter).
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"""`+"a\x00b"+`""")}`, `{f(s:"""ab""")}`)
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"""`+"a\x07b"+`""")}`, `{f(s:"""ab""")}`)

	// A string is hashed by its value, not by how it's written
	// (https://spec.graphql.org/September2025/#sec-String-Value).

	// Escape sequences count in a normal string but not in a block string, so
	// a line feed and the two characters `\` and `n` are different values.
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"\n")}`, `{f(s:"""\n""")}`)
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"\t")}`, `{f(s:"""\t""")}`)
	f(t, nil, `{f(s:"\u0041")}`, `{f(s:"""A""")}`)

	// Different spellings of the same normal string are equal.
	f(t, nil, `{f(s:"a")}`, `{f(s:"\u0061")}`)
	f(t, nil, `{f(s:"ab")}`, `{f(s:"\u0061\u0062")}`)
	f(t, nil, `{f(s:"a/b")}`, `{f(s:"a\/b")}`)
	f(t, nil, `{f(s:"💩")}`, `{f(s:"\u{1F4A9}")}`)
	// A surrogate pair spells out one code point, not two characters.
	f(t, nil, `{f(s:"💩")}`, `{f(s:"\uD83D\uDCA9")}`)
	f(t, nil, `{f(s:"\u{1F4A9}")}`, `{f(s:"\uD83D\uDCA9")}`)
	// An escape sequence isn't the character it's spelled with.
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"\b")}`, `{f(s:"b")}`)
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"\f")}`, `{f(s:"f")}`)
	f(t, gqlhash.ErrQueriesDiffer, `{f(s:"\r")}`, `{f(s:"\n")}`)

	// A block string evaluates `\"""` to `"""` and leaves everything else as is.
	f(t, nil, `{f(s:"a\"\"\"b")}`, `{f(s:"""a\"""b""")}`)
	f(t, nil, `{f(s:"a\\b")}`, `{f(s:"""a\b""")}`)
	f(t, nil, `{f(s:"a\\nb")}`, `{f(s:"""a\nb""")}`)

	// A normal and a block string holding the same value are equal.
	f(t, nil, `{f(s:"a\nb")}`, `{f(s:"""a`+"\n"+`b""")}`)
	f(t, nil, `{f(s:"a\"b")}`, `{f(s:"""a"b""")}`)
	f(t, nil, `{f(s:"\tx")}`, `{f(s:"""`+"\tx"+`""")}`)

	// An escaped byte must not be able to imitate a hash prefix: 0x07 is
	// [parser.HPrefField], so the string below would otherwise collide with an
	// empty string followed by the field x.
	f(t, gqlhash.ErrQueriesDiffer, `{f(a:"\u0007x")}`, `{f(a:"") x}`)
	// A block string holds those bytes raw, which must not collide either.
	f(t, gqlhash.ErrQueriesDiffer, `{f(a:"""`+"\x07x"+`""")}`, `{f(a:"""""") x}`)
	f(t, gqlhash.ErrQueriesDiffer,
		`{f(a:"""`+"\x11\x07x\x12"+`""")}`, `{f(a:""""""){x}}`)
	// 0x11 opens and 0x12 closes a selection set.
	f(t, gqlhash.ErrQueriesDiffer,
		`{f(a:"\u0011\u0007x\u0012")}`, `{f(a:""){x}}`)

	// A description is documentation and isn't hashed, so documents differing
	// only in their descriptions are equal
	// (https://spec.graphql.org/September2025/#sec-Descriptions).
	f(t, nil, `"A" query Q { f }`, `query Q { f }`)
	f(t, nil, `"A" query Q { f }`, `"B" query Q { f }`)
	f(t, nil, `"""A""" query Q { f }`, `query Q { f }`)
	f(t, nil,
		`"A" fragment F on T { f } query Q { ...F }`,
		`fragment F on T { f } query Q { ...F }`)
	f(t, nil, `query Q("A" $x: Int) { f }`, `query Q($x: Int) { f }`)
	f(t, nil, `query Q("A" $x: Int) { f }`, `query Q("B" $x: Int) { f }`)

	// A '$' not followed by a Name leaves the variable definition list unclosed,
	// so the document is invalid and must be rejected rather than hashed. It
	// otherwise collides with the valid operation that declares no variables.
	f(t, gqlhash.ErrUnexpectedToken, `query($ {f}`, `query {f}`)
	f(t, gqlhash.ErrUnexpectedToken, `query($@dir {f}`, `query @dir {f}`)
}

func TestCompareErr(t *testing.T) {
	received := gqlhash.Compare(sha1.New(), gqlhash.Options{}, []byte(``), []byte(`{x}`))
	if received.Err != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
	}

	received = gqlhash.Compare(sha1.New(), gqlhash.Options{}, []byte(`{x}`), []byte(``))
	if received.Err != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
	}
}

func TestCompareOptions(t *testing.T) {
	ignore := gqlhash.Options{IgnoreInputs: true}
	f := func(t *testing.T, expect error, a, b string) {
		t.Helper()
		if got := gqlhash.Compare(
			sha1.New(), ignore, []byte(a), []byte(b),
		); got.Err != expect {
			t.Errorf("expected %v; received %v", expect, got)
		}
		// Reused buffer must agree.
		buf := make([]byte, 0, sha1.Size*2)
		got := gqlhash.CompareWithBuffer(buf, sha1.New(), ignore, []byte(a), []byte(b))
		if got.Err != expect {
			t.Errorf("buffered: expected %v; received %v", expect, got)
		}
	}

	// Same structure, different input values => equal.
	f(t, nil,
		`{ user(id: 1, name: "alice", role: ADMIN) { id posts(first: 10) { t } } }`,
		`{ user(id: 9, name: "bob", role: GUEST) { id posts(first: 99) { t } } }`)

	// Formatting/comment differences are ignored as usual.
	f(t, nil,
		"{ user(id: 1) { id } }",
		"{\n\tuser(id: 999) {\n\t\t# comment\n\t\tid\n\t}\n}")

	// Different structure => differ.
	f(t, gqlhash.ErrQueriesDiffer,
		`{ user(id: 1) { id name } }`,
		`{ user(id: 1) { id email } }`)
	f(t, gqlhash.ErrQueriesDiffer,
		`{ user(id: 1) { id name } }`,
		`{ user(id: 1) { name id } }`)

	// Value types collapse too: `1` and `"1"` are both ignored inputs.
	f(t, nil, `{ f(x: 1) }`, `{ f(x: "1") }`)
	// A variable usage is ignored like any other value.
	f(t, nil, `{ f(x: $v) }`, `{ f(x: 1) }`)
	// The variable signature is kept, though: declaring a variable differs.
	f(t, gqlhash.ErrQueriesDiffer, `query ($v: ID) { f(x: $v) }`, `{ f(x: 1) }`)

	// Syntax errors still propagate.
	f(t, gqlhash.ErrUnexpectedEOF, ``, `{x}`)
}

func TestAppendQueryHashOptions(t *testing.T) {
	// Full hash distinguishes values; structure hash does not.
	a := []byte(`{ f(x: 1, y: "a") }`)
	b := []byte(`{ f(x: 2, y: "b") }`)

	full := func(s []byte) string {
		h, err := gqlhash.AppendQueryHash(nil, sha1.New(), gqlhash.Options{}, s)
		if err.Err != nil {
			t.Fatal(err)
		}
		return string(h)
	}
	structure := func(s []byte) string {
		h, err := gqlhash.AppendQueryHash(
			nil, sha1.New(), gqlhash.Options{IgnoreInputs: true}, s,
		)
		if err.Err != nil {
			t.Fatal(err)
		}
		return string(h)
	}

	if full(a) == full(b) {
		t.Error("full hashes should differ for differing values")
	}
	if structure(a) != structure(b) {
		t.Error("structure hashes should match for differing values")
	}
	if full(a) == structure(a) {
		t.Error("full and structure hashes should differ when values are present")
	}
}

//go:embed "testdata/schema.graphqls"
var benchSchema string

//go:embed "testdata/medium.graphql"
var benchQueryMedium string

//go:embed "testdata/medium.min.graphql"
var benchQueryMediumMinified string

//go:embed "testdata/big.graphql"
var benchQueryBig string

//go:embed "testdata/big.min.graphql"
var benchQueryBigMinified string

var benchQueries = []struct {
	Name      string
	Schema    string
	Formatted string
	Minified  string
}{
	{
		Name:   "blockstring",
		Schema: benchSchema,
		Formatted: `{x{... on A{
  withArgs(x:{
    escapedUnicodeBlockString: """
      \u3053\u3093\u306b\u3061\u306f
    """
  })
}}}`,
		Minified: `{x{... on A{` +
			`withArgs(x:{escapedUnicodeBlockString: ` +
			`"""\u3053\u3093\u306b\u3061\u306f"""` +
			`})}}}`,
	},
	{
		Name:   "tiny",
		Schema: benchSchema,
		Formatted: `{
			x {
				bar
				bazz
			}
		}`,
		Minified: `{x{bar,bazz}}`,
	},
	{
		Name:      "medium",
		Schema:    benchSchema,
		Formatted: benchQueryMedium,
		Minified:  benchQueryMediumMinified,
	},
	{
		Name:      "big",
		Schema:    benchSchema,
		Formatted: benchQueryBig,
		Minified:  benchQueryBigMinified,
	},
}

func BenchmarkCompare(b *testing.B) {
	for _, q := range benchQueries {
		b.Run(q.Name, func(b *testing.B) {
			varForm, varMin := []byte(q.Formatted), []byte(q.Minified)
			h := sha1.New()
			b.ResetTimer()

			b.Run("alloc_buffer", func(b *testing.B) {
				for range b.N {
					if err := gqlhash.Compare(
						h, gqlhash.Options{}, varForm, varMin,
					); err.Err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("reuse_buffer", func(b *testing.B) {
				buf := make([]byte, 0, h.Size()*2)
				for range b.N {
					err := gqlhash.CompareWithBuffer(buf, h, gqlhash.Options{}, varForm, varMin)
					if err.Err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func TestBenchQueries(t *testing.T) {
	for _, q := range benchQueries {
		t.Run(q.Name, func(t *testing.T) {
			// Prepare vektah schema
			schema, err := vektah.LoadSchema(&ast.Source{Input: q.Schema})
			if err != nil {
				t.Fatalf("parsing schema: %v", err)
			}
			if _, errs := vektah.LoadQueryWithRules(
				schema, q.Formatted, nil,
			); errs != nil {
				t.Errorf("parsing formatted query: %v", errs)
			}
			if _, errs := vektah.LoadQueryWithRules(
				schema, q.Minified, nil,
			); errs != nil {
				t.Errorf("parsing minified query: %v", errs)
			}

			errCmp := gqlhash.Compare(
				sha1.New(), gqlhash.Options{}, []byte(q.Formatted), []byte(q.Minified),
			)
			if errCmp.Err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func BenchmarkReferenceSHA1(b *testing.B) {
	for _, q := range benchQueries {
		b.Run(q.Name, func(b *testing.B) {
			// Prepare vektah schema
			schema, err := vektah.LoadSchema(&ast.Source{Input: q.Schema})
			if err != nil {
				b.Fatalf("parsing schema: %v", err)
			}
			// Construct the validation rules once, mirroring real-world
			// vektah usage instead of rebuilding them on every parse.
			vektahRules := rules.NewDefaultRules()
			hashBuffer := make([]byte, 64)
			h := sha1.New()
			b.ResetTimer()

			run := func(name string, b *testing.B, input string) {
				inputBytes := []byte(input)
				b.Run(name+"/direct", func(b *testing.B) {
					for range b.N {
						hashBuffer = hashBuffer[:0]
						h.Reset()
						_, _ = h.Write(inputBytes)
						hashBuffer = h.Sum(hashBuffer)
					}
				})

				b.Run(name+"/gqlhash", func(b *testing.B) {
					for range b.N {
						hashBuffer = hashBuffer[:0]
						var err gqlhash.Error
						hashBuffer, err = gqlhash.AppendQueryHash(
							hashBuffer, h, gqlhash.Options{}, inputBytes,
						)
						if err.Err != nil {
							b.Fatal(err)
						}
					}
				})

				b.Run(name+"/vektah", func(b *testing.B) {
					for range b.N {
						_, errs := vektah.LoadQueryWithRules(schema, input, vektahRules)
						if errs != nil {
							b.Fatal(errs)
						}
					}
				})
			}

			run("minified", b, q.Minified)
			run("formatted", b, q.Formatted)
		})
	}
}

// BenchmarkOptions compares the hashing option modes. Structure modes hash
// fewer bytes (skipped input values), so they should not be slower than full.
func BenchmarkOptions(b *testing.B) {
	modes := []struct {
		name string
		o    parser.Options
	}{
		{"full", parser.Options{}},
		{"ignore_inputs", parser.Options{IgnoreInputs: true}},
		{"ignore_variables", parser.Options{IgnoreVariables: true}},
	}
	for _, q := range benchQueries {
		in := []byte(q.Formatted)
		for _, m := range modes {
			b.Run(q.Name+"/"+m.name, func(b *testing.B) {
				h := sha1.New()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					h.Reset()
					if err := parser.ReadDocument(h, m.o, in); err.Err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// FuzzHashing makes sure hashing never panics, that all option modes agree on
// an input's validity, and that a valid query never differs from itself.
func FuzzHashing(f *testing.F) {
	// Invalid inputs.
	for _, q := range internal.TestUnexpectedEOF {
		f.Add(q)
	}
	for _, q := range internal.TestErrUnexpectedToken {
		f.Add(q)
	}

	// Valid inputs.
	for _, q := range benchQueries {
		f.Add(q.Formatted)
		f.Add(q.Minified)
	}
	for _, t := range hashTests {
		for _, q := range t.Inputs {
			f.Add(q)
		}
	}
	// Inputs exercising variables, defaults and directives on definitions.
	f.Add(`query Q($x: Int = 1 @dep, $y: [String!]) { f(a: $x, b: [$y, 2]) }`)
	f.Add(`query ($v: ID = "x") { f(id: $v, list: {nested: $v}) }`)

	// All option modes share the same parsing logic, so none may panic and they
	// must agree on whether the input is valid (only the hashing differs).
	opts := []parser.Options{
		{},
		{IgnoreInputs: true},
		{IgnoreVariables: true},
		{IgnoreInputs: true, IgnoreVariables: true},
	}
	f.Fuzz(func(t *testing.T, a string) {
		in := []byte(a)
		h := internal.NoopHash{}

		// Public wrappers must not panic.
		_, _ = gqlhash.AppendQueryHash(nil, h, gqlhash.Options{}, in)
		_, _ = gqlhash.AppendQueryHash(
			nil, h, gqlhash.Options{IgnoreInputs: true}, in,
		)

		var first error
		for i, o := range opts {
			err := parser.ReadDocument(h, o, in)
			if i == 0 {
				first = err
			} else if fmt.Sprint(err) != fmt.Sprint(first) {
				// Compared by message: a [parser.SyntaxError] is a pointer, so
				// two equal errors are still two values.
				t.Fatalf("options %+v returned %v; want %v", o, err, first)
			}
		}

		// A valid query must never differ from itself, for any options. This
		// exercises hashing determinism and buffer reuse and needs a real hash
		// (NoopHash has a constant sum). Skip invalid inputs (first != nil).
		if first == nil {
			sh := sha1.New()
			for _, o := range opts {
				if err := gqlhash.Compare(sh, o, in, in); err.Err != nil {
					t.Fatalf("self-compare with %+v: %v", o, err)
				}
			}
		}
	})
}
