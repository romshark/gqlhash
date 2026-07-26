package parser_test

import (
	"os"
	"testing"

	"github.com/romshark/gqlhash/internal"
	"github.com/romshark/gqlhash/parser"
)

// benchDocuments are the documents of testdata, formatted and minified.
var benchDocuments = []string{
	"medium.graphql", "medium.min.graphql", "big.graphql", "big.min.graphql",
}

func readTestdata(tb testing.TB, name string) string {
	tb.Helper()
	b, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// BenchmarkParse measures the parser itself: [internal.NoopHash] keeps the cost
// of the hash function out of the numbers.
func BenchmarkParse(b *testing.B) {
	for _, name := range benchDocuments {
		src := readTestdata(b, name)
		b.Run(name, func(b *testing.B) {
			p, h, o := parser.NewParser[string](0, 0), internal.NoopHash{}, parser.Options{}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if e := p.Parse(h, o, src); e.Err != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// BenchmarkParseBytes is [BenchmarkParse] over a []byte input, which the parser
// views as a string instead of instantiating a second state machine for.
func BenchmarkParseBytes(b *testing.B) {
	for _, name := range benchDocuments {
		src := []byte(readTestdata(b, name))
		b.Run(name, func(b *testing.B) {
			p, h, o := parser.NewParser[[]byte](0, 0), internal.NoopHash{}, parser.Options{}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if e := p.Parse(h, o, src); e.Err != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// BenchmarkParsePooled measures the package-level function, which takes its
// buffers from a global pool instead of a reused [parser.Parser].
func BenchmarkParsePooled(b *testing.B) {
	for _, name := range benchDocuments {
		src := readTestdata(b, name)
		b.Run(name, func(b *testing.B) {
			h, o := internal.NoopHash{}, parser.Options{}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if e := parser.Parse(h, o, src); e.Err != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// BenchmarkParseOptions compares the hashing option modes. The structure modes
// hash fewer bytes (the skipped input values), so they must not be slower.
func BenchmarkParseOptions(b *testing.B) {
	modes := []struct {
		name string
		o    parser.Options
	}{
		{"full", parser.Options{}},
		{"ignore_inputs", parser.Options{IgnoreInputs: true}},
		{"ignore_variables", parser.Options{IgnoreVariables: true}},
	}
	src := readTestdata(b, "big.graphql")
	for _, m := range modes {
		b.Run(m.name, func(b *testing.B) {
			p, h := parser.NewParser[string](0, 0), internal.NoopHash{}
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if e := p.Parse(h, m.o, src); e.Err != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
