package parser

import (
	"errors"
	"fmt"
	"hash"
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
	// It wraps [ErrUnexpectedToken], so matching that one with [errors.Is] still works.
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
	// takes it as it is.
	// Reference:
	//
	//   - https://spec.graphql.org/September2025/#StringCharacter
	ErrUnescapedControlChar = fmt.Errorf("%w: unescaped control character",
		ErrUnexpectedToken)
)

// Error says where in the document parsing stopped. Its zero value means no
// error, so callers check [Error.Err] instead of comparing to nil.
//
// [Parse] returns it as it is, not as an error, because putting it into an
// error would allocate on every rejected document.
type Error struct {
	// Err is nil when there's no error. Otherwise it's the sentinel:
	// [ErrUnexpectedEOF], [ErrUnexpectedToken] or one of the errors wrapping
	// [ErrUnexpectedToken].
	Err error

	// Offset is the byte index into the document where parsing stopped.
	Offset int

	// Line and Column are the 1-based position of Offset.
	// A column counts characters, not bytes.
	Line, Column int
}

func (e Error) Error() string {
	switch {
	case e.Err == nil:
		return "no error"
	case e.Line == 0:
		// An error that carries no position, like a hash mismatch.
		return e.Err.Error()
	}
	return fmt.Sprintf("%v (line %d, column %d)", e.Err, e.Line, e.Column)
}

func (e Error) Unwrap() error { return e.Err }

// newError points err at offset, the position where the state machine stopped.
func newError(src string, offset int, err error) Error {
	offset = min(max(offset, 0), len(src))
	head := src[:offset]

	// CRLF is one LineTerminator, so the pairs are counted once
	// (https://spec.graphql.org/September2025/#LineTerminator).
	line := 1 + strings.Count(head, "\n") + strings.Count(head, "\r") -
		strings.Count(head, "\r\n")

	// A column counts the characters after the last LineTerminator.
	// RuneCountInString takes a malformed byte for one character, which is what
	// the state machine does.
	lineStart := max(
		strings.LastIndexByte(head, '\n'), strings.LastIndexByte(head, '\r'),
	) + 1
	column := utf8.RuneCountInString(head[lineStart:]) + 1

	return Error{Err: err, Offset: offset, Line: line, Column: column}
}

// Hash is a subset of the standard [hash.Hash].
type Hash interface {
	Reset()
	Sum([]byte) []byte
	Write([]byte) (int, error)
}

var _ Hash = hash.Hash(nil)

// The hash prefixes are written as magic bytes before the actual query contents
// to prevent tokens from collapsing into one if separators aren't written, for example:
// query fields `{ foo bar }` might collapse into one field `{ foobar }`
// producing the same hash for those two different queries.
// 0x9, 0xA and 0xD cannot be used because they're valid bytes within string values
// (https://spec.graphql.org/September2025/#SourceCharacter).
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

// Options configures how a document is hashed.
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
	// stream is assembled in before it's handed to the [Hash]. The buffer grows
	// into whatever a document needs, so this only decides how many documents a
	// fresh parser allocates for.
	DefaultBufferSize = 4096

	// DefaultValueStackSize is the number of nested ListValues and
	// InputObjectValues a parser can read without growing its stack.
	DefaultValueStackSize = 32

	// maxRetainedBufferSize is the buffer size a parser keeps between calls. One
	// oversized document must not make a parser, or a pooled one at that, hold
	// on to its buffer for good.
	maxRetainedBufferSize = 1 << 20
)

// state holds the buffers a [Parser] reuses across calls. It's not generic
// because the state machine always works on a string, whatever the input type.
type state struct {
	// buf holds the canonical token stream until it's flushed to the [Hash].
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
// its canonical form to h, applying options. The canonical form leaves out
// comments, spaces, tabs, line-breaks, carriage-returns and descriptions, so two
// documents that differ only in formatting produce the same hash.
//
// The returned [Error] is the zero value if s is a valid document. Parse doesn't
// reset h, which lets a caller hash several documents into one sum.
//
// Unlike [Parser.Parse] this function takes its buffers from a global pool and
// can therefore be less efficient. Consider reusing a [Parser] instead.
//
// Reference:
//
//   - https://spec.graphql.org/September2025/#Document
//   - https://spec.graphql.org/September2025/#ExecutableDefinition
func Parse[S ~string | ~[]byte](h Hash, options Options, s S) Error {
	p := pool.Get().(*state)
	err := parse(p, h, options, asString(s))
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
func (p *Parser[S]) Parse(h Hash, options Options, s S) Error {
	return parse(p.s, h, options, asString(s))
}

// asString views s as a string without copying it.
//
// A []byte is only viewed, never written to, and the state machine keeps no
// reference to it once it returns, so the view never outlives the call. A named
// []byte type is the one case that copies, because a type switch can't match it.
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
