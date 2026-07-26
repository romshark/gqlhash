// Package gqlhash hashes GraphQL executable documents of the latest GraphQL
// specification (https://spec.graphql.org/September2025/), ignoring differences
// in formatting. It hashes the canonical form that [parser.Parse] writes.
package gqlhash

import (
	"bytes"
	"errors"
	"hash"

	"github.com/romshark/gqlhash/parser"
)

// Error is a [parser.Error]. Its zero value means no error, so callers check
// [Error.Err] instead of comparing to nil.
type Error = parser.Error

var (
	ErrUnexpectedEOF      = parser.ErrUnexpectedEOF
	ErrUnexpectedToken    = parser.ErrUnexpectedToken
	ErrUnexpectedVariable = parser.ErrUnexpectedVariable

	ErrInvalidEscape        = parser.ErrInvalidEscape
	ErrMalformedNumber      = parser.ErrMalformedNumber
	ErrMalformedUTF8        = parser.ErrMalformedUTF8
	ErrUnescapedControlChar = parser.ErrUnescapedControlChar
	ErrQueriesDiffer        = errors.New("queries differ")
)

// Hash is the subset of the standard [hash.Hash] this package needs.
// [parser.Parse] takes an [io.Writer] instead.
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
func Position[S ~string | ~[]byte](s S, offset int) (line, column int) {
	return parser.Position(s, offset)
}

// Compare returns the zero [Error] if the documents a and b have the same hash,
// and an [Error] carrying [ErrQueriesDiffer] if both are valid GraphQL but
// differ. Applies options (see [Options]).
//
// Order is significant: two documents with the same fields in a different order
// differ.
func Compare[S ~string | ~[]byte](h hash.Hash, options Options, a, b S) Error {
	return CompareWithBuffer(nil, h, options, a, b)
}

// CompareWithBuffer is [Compare] with a reusable buffer for the two sums.
// A buffer of capacity h.Size()*2 avoids the allocation.
func CompareWithBuffer[S ~string | ~[]byte](
	buffer []byte, h hash.Hash, options Options, a, b S,
) Error {
	size := h.Size()
	if buffer == nil {
		buffer = make([]byte, 0, size*2)
	} else {
		buffer = buffer[:0]
	}
	var err Error
	buffer, err = AppendQueryHash(buffer, h, options, a)
	if err.Err != nil {
		return err
	}
	buffer, err = AppendQueryHash(buffer, h, options, b)
	if err.Err != nil {
		return err
	}
	if !bytes.Equal(buffer[:size], buffer[size:]) {
		return Error{Err: ErrQueriesDiffer, Offset: -1}
	}
	return Error{}
}

// AppendQueryHash reads the document s and appends its hash to buffer, applying
// options. It resets h.
func AppendQueryHash[S ~string | ~[]byte](
	buffer []byte, h Hash, options Options, s S,
) ([]byte, Error) {
	h.Reset()
	if err := parser.Parse(h, options, s); err.Err != nil {
		// Why no error: returning err as one allocates.
		return nil, err
	}
	return h.Sum(buffer), Error{}
}
