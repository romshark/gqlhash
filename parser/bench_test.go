package parser_test

import (
	"io"
	"os"
	"testing"

	"github.com/romshark/gqlhash/v2/parser"
)

// benchDocuments are the documents of testdata: formatted, minified and nested.
var benchDocuments = []string{
	"medium.graphql", "medium.min.graphql", "big.graphql", "big.min.graphql",
	"nesting-attack.graphql", "nesting-attack.min.graphql",
}

func readTestdata(tb testing.TB, name string) string {
	tb.Helper()
	b, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	return string(b)
}

// BenchmarkParse measures the parser without a hash function: [io.Discard] takes
// the writes.
func BenchmarkParse(b *testing.B) {
	for _, name := range benchDocuments {
		src := readTestdata(b, name)
		b.Run(name, func(b *testing.B) {
			p, h, o := parser.NewParser[string](0), io.Discard, parser.Options{}
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
// views as a string.
func BenchmarkParseBytes(b *testing.B) {
	for _, name := range benchDocuments {
		src := []byte(readTestdata(b, name))
		b.Run(name, func(b *testing.B) {
			p, h, o := parser.NewParser[[]byte](0), io.Discard, parser.Options{}
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
			h, o := io.Discard, parser.Options{}
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

// BenchmarkParseOptions compares the option modes. The ignoring modes write
// fewer bytes and must not be slower.
func BenchmarkParseOptions(b *testing.B) {
	modes := []struct {
		name string
		o    parser.Options
	}{
		{"full", parser.Options{}},
		{"ignore_inputs", parser.Options{Ignore: parser.IgnoreInputs}},
		{"ignore_variables", parser.Options{Ignore: parser.IgnoreVariables}},
	}
	src := readTestdata(b, "big.graphql")
	for _, m := range modes {
		b.Run(m.name, func(b *testing.B) {
			p, h := parser.NewParser[string](0), io.Discard
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

// escapeHeavy is a document whose strings are nothing but escapes,
// a surrogate pair among them. The documents of testdata don't reach that path:
// what looks like an escape in big.graphql sits inside a block string,
// which takes its characters raw.
const escapeHeavy = `{f(a:"` +
	`\u3053\u3093\u306b\u3061\u306f\ud83d\udca9\u0041` + `",` +
	`b:"` + `\u00e9\u3053\ud83d\udca9\u0041\u0042\u0043` + `")}`

// BenchmarkParseEscapes measures the escape decoder,
// which the documents of testdata leave untouched.
func BenchmarkParseEscapes(b *testing.B) {
	p := parser.NewParser[string](0)
	b.SetBytes(int64(len(escapeHeavy)))
	b.ReportAllocs()
	for b.Loop() {
		if e := p.Parse(io.Discard, parser.Options{}, escapeHeavy); e.Err != nil {
			b.Fatal(e)
		}
	}
}
