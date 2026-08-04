package parser

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"
)

var (
	ErrUnexpectedEOF   = errors.New("unexpected EOF")
	ErrUnexpectedToken = errors.New("unexpected token")

	// ErrUnexpectedVariable is a variable usage where the grammar asks for a
	// Value[Const]: the default value or a directive argument of a variable definition.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#VariableDefinition
	ErrUnexpectedVariable = fmt.Errorf("%w: variable in constant value",
		ErrUnexpectedToken)

	// ErrTooDeep is a document nesting deeper than [Options.DepthLimit] allows.
	ErrTooDeep = errors.New("too deep")

	// ErrInvalidEscape is a broken escape sequence in a string value:
	// an unknown escape character, a bad hexadecimal digit,
	// or a Unicode escape that is no scalar value.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#EscapedCharacter
	//   - https://spec.graphql.org/September2025/#EscapedUnicode
	ErrInvalidEscape = fmt.Errorf("%w: invalid escape sequence",
		ErrUnexpectedToken)

	// ErrMalformedNumber is an IntValue or FloatValue that breaks a lexical
	// rule: a leading zero, a '-' or a fraction or exponent without digits,
	// or a digit, '.' or NameStart right after the number.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#IntValue
	//   - https://spec.graphql.org/September2025/#FloatValue
	ErrMalformedNumber = fmt.Errorf("%w: malformed number", ErrUnexpectedToken)

	// ErrMalformedUTF8 is a byte sequence that is no UTF-8 encoded Unicode
	// scalar value and hence no SourceCharacter.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#SourceCharacter
	ErrMalformedUTF8 = fmt.Errorf("%w: malformed UTF-8", ErrUnexpectedToken)

	// ErrUnescapedControlChar is a control character written as it is in a single-line
	// string, where it needs an escape sequence. A block string takes it unescaped.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#StringCharacter
	ErrUnescapedControlChar = fmt.Errorf("%w: unescaped control character",
		ErrUnexpectedToken)
)

// Result says where in the document parsing stopped. Its zero value means no
// error, so callers check [Result.Err], nil when nothing failed.
//
// [Result.Err] is an error like any other:
// pass it to [errors.Is], or wrap it with %w where a failure travels on.
type Result struct {
	// Err is nil when there's no error. Otherwise it's [ErrUnexpectedEOF],
	// [ErrUnexpectedToken], one of the errors wrapping [ErrUnexpectedToken],
	// [ErrTooDeep], or the error of the [io.Writer].
	Err error

	// ErrOffset is the byte index where parsing stopped,
	// and -1 where there is no position, which is the error of an [io.Writer].
	// Offset 0 is the first byte of the document, so it can't stand for "no position".
	//
	// [Position] turns it into a line and a column.
	ErrOffset int
}

// IsErr returns true if the result is an error.
func (e Result) IsErr() bool { return e.Err != nil }

// String reads the value, so %v carries the offset, which [Result.Err] alone doesn't.
// The zero value names itself: an empty string would leave a hole where a log
// line expects a value, and read the same as a failure with no message.
func (e Result) String() string {
	switch {
	case e.Err == nil:
		return "no error"
	case e.ErrOffset < 0:
		return e.Err.Error()
	}
	return fmt.Sprintf("%v (offset %d)", e.Err, e.ErrOffset)
}

// errResult is the Result for err at offset, where the state machine stopped.
func errResult(src string, offset int, err error) Result {
	return Result{Err: err, ErrOffset: min(max(offset, 0), len(src))}
}

// Position returns the 1-based line and column of offset in s.
// A column counts characters, not bytes: a malformed byte counts as one,
// as it does for the parser. A negative offset,
// which is what a [Result] without a position carries, returns 0, 0.
//
// It scans s up to offset, so call it where a message is formatted,
// not on the path that rejects a document.
// Reference:
//
//   - https://spec.graphql.org/September2025/#LineTerminator
func Position[S string | []byte](s S, offset int) (line, column int) {
	if offset < 0 {
		return 0, 0
	}
	src := asString(s)
	head := src[:min(offset, len(src))]

	// CRLF is one LineTerminator, so a pair counts once.
	line = 1 + strings.Count(head, "\n") + strings.Count(head, "\r") -
		strings.Count(head, "\r\n")

	// A column counts the characters after the last LineTerminator.
	lineStart := max(
		strings.LastIndexByte(head, '\n'), strings.LastIndexByte(head, '\r'),
	) + 1
	return line, utf8.RuneCountInString(head[lineStart:]) + 1
}

// The hash prefixes introduce the tokens of the canonical form.
// Without them two tokens collapse into one: the fields of `{ foo bar }` would
// produce the bytes of the single field `{ foobar }`.
//
// Requirement: no prefix may be 0x9, 0xA or 0xD. Those are valid bytes within a
// string value (https://spec.graphql.org/September2025/#SourceCharacter).
const (
	HPrefQuery                 byte = 0x1
	HPrefMutation              byte = 0x2
	HPrefSubscription          byte = 0x3
	HPrefFragmentDefinition    byte = 0x4
	HPrefVariableDefinition    byte = 0x5
	HPrefDirective             byte = 0x6
	HPrefField                 byte = 0x7
	HPrefType                  byte = 0x8
	HPrefFieldAliasedName      byte = 0xb // The actual name of the aliased field.
	HPrefFragmentSpread        byte = 0xc
	HPrefInlineFragment        byte = 0xe
	HPrefArgument              byte = 0xf
	HPrefSelectionSet          byte = 0x11
	HPrefSelectionSetEnd       byte = 0x12
	HPrefValueInputObject      byte = 0x13
	HPrefValueInputObjectField byte = 0x14
	HPrefInputObjectEnd        byte = 0x15
	HPrefValueNull             byte = 0x16
	HPrefValueTrue             byte = 0x17
	HPrefValueFalse            byte = 0x18
	HPrefValueInteger          byte = 0x19
	HPrefValueFloat            byte = 0x1a
	HPrefValueEnum             byte = 0x1b
	HPrefValueString           byte = 0x1c
	HPrefValueList             byte = 0x1d
	HPrefValueListEnd          byte = 0x1e
	HPrefValueVariable         byte = 0x1f
)

// Ignore says how much of the input a document is hashed without.
// The values are ordered: each one leaves out what the one before it leaves out,
// and more.
type Ignore uint8

const (
	// IgnoreNothing keeps every input value and every variable.
	// Only formatting, comments and descriptions are left out.
	IgnoreNothing Ignore = iota

	// IgnoreInputs leaves out every argument value: literals, lists,
	// input objects and variable usages alike. The argument name is kept,
	// so `f(x: 1)` and `f` still differ, and so is the variable signature.
	//
	// These 3 queries produce the same hash:
	//
	//	{ user(id: 1, role: ADMIN) { name } }
	//	{ user(id: 42, role: GUEST) { name } }
	//	{ user(id: "42", role: GUEST) { name } }
	//
	// This one differs, because it declares a variable:
	//
	//	query($id: ID) { user(id: $id, role: GUEST) { name } }
	IgnoreInputs

	// IgnoreVariables leaves out what [IgnoreInputs] leaves out and the variable
	// definitions on top of that, so nothing of a variable is hashed:
	// neither the signature nor a usage.
	//
	// These 3 queries produce the same hash:
	//
	//	query Q($x: Int = 1) { f(a: $x) }
	//	query Q($y: String) { f(a: $y) }
	//	query Q { f(a: 1) }
	//
	// and a parameterized operation matches its unparameterized form:
	//
	//	query Q($x: Int) { f(x: $x) }
	//	query Q { f(x: 1) }
	IgnoreVariables
)

type Options struct {
	// Ignore is how much of the input to leave out. The zero value is [IgnoreNothing].
	Ignore Ignore

	// DepthLimit is how deeply selection sets, list values and input object
	// values may nest before a document is rejected with [ErrTooDeep].
	// Below 1 takes [DefaultDepthLimit]: no value turns the limit off.
	//
	// Nesting is what a document grows cheaply, so this bounds what one costs.
	DepthLimit int
}

// Default sizes a [Parser] starts at, see [NewParser].
const (
	// DefaultBufferSize is how many bytes of canonical form a fresh parser holds.
	// It's a starting size and no limit.
	DefaultBufferSize = 4096

	// DefaultDepthLimit is the nesting an [Options] with no DepthLimit takes.
	//
	// Past what a document written for an API reaches — a Relay-style query
	// costs two levels per page, a filter of nested input objects a few more —
	// and far below what a document costs to attack with.
	DefaultDepthLimit = 128

	// maxRetainedBufferSize is the largest buffer a parser keeps between calls,
	// so one oversized document doesn't leave it holding an oversized buffer.
	maxRetainedBufferSize = 1 << 20
)

// state holds the buffers a [Parser] reuses across calls.
// Not generic: the state machine works on a string, whatever the input type.
type state struct {
	// buf holds the canonical token stream until it's written out.
	buf []byte

	// stack holds one frame per ListValue and InputObjectValue currently open.
	stack []byte
}

func newState(bufferSize int) *state {
	if bufferSize < 1 {
		bufferSize = DefaultBufferSize
	}
	return &state{
		buf: make([]byte, 0, bufferSize),
		// The depth limit caps the stack, so the default one never grows it.
		stack: make([]byte, 0, DefaultDepthLimit),
	}
}

var pool = sync.Pool{New: func() any {
	return newState(DefaultBufferSize)
}}

// Parse reads a Document, which is one or many ExecutableDefinitions,
// and writes its canonical form to w, applying options.
//
// w receives the canonical form in a single Write,
// and nothing at all for a document that turns out to be invalid.
//
// The returned [Result] is the zero value if s is a valid document, and carries
// the error of w if the write failed. Parse never resets w,
// so several documents can be written into one sum.
//
// A named type such as json.RawMessage takes a conversion,
// which copies nothing: Parse(w, options, []byte(raw)).
//
// Reuse a [Parser] where this is called per request, it can be more efficient.
//
// Reference:
//
//   - https://spec.graphql.org/September2025/#Document
//   - https://spec.graphql.org/September2025/#ExecutableDefinition
func Parse[S string | []byte](w io.Writer, options Options, s S) Result {
	p := pool.Get().(*state)
	err := parse(p, w, options, asString(s))
	pool.Put(p)
	return err
}

// Parser is a reusable parser instance. Reusing one is more efficient than
// calling [Parse].
//
// WARNING: A Parser is not safe for concurrent use.
type Parser[S string | []byte] struct{ s *state }

// NewParser creates a new reusable parser instance.
//
// bufferSize is the buffer's starting size; below 1 takes [DefaultBufferSize].
func NewParser[S string | []byte](bufferSize int) *Parser[S] {
	return &Parser[S]{s: newState(bufferSize)}
}

// Parse is identical to the [Parse] function.
func (p *Parser[S]) Parse(w io.Writer, options Options, s S) Result {
	return parse(p.s, w, options, asString(s))
}

// asString views s as a string without copying it. The state machine only reads
// the source and keeps no reference, so the view doesn't outlive the call.
// The return after the switch is unreachable: the constraint admits no third type.
func asString[S string | []byte](s S) string {
	switch v := any(s).(type) {
	case string:
		return v
	case []byte:
		if len(v) == 0 {
			return ""
		}
		return unsafe.String(unsafe.SliceData(v), len(v))
	}
	return string(s)
}
