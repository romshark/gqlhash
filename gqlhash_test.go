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
)

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
			parser.HPrefValueString, "line one.\n", "\tline two.\n", "line three.",
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
}

func TestHash(t *testing.T) {
	for _, set := range hashTests {
		t.Run(set.Name, func(t *testing.T) {
			h := new(MockHash)
			for _, input := range set.Inputs {
				if _, err := gqlhash.AppendQueryHash(nil, h, []byte(input)); err != nil {
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
		received := gqlhash.Compare(sha1.New(), []byte(a), []byte(b))
		if expect != received {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide nil buffer.
		received = gqlhash.CompareWithBuffer(nil, sha1.New(), []byte(a), []byte(b))
		if expect != received {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide buffer that's too small in len.
		received = gqlhash.CompareWithBuffer(
			make([]byte, 1), sha1.New(), []byte(a), []byte(b),
		)
		if expect != received {
			t.Errorf("expected %v; received: %v", expect, received)
		}

		// Provide buffer with len 0 and some capacity.
		received = gqlhash.CompareWithBuffer(
			make([]byte, 0, 1), sha1.New(), []byte(a), []byte(b),
		)
		if expect != received {
			t.Errorf("expected %v; received: %v", expect, received)
		}
	}

	f(t, nil, `{foo bar}`, `{foo bar}`)
	f(t, nil, `
		# comment
		{ foo, bar }
	`, `{foo bar}`)
	f(t, gqlhash.ErrQueriesDiffer, `{foo bar}`, `{foobar}`)
}

func TestCompareErr(t *testing.T) {
	received := gqlhash.Compare(sha1.New(), []byte(``), []byte(`{x}`))
	if received != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
	}

	received = gqlhash.Compare(sha1.New(), []byte(`{x}`), []byte(``))
	if received != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
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
					if err := gqlhash.Compare(h, varForm, varMin); err != nil {
						b.Fatal(err)
					}
				}
			})

			b.Run("reuse_buffer", func(b *testing.B) {
				buf := make([]byte, 0, h.Size()*2)
				for range b.N {
					err := gqlhash.CompareWithBuffer(buf, h, varForm, varMin)
					if err != nil {
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
			// fmt.Printf("FORMATTED: %q", q.Formatted)
			if _, errs := vektah.LoadQuery(schema, q.Formatted); errs != nil {
				t.Errorf("parsing formatted query: %v", errs)
			}
			if _, errs := vektah.LoadQuery(schema, q.Minified); errs != nil {
				t.Errorf("parsing minified query: %v", errs)
			}

			err = gqlhash.Compare(sha1.New(), []byte(q.Formatted), []byte(q.Minified))
			if err != nil {
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
						var err error
						hashBuffer, err = gqlhash.AppendQueryHash(
							hashBuffer, h, inputBytes,
						)
						if err != nil {
							b.Fatal(err)
						}
					}
				})

				b.Run(name+"/vektah", func(b *testing.B) {
					for range b.N {
						_, errs := vektah.LoadQuery(schema, input)
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

// FuzzAppendQueryHash makes sure AppendQueryHash never panics.
func FuzzAppendQueryHash(f *testing.F) {
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

	f.Fuzz(func(t *testing.T, a string) {
		_, _ = gqlhash.AppendQueryHash(nil, internal.NoopHash{}, []byte(a))
	})
}
