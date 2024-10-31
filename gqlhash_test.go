package gqlhash_test

import (
	"crypto/sha1"
	"fmt"
	"slices"
	"testing"

	"github.com/romshark/gqlhash"
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
		Name: "mutation with args",
		Inputs: []string{
			`mutation GQL { addStandard ( name : "GraphQL" )  }`,
			`mutation GQL{addStandard(name:"GraphQL")}`,
		},
		ExpectRecords: MakeRecords(
			parser.HPrefMutation,
			"GQL",
			parser.HPrefSelectionSet,
			parser.HPrefField, "addStandard",
			parser.HPrefArgument, "name",
			parser.HPrefValueString, `"GraphQL"`,
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
			parser.HPrefValueString, `"жツ"`,
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
	}

	f(t, nil, `{foo bar}`, `{foo bar}`)

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

const benchSchema = `
type Query {
	x: I
}

interface I {
	a: String
	bar: String
	bazz: String
	i: String
	f: Int
}

type X implements I {
	a: String
	bar: String
	bazz: String
	i: String
	f: Int
}

type A implements I {
	a: String
	bar: String
	bazz: String
	i: String
	f: Int
	withArgs(x: WithArgsInput): String
}

input WithArgsInput {
	quiteALongArgumentName: String
	unicode: String
	escapedUnicodeBlockString: String
}

directive @dir on FRAGMENT_DEFINITION
`

const benchQuery = `
	query {  # First comment.
		x {
			bar
			bazz
			... on A {
				a # Second comment.
				withArgs(x: {
					quiteALongArgumentName: "foo bar bazz"
					unicode: "こんにちは"
					escapedUnicodeBlockString: """\u3053\u3093\u306b\u3061\u306f"""
				})
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
	}
`

const benchQueryMinified = `{x{bar,bazz,...on A{a,withArgs(x:{` +
	`quiteALongArgumentName:"foo bar bazz",unicode:"こんにちは",` +
	`escapedUnicodeBlockString:"""\u3053\u3093\u306b\u3061\u306f"""})},` +
	`...F,...@include(if:true){i}}},fragment F on X@dir{f}`

func BenchmarkCompare(b *testing.B) {
	varA := []byte(benchQuery)
	varB := []byte(benchQueryMinified)
	h := sha1.New()
	b.ResetTimer()

	b.Run("alloc_buffer", func(b *testing.B) {
		for range b.N {
			if err := gqlhash.Compare(h, varA, varB); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("reuse_buffer", func(b *testing.B) {
		buf := make([]byte, 0, h.Size()*2)
		for range b.N {
			if err := gqlhash.CompareWithBuffer(buf, h, varA, varB); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkReferenceSHA1(b *testing.B) {
	// Prepare vektah schema
	schema, err := vektah.LoadSchema(&ast.Source{Input: benchSchema})
	if err != nil {
		b.Fatalf("parsing schema: %v", err)
	}
	if _, errs := vektah.LoadQuery(schema, benchQuery); errs != nil {
		b.Fatalf("parsing query: %v", errs)
	}

	q := []byte(benchQuery)
	hashBuffer := make([]byte, 64)
	h := sha1.New()
	b.ResetTimer()

	b.Run("direct", func(b *testing.B) {
		for range b.N {
			hashBuffer = hashBuffer[:0]
			h.Reset()
			_, _ = h.Write(q)
			hashBuffer = h.Sum(hashBuffer)
		}
	})

	b.Run("gqlhash", func(b *testing.B) {
		for range b.N {
			hashBuffer = hashBuffer[:0]
			gqlhash.AppendQueryHash(hashBuffer, h, q)
		}
	})

	b.Run("vektah", func(b *testing.B) {
		for range b.N {
			if _, errs := vektah.LoadQuery(schema, benchQuery); errs != nil {
				b.Fatal(errs)
			}
		}
	})
}
