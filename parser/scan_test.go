package parser_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/internal"
	"github.com/romshark/gqlhash/parser"
)

// TestParseScanOffsets exercises the unrolled scanners at every offset a token
// can end at, which is where an off-by-one would hide. Every document below is
// valid, so a misstep of a scanner shows up as a syntax error or a wrong stream.
func TestParseScanOffsets(t *testing.T) {
	for n := 1; n <= 20; n++ {
		name := strings.Repeat("a", n)
		ws := strings.Repeat(" ", n)
		comment := "#" + strings.Repeat("c", n) + "\n"
		text := strings.Repeat("s", n)

		// A Name of this length in every position a Name can appear in, and
		// Ignored tokens of this length between the tokens.
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

		// A malformed byte at this offset of a comment ends the comment, which
		// leaves the byte as an unexpected token.
		input := "{f}#" + strings.Repeat("c", n) + "\xff"
		if _, err := parse(parser.Options{}, input); !errors.Is(
			err.Err, parser.ErrUnexpectedToken,
		) {
			t.Errorf("n=%d: expected an unexpected token; received: %v; input: %q",
				n, err, input)
		}

		// A string value of this length: plain, with a multi-byte character,
		// with an escape sequence and with one that has to be escaped again.
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

// TestParseHugeDocument makes sure a document whose canonical stream outgrows the
// retained buffer size is read correctly and doesn't make the parser hold on to
// an outsized buffer.
func TestParseHugeDocument(t *testing.T) {
	huge := strings.Repeat("x", 2<<20)
	input := `{f(a:"` + huge + `")}`
	expect := stream(
		parser.HPrefQuery, parser.HPrefSelectionSet,
		parser.HPrefField, "f", parser.HPrefArgument, "a",
		parser.HPrefValueString, huge,
		parser.HPrefSelectionSetEnd,
	)

	p := parser.NewParser[string](0, 0)
	for range 2 {
		r := new(recorder)
		if err := p.Parse(r, parser.Options{}, input); err.Err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.String() != expect {
			t.Fatal("stream differs")
		}
	}

	// The same for a document that turns out to be invalid only after its
	// oversized value was written.
	if err := p.Parse(new(recorder), parser.Options{}, input[:len(input)-1]+"?)}"); err.Err ==
		nil {
		t.Error("expected a syntax error")
	}

	// The parser must be usable, and allocation-free, afterwards.
	h := internal.NoopHash{}
	_ = p.Parse(h, parser.Options{}, `{f}`)
	if n := testing.AllocsPerRun(100, func() {
		_ = p.Parse(h, parser.Options{}, `{f}`)
	}); n != 0 {
		t.Errorf("expected no allocations; received %v", n)
	}
}

// TestError covers the [parser.Error] methods.
func TestError(t *testing.T) {
	var zero parser.Error
	if s := zero.Error(); s != "no error" {
		t.Errorf("expected %q; received %q", "no error", s)
	}
	if err := zero.Unwrap(); err != nil {
		t.Errorf("expected nil; received %v", err)
	}

	// An error without a position, like the hash mismatch of the root package.
	noPos := parser.Error{Err: parser.ErrUnexpectedToken}
	if s := noPos.Error(); s != parser.ErrUnexpectedToken.Error() {
		t.Errorf("expected %q; received %q", parser.ErrUnexpectedToken, s)
	}

	_, err := parse(parser.Options{}, "{\n\t?}")
	if s := err.Error(); s != "unexpected token (line 2, column 2)" {
		t.Errorf("unexpected message: %q", s)
	}
	if !errors.Is(err, parser.ErrUnexpectedToken) {
		t.Error("expected errors.Is to match the sentinel")
	}
	if err.Unwrap() != parser.ErrUnexpectedToken {
		t.Error("expected Unwrap to return the sentinel")
	}
}
