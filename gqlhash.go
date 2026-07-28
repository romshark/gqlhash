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
//
// Requirement: never hold one in an error variable, see [parser.Error].
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

// Hash is what this package needs of a [hash.Hash], which every implementation
// of that interface satisfies.
//
// Sum must append the digest to its argument, as [hash.Hash] requires.
type Hash interface {
	Reset()
	Size() int
	Sum([]byte) []byte
	Write([]byte) (int, error)
}

var _ Hash = hash.Hash(nil)

// Options configures how a document is hashed (see [parser.Options]).
type Options = parser.Options

// Ignore says how much of the input a document is hashed without (see [parser.Ignore]).
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
// Order is significant: two documents with the same fields in a different order differ.
func Compare[S string | []byte](
	h Hash, options Options, a, b S,
) (equal bool, err Error) {
	return CompareWithBuffer(nil, h, options, a, b)
}

// CompareWithBuffer is [Compare] with a reusable buffer for the two sums.
// A buffer of capacity h.Size()*2 avoids the allocation.
func CompareWithBuffer[S string | []byte](
	buffer []byte, h Hash, options Options, a, b S,
) (equal bool, err Error) {
	size := h.Size()
	if cap(buffer) < size*2 {
		buffer = make([]byte, 0, size*2)
	}
	if buffer, err = AppendHash(buffer[:0], h, options, a); err.Err != nil {
		return false, err
	}
	if buffer, err = AppendHash(buffer, h, options, b); err.Err != nil {
		return false, err
	}
	return bytes.Equal(buffer[:size], buffer[size:]), Error{}
}

// Hasher hashes and compares documents without allocating.
// Reuse one on a per-request path instead of calling [AppendHash] and [Compare].
//
// WARNING: A Hasher is not safe for concurrent use. Use one per goroutine.
type Hasher[S string | []byte] struct {
	parser  *parser.Parser[S]
	hash    Hash
	options Options
	sums    []byte
}

// NewHasher returns a [Hasher] writing into h and applying options.
func NewHasher[S string | []byte](h Hash, options Options) *Hasher[S] {
	return &Hasher[S]{
		parser:  parser.NewParser[S](0),
		hash:    h,
		options: options,
		sums:    make([]byte, 0, h.Size()*2),
	}
}

// Append reads the document s and appends its hash to buffer.
// It resets the hash of h.
//
// A document that's rejected leaves buffer as it was, which is what the AppendX
// convention promises: buf, err = h.Append(buf, s) keeps what buf held where s
// didn't parse.
func (h *Hasher[S]) Append(buffer []byte, s S) ([]byte, Error) {
	h.hash.Reset()
	if err := h.parser.Parse(h.hash, h.options, s); err.Err != nil {
		return buffer, err
	}
	return h.hash.Sum(buffer), Error{}
}

// Compare is [Compare] with the hash and the options of h.
func (h *Hasher[S]) Compare(a, b S) (equal bool, err Error) {
	h.hash.Reset()
	if err = h.parser.Parse(h.hash, h.options, a); err.Err != nil {
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

// AppendHash reads the document s and appends its hash to buffer,
// applying options. It resets h.
//
// A document that's rejected leaves buffer as it was, which is what the AppendX
// convention promises: buf, err = AppendHash(buf, ...) keeps what buf held where
// s didn't parse.
//
// On a per-request path use a [Hasher], which allocates nothing.
func AppendHash[S string | []byte](
	buffer []byte, h Hash, options Options, s S,
) ([]byte, Error) {
	h.Reset()
	if err := parser.Parse(h, options, s); err.Err != nil {
		return buffer, err
	}
	return h.Sum(buffer), Error{}
}
