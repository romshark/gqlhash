// Package gqlhashtest holds what the tests of this module read from: documents
// that must be rejected, and a hash that does nothing.
//
// It's a package rather than an export_test.go because two packages read it,
// gqlhash and parser, and neither is the other's test. Nothing outside a test
// imports it, so none of this reaches a binary.
package gqlhashtest

// NoopHash is a hasher that discards its input. Its digest is constant.
//
// It's what pins the narrow [gqlhash.Hash] interface: no Size, no BlockSize,
// and it still goes everywhere a hasher goes.
type NoopHash struct{}

func (NoopHash) Write(d []byte) (int, error) { return len(d), nil }
func (NoopHash) Reset()                      { /* No-op */ }

// Sum appends the constant digest to b, as [hash.Hash] requires.
func (NoopHash) Sum(b []byte) []byte { return append(b, "mock-hash-sum"...) }

func (NoopHash) Size() int { return len("mock-hash-sum") }

// UnexpectedEOF are documents that end where more is required. Every one of
// them must be answered with [parser.ErrUnexpectedEOF], whatever reads it.
var UnexpectedEOF = []string{
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
	"query Foo ($v:T=12",
	// An incomplete fraction or exponent ("12.", "12e", "12.3E+") is an unexpected
	// token and not EOF, so those live in [UnexpectedToken].
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

// UnexpectedToken are documents holding a token the grammar doesn't allow
// there. Every one of them must be answered with [parser.ErrUnexpectedToken].
var UnexpectedToken = []string{
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
	`query Foo ($s:ID="` + "\u0000",
	`query Foo ($s:ID="` + "\u0001",
	`query Foo ($s:ID="` + "\u000b",
	// `query Foo($d:[T]="\u?`, // This Produces [parser.ErrUnexpectedEOF]
	// A variable definition whose name is missing or malformed.
	// A '$' takes a Name (https://spec.graphql.org/September2025/#Variable),
	// so the definition list is never closed and the document is
	// invalid whatever follows.
	"query($ {f}",
	"query($@dir {f}",
	"query($ @dir(x:1) {f}",
	"query Q($ {f}",
	"mutation($ {f}",
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
	"query Foo {...@dir(x:-1e?",
	"query Foo {...@dir(x:-1E?",
	"query Foo {...@dir(x:-1.e",
	"query Foo {...@dir(x:-1.E",
	"query Foo {...@dir(x:-1.2e?",
	"query Foo {...@dir(x:-1.2e-?",
	"query Foo {...@dir(x:-1.2e-4?",
	// A variable definition's default value and directives
	// take Value[Const], which has no variable usages
	// (https://spec.graphql.org/September2025/#VariableDefinition).
	"query Q($x:Int=$y){f}",
	"query Q($x:Int=[$y]){f}",
	"query Q($x:Int@d(a:$y)){f}",
	// A description is only allowed on an operation with an OperationType,
	// on a fragment and on a variable definition, and only one per definition
	// (https://spec.graphql.org/September2025/#sec-Descriptions).
	`"A" { f }`,
	`"A" "B" query Q { f }`,
	`query Q("A" "B" $x: Int) { f }`,
	// Broken numbers. Each of these is one invalid number, not two values
	// (https://spec.graphql.org/September2025/#sec-Int-Value).
	"{f(a:01)}",
	"{f(a:-01)}",
	"{f(a:-.1)}",
	"{f(a:0x123)}",
	"{f(a:123L)}",
	"{f(a:1e2foo)}",
	"{f(a:- foo)}",
	"{f(a:1.2.3)}",
	// A '-' takes a digit, so a '-' alone is an unexpected token even at EOF,
	// like a fraction or exponent without digits.
	"query Foo ($v:T=-",
	// Incomplete fraction or exponent at EOF: a digit is required.
	"query Foo ($v:T=12e",
	"query Foo ($v:T=12E",
	"query Foo ($v:T=12.3e",
	"query Foo ($v:T=12.3E",
	"query Foo ($v:T=12.3E+",
	"query Foo ($v:T=12.3E-",
	"query Foo {...@dir(x:[?",
	"query Foo {...@dir(x:{?",
	"query Foo {...@dir(x:{y?",
	"query Foo {...@dir(x:{y:?",
	"query Foo {...@dir(x:{y:{?",
}
