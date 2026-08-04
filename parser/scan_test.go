package parser_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/v2/parser"
)

// TestParseScanOffsets runs every kind of token at every length the unrolled
// scanners can end a chunk at. Every document below is valid:
// a misstep shows up as a syntax error or a wrong stream.
func TestParseScanOffsets(t *testing.T) {
	for n := 1; n <= 20; n++ {
		name := strings.Repeat("a", n)
		ws := strings.Repeat(" ", n)
		comment := "#" + strings.Repeat("c", n) + "\n"
		text := strings.Repeat("s", n)

		// A Name of this length in every position a Name can appear in,
		// and Ignored tokens of this length between the tokens.
		for _, input := range []string{
			"{" + name + "}",
			"{alias:" + name + "}",
			"{" + name + ":f}",
			"{f(" + name + ":1)}",
			"query " + name + "{f}",
			"query Q($" + name + ":T){f}",
			"query Q($x:" + name + "){f}",
			"query Q($x:[" + name + "!]!){f}",
			"fragment " + name + " on T{f}",
			"fragment F on " + name + "{f}",
			"{..." + name + "}",
			"{...on " + name + "{f}}",
			"{f@" + name + "}",
			"{f(a:" + name + ")}",
			"{f(a:{" + name + ":1})}",
			"{f(a:$" + name + ")}",
			"{f(a:" + strings.Repeat("1", n) + ")}",
			ws + "{" + ws + "f" + ws + "}" + ws,
			"{" + strings.Repeat(",", n) + "f}",
			comment + "{f}" + comment,
			"{f}" + ws + comment,
			"{" + comment + "f" + comment + "}",
			// A multi-byte SourceCharacter at this offset of a comment.
			"#" + strings.Repeat("c", n) + "ツ\n{f}",
		} {
			if _, err := parse(parser.Options{}, input); err.Err != nil {
				t.Errorf("n=%d: unexpected error: %v; input: %q", n, err, input)
			}
		}

		// A malformed byte at this offset ends the comment.
		// The byte is then an unexpected token.
		input := "{f}#" + strings.Repeat("c", n) + "\xff"
		if _, err := parse(parser.Options{}, input); !errors.Is(
			err.Err, parser.ErrUnexpectedToken,
		) {
			t.Errorf("n=%d: expected an unexpected token; received: %v; input: %q",
				n, err, input)
		}

		// A string value of this length: plain, with a multi-byte character, with
		// an escape sequence, and with one that is escaped again on the way out.
		for _, c := range []struct {
			expect string
			inputs []string
		}{
			{text, []string{`"` + text + `"`, `"""` + text + `"""`}},
			{text + "ツ", []string{`"` + text + `ツ"`, `"""` + text + `ツ"""`}},
			{text + "\t", []string{`"` + text + `\t"`, "\"\"\"" + text + "\t\"\"\""}},
			{text + `\@`, []string{
				`"` + text + `\u0000"`,
				`"""` + text + string(rune(0)) + `"""`,
			}},
			{text + "A" + text, []string{
				`"` + text + `A` + text + `"`,
				`"` + text + `A` + text + `"`,
				`"` + text + `\u{41}` + text + `"`,
			}},
		} {
			for _, in := range c.inputs {
				if a := stringValueOf(t, in); a != c.expect {
					t.Errorf("n=%d: expected value %q; received %q; input: %q",
						n, c.expect, a, in)
				}
			}
		}
	}
}

// TestParseHugeDocument reads a document whose canonical form outgrows
// maxRetainedBufferSize. The parser must not hold on to the oversized buffer.
func TestParseHugeDocument(t *testing.T) {
	huge := strings.Repeat("x", 2<<20)
	input := `{f(a:"` + huge + `")}`
	expect := stream(
		parser.HPrefQuery, parser.HPrefSelectionSet,
		parser.HPrefField, "f", parser.HPrefArgument, "a",
		parser.HPrefValueString, huge,
		parser.HPrefSelectionSetEnd,
	)

	p := parser.NewParser[string](0)
	for range 2 {
		r := new(recorder)
		if err := p.Parse(r, parser.Options{}, input); err.Err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.String() != expect {
			t.Fatal("stream differs")
		}
	}

	// The same for a document that turns out to be invalid after its oversized
	// value was written.
	if err := p.Parse(new(recorder), parser.Options{}, input[:len(input)-1]+"?)}"); err.Err ==
		nil {
		t.Error("expected a syntax error")
	}

	// The parser must be usable, and allocation-free, afterwards.
	h := io.Discard
	_ = p.Parse(h, parser.Options{}, `{f}`)
	if n := testing.AllocsPerRun(100, func() {
		_ = p.Parse(h, parser.Options{}, `{f}`)
	}); n != 0 {
		t.Errorf("expected no allocations; received %v", n)
	}
}

// TestResult covers a [parser.Result] as parsing hands it back.
// [TestResultSemantics] covers the type itself.
func TestResult(t *testing.T) {
	_, err := parse(parser.Options{}, "{\n\t?}")
	if !err.IsErr() {
		t.Error("expected an error")
	}
	if s := err.String(); s != "unexpected token (offset 3)" {
		t.Errorf("unexpected message: %q", s)
	}
	if line, column := parser.Position("{\n\t?}", err.ErrOffset); line != 2 || column != 2 {
		t.Errorf("expected line 2, column 2; received line %d, column %d",
			line, column)
	}
	if !errors.Is(err.Err, parser.ErrUnexpectedToken) {
		t.Error("expected errors.Is to match the sentinel")
	}
}

// TestParseNestingAttack reads testdata/nesting-attack.graphql. Nothing grows
// with the nesting but the value stack, one byte per open ListValue or
// InputObjectValue.
func TestParseNestingAttack(t *testing.T) {
	src := readTestdata(t, "nesting-attack.graphql")

	// The default limit rejects it, which is what the limit is for. The rest of
	// this test is about the parser holding up where the limit allows it.
	if e := parser.Parse(io.Discard, parser.Options{}, src); e.Err != parser.ErrTooDeep {
		t.Errorf("expected the default depth limit to reject it; received %v", e)
	}
	noLimit := parser.Options{DepthLimit: 10_000}

	// A parser with the smallest possible buffers must grow into it and agree
	// with a default one.
	want, err := parse(noLimit, src)
	if err.Err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The minified variant is the same document and produces the same form.
	min, errMin := parse(noLimit, readTestdata(t, "nesting-attack.min.graphql"))
	if errMin.Err != nil {
		t.Fatalf("minified: unexpected error: %v", errMin)
	}
	if min != want {
		t.Error("minified variant must produce the same stream")
	}
	r := new(recorder)
	if e := parser.NewParser[string](1).Parse(r, noLimit, src); e.Err != nil {
		t.Fatalf("minimal parser: %v", e)
	}
	if r.String() != want {
		t.Error("minimal parser: stream differs")
	}

	// Reading it twice must not leave anything behind in the reused buffers.
	p := parser.NewParser[string](0)
	for range 2 {
		r := new(recorder)
		if e := p.Parse(r, noLimit, src); e.Err != nil {
			t.Fatalf("unexpected error: %v", e)
		}
		if r.String() != want {
			t.Error("stream differs between runs")
		}
	}
	h := io.Discard
	_ = p.Parse(h, parser.Options{}, src)
	if n := testing.AllocsPerRun(20, func() {
		_ = p.Parse(h, parser.Options{}, src)
	}); n != 0 {
		t.Errorf("expected no allocations; received %v", n)
	}

	// Nesting deeper than a recursive parser survives, where the limit allows it.
	const deep = 100_000
	// The list-in-object case alternates, so it reaches twice the repeat count.
	deepOptions := parser.Options{DepthLimit: 2*deep + 1}
	for _, input := range []string{
		strings.Repeat("{a", deep) + "b" + strings.Repeat("}", deep),
		"{f(a:" + strings.Repeat("[", deep) + "1" + strings.Repeat("]", deep) + ")}",
		"{f(a:" + strings.Repeat("{k:", deep) + "1" + strings.Repeat("}", deep) + ")}",
		"{f(a:" + strings.Repeat("[{k:", deep) + "1" + strings.Repeat("}]", deep) + ")}",
		"query Q($v:" + strings.Repeat("[", deep) + "Int" +
			strings.Repeat("]", deep) + "){f}",
	} {
		if e := p.Parse(io.Discard, deepOptions, input); e.Err != nil {
			t.Errorf("%d levels deep: %v", deep, e)
		}
	}

	// Nesting that never closes is a syntax error, not a hang or a crash.
	for _, input := range []string{
		strings.Repeat("{a", deep),
		"{f(a:" + strings.Repeat("[", deep),
		"{f(a:" + strings.Repeat("{k:", deep),
		"query Q($v:" + strings.Repeat("[", deep),
	} {
		if e := p.Parse(io.Discard, deepOptions, input); e.Err !=
			parser.ErrUnexpectedEOF {
			t.Errorf("expected %v; received: %v", parser.ErrUnexpectedEOF, e)
		}
	}
}
