// Package gqlhash hashes GraphQL executable documents of the latest GraphQL
// specification (https://spec.graphql.org/September2025/), ignoring differences
// in formatting. It hashes the canonical form that [parser.Parse] writes.
package gqlhash

import (
	"bytes"
	"hash"

	"github.com/romshark/gqlhash/v2/parser"
)

// Error is a [parser.Error]. Its zero value means no error,
// so callers check [Error.Err] instead of comparing to nil.
type Error = parser.Error

var (
	ErrUnexpectedEOF      = parser.ErrUnexpectedEOF
	ErrUnexpectedToken    = parser.ErrUnexpectedToken
	ErrUnexpectedVariable = parser.ErrUnexpectedVariable

	ErrInvalidEscape        = parser.ErrInvalidEscape
	ErrMalformedNumber      = parser.ErrMalformedNumber
	ErrMalformedUTF8        = parser.ErrMalformedUTF8
	ErrUnescapedControlChar = parser.ErrUnescapedControlChar
)

// Hash is what [AppendHash] and [Hasher] need of a [hash.Hash]. [Compare]
// takes the full interface, because it needs Size. [parser.Parse] takes an
// [io.Writer].
type Hash interface {
	Reset()
	Sum([]byte) []byte
	Write([]byte) (int, error)
}

var _ Hash = hash.Hash(nil)

// Options configures how a document is hashed (see [parser.Options]).
type Options = parser.Options

// Ignore says how much of the input a document is hashed without
// (see [parser.Ignore]).
type Ignore = parser.Ignore

const (
	IgnoreNothing   = parser.IgnoreNothing
	IgnoreInputs    = parser.IgnoreInputs
	IgnoreVariables = parser.IgnoreVariables
)

// Position returns the 1-based line and column of offset in s (see [parser.Position]).
func Position[S string | []byte](s S, offset int) (line, column int) {
	return parser.Position(s, offset)
}

// Compare reports whether the documents a and b have the same hash.
//
// Two valid documents that differ are no error: equal is false and the returned
// [Error] is the zero value. equal is false whenever the [Error] holds one.
//
// Order is significant: two documents with the same fields in a different order
// differ.
func Compare[S string | []byte](
	h hash.Hash, options Options, a, b S,
) (equal bool, err Error) {
	return CompareWithBuffer(nil, h, options, a, b)
}

// CompareWithBuffer is [Compare] with a reusable buffer for the two sums.
// A buffer of capacity h.Size()*2 avoids the allocation.
func CompareWithBuffer[S string | []byte](
	buffer []byte, h hash.Hash, options Options, a, b S,
) (equal bool, err Error) {
	size := h.Size()
	if buffer == nil {
		buffer = make([]byte, 0, size*2)
	} else {
		buffer = buffer[:0]
	}
	if buffer, err = AppendHash(buffer, h, options, a); err.Err != nil {
		return false, err
	}
	if buffer, err = AppendHash(buffer, h, options, b); err.Err != nil {
		return false, err
	}
	return bytes.Equal(buffer[:size], buffer[size:]), Error{}
}

// Hasher hashes documents with buffers of its own: a parser, the hash it writes
// into and a buffer for the two sums of [Hasher.Compare]. Nothing is taken from
// a global pool, which is what a server on a per-request path wants.
//
// WARNING: A Hasher is not safe for concurrent use. Use one per goroutine.
type Hasher[S string | []byte] struct {
	parser  *parser.Parser[S]
	hash    Hash
	options Options
	sums    []byte
}

// NewHasher returns a [Hasher] writing into h. Its parser starts at
// the default sizes and grows into whatever the documents need.
func NewHasher[S string | []byte](h Hash, options Options) *Hasher[S] {
	return &Hasher[S]{
		parser:  parser.NewParser[S](0, 0),
		hash:    h,
		options: options,
	}
}

// Append reads the document s and appends its hash to buffer. It resets the hash of h.
func (h *Hasher[S]) Append(buffer []byte, s S) ([]byte, Error) {
	h.hash.Reset()
	if err := h.parser.Parse(h.hash, h.options, s); err.Err != nil {
		return nil, err
	}
	return h.hash.Sum(buffer), Error{}
}

// Compare is [Compare] with the buffers of h. It needs no Size, because it takes
// the length of the first sum.
func (h *Hasher[S]) Compare(a, b S) (equal bool, err Error) {
	h.hash.Reset()
	if err := h.parser.Parse(h.hash, h.options, a); err.Err != nil {
		return false, err
	}
	sums := h.hash.Sum(h.sums[:0])
	size := len(sums)

	h.hash.Reset()
	// The buffer is kept on every way out, so a rejected document doesn't cost
	// the next call its capacity.
	if err = h.parser.Parse(h.hash, h.options, b); err.Err != nil {
		h.sums = sums
		return false, err
	}
	sums = h.hash.Sum(sums)
	h.sums = sums

	return bytes.Equal(sums[:size], sums[size:]), Error{}
}

// AppendHash reads the document s and appends its hash to buffer, applying
// options. It resets h.
//
// It takes its parser from a global pool.
func AppendHash[S string | []byte](
	buffer []byte, h Hash, options Options, s S,
) ([]byte, Error) {
	h.Reset()
	if err := parser.Parse(h, options, s); err.Err != nil {
		return nil, err
	}
	return h.Sum(buffer), Error{}
}
