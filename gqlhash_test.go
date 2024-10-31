package gqlhash_test

import (
	"crypto/sha1"
	"fmt"
	"slices"
	"testing"

	"github.com/romshark/gqlhash"
	"github.com/romshark/gqlhash/parser"
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
			`query {
				x {
					... on A {
						a
					}
					...F
					... @ include ( if : true ) {
						i
					}
				}
			}
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
				if _, err := gqlhash.AppendQueryHash(nil, h, input); err != nil {
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
		received := gqlhash.Compare(sha1.New(), a, b)
		if expect != received {
			t.Errorf("expected %v; received: %v", expect, received)
		}
	}

	f(t, nil, `{foo bar}`, `{foo bar}`)

	f(t, gqlhash.ErrQueriesDiffer, `{foo bar}`, `{foobar}`)
}

func TestCompareErr(t *testing.T) {
	received := gqlhash.Compare(sha1.New(), ``, `{x}`)
	if received != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
	}

	received = gqlhash.Compare(sha1.New(), `{x}`, ``)
	if received != gqlhash.ErrUnexpectedEOF {
		t.Errorf("expected %v; received: %v", gqlhash.ErrUnexpectedEOF, received)
	}
}
