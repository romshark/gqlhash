package gqlhash_test

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	_ "embed"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"hash/fnv"
	"os"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/blake2s"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal"
	"github.com/romshark/gqlhash/v2/parser"

	vektah "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// bom is the UTF-8 encoding of U+FEFF, which is Ignored
// (https://spec.graphql.org/September2025/#UnicodeBOM).
const bom = "\xef\xbb\xbf"

var _ gqlhash.Hash = internal.NoopHash{}

// fuzzSeeds are documents covering every kind of token. TestParseCanonicalStream
// of the parser package asserts the canonical form of each one.
var fuzzSeeds = []string{
	"{foo}", "{ foo }", "query { foo }",
	"{foo bar}", "{ foo  bar }", "query{foo,bar}",
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
	// Indented, empty blank middle line.
	`{ f(x: """` + "\n    a\n\n    b\n    " + `""") }`,
	// Already dedented.
	`{ f(x: """` + "\na\n\nb\n" + `""") }`,
	// Indented, blank middle line filled with spaces.
	`{ f(x: """` + "\n    a\n    \n    b\n    " + `""") }`,
	// Extra leading blank line is trimmed like the first one.
	`{ f(x: """` + "\n\n    a\n\n    b\n    " + `""") }`,
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
	`query Q($x:[Int!]!){f}`,
	`query Q ( $x : [ Int ! ] ! ) { f }`,
	"query Q($x: [\n\t# comment\n\tInt !,\n] !) { f }",
	`query Q($x: ` + bom + `[` + bom + `Int` + bom +
		`!` + bom + `]` + bom + `!` + bom + `) { f }`,
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
	"{x(f:3.14)}", "{ x ( f : 3.14 ) }",
}

// TestInputTypes asserts that the functions take a document as a string and as a
// []byte, and that both produce the same hash.
func TestInputTypes(t *testing.T) {
	const a, b = `{ foo bar }`, "{\n\tfoo\n\tbar\n}"

	if err := gqlhash.Compare(sha1.New(), gqlhash.Options{}, a, b); err.Err != nil {
		t.Errorf("string: %v", err)
	}
	if err := gqlhash.Compare(
		sha1.New(), gqlhash.Options{}, []byte(a), []byte(b),
	); err.Err != nil {
		t.Errorf("[]byte: %v", err)
	}

	sumString, err := gqlhash.AppendQueryHash(nil, sha1.New(), gqlhash.Options{}, a)
	if err.Err != nil {
		t.Fatalf("string: %v", err)
	}
	sumBytes, err := gqlhash.AppendQueryHash(
		nil, sha1.New(), gqlhash.Options{}, []byte(a),
	)
	if err.Err != nil {
		t.Fatalf("[]byte: %v", err)
	}
	if string(sumString) != string(sumBytes) {
		t.Error("a string and a []byte document must hash alike")
	}
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

// TestPosition covers the [gqlhash.Position] wrapper.
func TestPosition(t *testing.T) {
	const src = "query Q {\n  f(a: 01)\n}"
	_, err := gqlhash.AppendQueryHash(nil, sha1.New(), gqlhash.Options{}, src)
	if !err.IsErr() {
		t.Fatal("expected an error")
	}
	if line, column := gqlhash.Position(src, err.Offset); line != 2 || column != 9 {
		t.Errorf("expected line 2, column 9; received line %d, column %d",
			line, column)
	}
	// A hash mismatch has no position.
	if line, column := gqlhash.Position(src, -1); line != 0 || column != 0 {
		t.Errorf("expected no position; received line %d, column %d", line, column)
	}
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
	ignore := gqlhash.Options{Ignore: gqlhash.IgnoreInputs}
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
			nil, sha1.New(), gqlhash.Options{Ignore: gqlhash.IgnoreInputs}, s,
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

//go:embed "testdata/nesting-attack.graphql"
var benchQueryNestingAttack string

//go:embed "testdata/nesting-attack.min.graphql"
var benchQueryNestingAttackMinified string

var benchQueries = []struct {
	Name      string
	Schema    string
	Formatted string
	Minified  string

	// SchemaInvalid excludes the query from everything that involves the
	// schema. An adversarial document doesn't respect a schema, and a
	// hash-based firewall has to reject it before validation gets to see it,
	// which is exactly why hashing it must stay cheap.
	SchemaInvalid bool
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
	{
		// Deeply nested selection sets, inline fragments, list values, input
		// object values and list types.
		Name:          "nesting_attack",
		Schema:        benchSchema,
		Formatted:     benchQueryNestingAttack,
		Minified:      benchQueryNestingAttackMinified,
		SchemaInvalid: true,
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
			if !q.SchemaInvalid {
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

// ignoreModes are the [gqlhash.Ignore] values, named for a benchmark row.
var ignoreModes = []struct {
	name   string
	ignore gqlhash.Ignore
}{
	{"nothing", gqlhash.IgnoreNothing},
	{"inputs", gqlhash.IgnoreInputs},
	{"variables", gqlhash.IgnoreVariables},
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

				// One row per ignore mode. The wider modes write fewer bytes,
				// so they must not be slower than gqlhash/nothing.
				for _, m := range ignoreModes {
					b.Run(name+"/gqlhash/"+m.name, func(b *testing.B) {
						o := gqlhash.Options{Ignore: m.ignore}
						for range b.N {
							hashBuffer = hashBuffer[:0]
							var err gqlhash.Error
							hashBuffer, err = gqlhash.AppendQueryHash(
								hashBuffer, h, o, inputBytes,
							)
							if err.IsErr() {
								b.Fatal(err)
							}
						}
					})
				}

				if q.SchemaInvalid {
					return
				}
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
	for _, q := range fuzzSeeds {
		f.Add(q)
	}
	// Inputs exercising variables, defaults and directives on definitions.
	f.Add(`query Q($x: Int = 1 @dep, $y: [String!]) { f(a: $x, b: [$y, 2]) }`)
	f.Add(`query ($v: ID = "x") { f(id: $v, list: {nested: $v}) }`)

	// All option modes share the same parsing logic, so none may panic and they
	// must agree on whether the input is valid (only the hashing differs).
	opts := []parser.Options{
		{Ignore: parser.IgnoreNothing},
		{Ignore: parser.IgnoreInputs},
		{Ignore: parser.IgnoreVariables},
	}
	f.Fuzz(func(t *testing.T, a string) {
		in := []byte(a)
		h := internal.NoopHash{}

		// Public wrappers must not panic.
		_, _ = gqlhash.AppendQueryHash(nil, h, gqlhash.Options{}, in)
		_, _ = gqlhash.AppendQueryHash(
			nil, h, gqlhash.Options{Ignore: gqlhash.IgnoreInputs}, in,
		)

		var first error
		for i, o := range opts {
			err := parser.Parse(h, o, in)
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

// benchHashFunctions are the hash functions the CLI offers, in the order of its
// -hash documentation. The constructors are written out here rather than taken
// from the command packages, so the library's benchmarks depend on nothing but
// the library.
var benchHashFunctions = []struct {
	Name string
	New  func() hash.Hash
}{
	{"sha1", sha1.New},
	{"sha2", sha256.New},
	{"sha3", func() hash.Hash { return sha3.New512() }},
	{"md5", md5.New},
	{"blake2b", func() hash.Hash {
		h, err := blake2b.New256(nil)
		if err != nil {
			panic(err)
		}
		return h
	}},
	{"blake2s", func() hash.Hash {
		h, err := blake2s.New256(nil)
		if err != nil {
			panic(err)
		}
		return h
	}},
	{"blake3", func() hash.Hash { return blake3.New() }},
	{"fnv", func() hash.Hash { return fnv.New64() }},
	{"fnv1a", func() hash.Hash { return fnv.New64a() }},
	{"xxh64", func() hash.Hash { return xxhash.New() }},
	{"crc32", func() hash.Hash { return crc32.NewIEEE() }},
	{"crc64", func() hash.Hash { return crc64.New(crc64.MakeTable(crc64.ISO)) }},
}

// BenchmarkHashFunctions measures hashing one document with each hash function.
// The parsing is identical across them and dominates the total, so the
// differences are small. [BenchmarkHashFunctionsRaw] is the same set without the
// parsing, which is what makes the overhead of gqlhash readable.
func BenchmarkHashFunctions(b *testing.B) {
	query, err := os.ReadFile("testdata/big.graphql")
	if err != nil {
		b.Fatal(err)
	}

	for _, f := range benchHashFunctions {
		b.Run(f.Name, func(b *testing.B) {
			h := f.New()
			buf := make([]byte, 0, h.Size())
			b.SetBytes(int64(len(query)))
			b.ReportAllocs()

			for b.Loop() {
				// errHash is no error: assigning it to one would make it
				// non-nil even when there's no error.
				var errHash gqlhash.Error
				buf, errHash = gqlhash.AppendQueryHash(
					buf[:0], h, gqlhash.Options{}, query,
				)
				if errHash.IsErr() {
					b.Fatal(errHash)
				}
			}
		})
	}
}

// BenchmarkHashFunctionsRaw measures each hash function on its own, over a buffer
// the size of the document [BenchmarkHashFunctions] uses.
func BenchmarkHashFunctionsRaw(b *testing.B) {
	query, err := os.ReadFile("testdata/big.graphql")
	if err != nil {
		b.Fatal(err)
	}

	for _, f := range benchHashFunctions {
		b.Run(f.Name, func(b *testing.B) {
			h := f.New()
			buf := make([]byte, 0, h.Size())
			b.SetBytes(int64(len(query)))
			b.ReportAllocs()

			for b.Loop() {
				h.Reset()
				_, _ = h.Write(query)
				buf = h.Sum(buf[:0])
			}
		})
	}
}
