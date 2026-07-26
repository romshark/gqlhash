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
	// Value[Const]: the default value or a directive argument of a variable
	// definition. It wraps [ErrUnexpectedToken].
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#VariableDefinition
	ErrUnexpectedVariable = fmt.Errorf("%w: variable in constant value",
		ErrUnexpectedToken)

	// ErrInvalidEscape is a broken escape sequence in a string value: an
	// unknown escape character, a bad hexadecimal digit, or a Unicode escape
	// that is no scalar value.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#EscapedCharacter
	//   - https://spec.graphql.org/September2025/#EscapedUnicode
	ErrInvalidEscape = fmt.Errorf("%w: invalid escape sequence",
		ErrUnexpectedToken)

	// ErrMalformedNumber is an IntValue or FloatValue that breaks a lexical
	// rule: a leading zero, a '-' or a fraction or exponent without digits, or
	// a digit, '.' or NameStart right after the number.
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

	// ErrUnescapedControlChar is a control character written as it is in a
	// single-line string, where it needs an escape sequence. A block string
	// takes it unescaped.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#StringCharacter
	ErrUnescapedControlChar = fmt.Errorf("%w: unescaped control character",
		ErrUnexpectedToken)
)

// Error says where in the document parsing stopped. Its zero value means no
// error, so callers check [Error.Err] instead of comparing to nil.
//
// Why a value and no error: putting it into an error allocates on every rejected
// document.
type Error struct {
	// Err is nil when there's no error. Otherwise it's [ErrUnexpectedEOF],
	// [ErrUnexpectedToken], one of the errors wrapping [ErrUnexpectedToken], or
	// the error of the [io.Writer].
	Err error

	// Offset is the byte index into the document where parsing stopped, and -1
	// where there is no position, which is the error of an [io.Writer]. Offset 0
	// is the first byte of the document, so it can't stand for "no position".
	//
	// [Position] turns it into a line and a column.
	Offset int
}

// IsErr returns true if e holds an error.
func (e Error) IsErr() bool { return e.Err != nil }

func (e Error) Error() string {
	switch {
	case e.Err == nil:
		return "no error"
	case e.Offset < 0:
		return e.Err.Error()
	}
	return fmt.Sprintf("%v (offset %d)", e.Err, e.Offset)
}

func (e Error) Unwrap() error { return e.Err }

// newError points err at offset, the position where the state machine stopped.
func newError(src string, offset int, err error) Error {
	return Error{Err: err, Offset: min(max(offset, 0), len(src))}
}

// Position returns the 1-based line and column of offset in s. A column counts
// characters, not bytes: a malformed byte counts as one, as it does for the
// parser. It returns 0, 0 for a negative offset, which is what an [Error] carries
// where there is no position.
//
// Why not a method of [Error]: a line and a column are presentation, and finding
// them scans the document up to offset. That belongs where the message is
// formatted, not on the path that rejects a document.
// Reference:
//
//   - https://spec.graphql.org/September2025/#LineTerminator
func Position[S ~string | ~[]byte](s S, offset int) (line, column int) {
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
//
// Why: without them two tokens collapse into one, and the fields of
// `{ foo bar }` would produce the bytes of the single field `{ foobar }`.
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

// Options configures what the canonical form leaves out.
type Options struct {
	// IgnoreInputs produces the same hash for two documents that differ only in
	// their input values. Every argument value (literals, lists, input objects
	// and variable usages alike) is ignored; only the variable signature (the
	// definitions in the operation) is kept.
	//
	// For example, these 3 queries produce the same hash:
	//
	//	{ user(id: 1, role: ADMIN) { name } }
	//	{ user(id: 42, role: GUEST) { name } }
	//	{ user(id: "42", role: GUEST) { name } }
	//
	// The following query differs though, because it declares a variable:
	//
	//	query($id: ID) { user(id: $id, role: GUEST) { name } }
	//
	// IgnoreInputs is a subset of [Options.IgnoreVariables].
	IgnoreInputs bool

	// IgnoreVariables skips hashing variables entirely: both variable definitions and
	// variable usages. Two documents that differ only in their variable
	// definitions and references then produce the same hash.
	//
	// [Options.IgnoreInputs] already ignores variable usages; IgnoreVariables
	// additionally ignores the variable definitions (the signature).
	//
	// For example, these two queries produce the same hash:
	//
	//	query Q($x: Int = 1) { f(a: $x) }
	//	query Q($y: String) { f(a: $y) }
	//	query Q { f(a: 1) }
	//
	// and a parameterized operation matches its unparameterized form:
	//
	//	query Q($x: Int) { f }
	//	query Q { f }
	//
	// IgnoreVariables is a superset of [Options.IgnoreInputs].
	IgnoreVariables bool
}

// Default sizes of the reusable buffers of a [Parser].
const (
	// DefaultBufferSize is the initial size of the buffer the canonical token
	// stream is assembled in before it's handed to the [io.Writer]. The buffer
	// grows into whatever a document needs, so this only decides how many
	// documents a fresh parser allocates for.
	DefaultBufferSize = 4096

	// DefaultValueStackSize is the number of nested ListValues and
	// InputObjectValues a parser can read without growing its stack.
	DefaultValueStackSize = 32

	// maxRetainedBufferSize is the largest buffer a parser keeps between calls.
	// A bigger one is released, so one oversized document doesn't make a parser
	// hold on to an oversized buffer.
	maxRetainedBufferSize = 1 << 20
)

// state holds the buffers a [Parser] reuses across calls. It's not generic: the
// state machine works on a string, whatever the input type.
type state struct {
	// buf holds the canonical token stream until it's written out.
	buf []byte

	// stack holds one frame per ListValue and InputObjectValue currently open.
	stack []byte
}

func newState(bufferSize, valueStackSize int) *state {
	if bufferSize < 1 {
		bufferSize = DefaultBufferSize
	}
	if valueStackSize < 1 {
		valueStackSize = DefaultValueStackSize
	}
	return &state{
		buf:   make([]byte, 0, bufferSize),
		stack: make([]byte, 0, valueStackSize),
	}
}

var pool = sync.Pool{New: func() any { return newState(0, 0) }}

// Parse reads a Document, which is one or many ExecutableDefinitions, and writes
// its canonical form to w, applying options.
//
// The canonical form leaves out comments, spaces, tabs, line-breaks,
// carriage-returns and descriptions, so two documents that differ only in
// formatting produce the same bytes. It's assembled in full and handed to w in a
// single Write. Nothing is written for a document that turns out to be invalid.
//
// The returned [Error] is the zero value if s is a valid document, and carries
// the error of w if the write failed. Parse never resets w, so several documents
// can be written into one sum.
//
// Unlike [Parser.Parse] this function takes its buffers from a global pool and
// can therefore be less efficient.
//
// Reference:
//
//   - https://spec.graphql.org/September2025/#Document
//   - https://spec.graphql.org/September2025/#ExecutableDefinition
func Parse[S ~string | ~[]byte](w io.Writer, options Options, s S) Error {
	p := pool.Get().(*state)
	err := parse(p, w, options, asString(s))
	pool.Put(p)
	return err
}

// Parser is a reusable parser instance. Reusing one is more efficient than
// calling [Parse], which takes its buffers from a global pool.
//
// A Parser is not safe for concurrent use.
type Parser[S ~string | ~[]byte] struct{ s *state }

// NewParser creates a new reusable parser instance. bufferSize and
// valueStackSize preallocate the two buffers a parser needs; both fall back to
// [DefaultBufferSize] and [DefaultValueStackSize] when less than 1.
func NewParser[S ~string | ~[]byte](bufferSize, valueStackSize int) *Parser[S] {
	return &Parser[S]{s: newState(bufferSize, valueStackSize)}
}

// Parse is identical to the [Parse] function but reuses the buffers of p.
func (p *Parser[S]) Parse(w io.Writer, options Options, s S) Error {
	return parse(p.s, w, options, asString(s))
}

// asString views s as a string without copying it.
//
// The state machine only reads the source and keeps no reference to it, so the
// view doesn't outlive the call. A named []byte type is the one case that
// copies: a type switch can't match it.
func asString[S ~string | ~[]byte](s S) string {
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
