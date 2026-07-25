// Package gqlhash provides GraphQL query hashing functions for
// the latest GraphQL specification: https://spec.graphql.org/September2025/
// that ignore formatting differences.
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

// Hash is a subset of the standard [hash.Hash].
type Hash = parser.Hash

// Options configures how a document is hashed (see [parser.Options]).
type Options = parser.Options

// Compare returns the zero [Error] if GraphQL queries a and b are equal comparing
// their hashes while ignoring comments, spaces, tabs, line-breaks and
// carriage-returns. Err is [ErrQueriesDiffer] if the queries are valid GraphQL
// but different. The order of fields must be preserved, otherwise a difference
// will be observed. Applies options (see [Options]).
func Compare(h hash.Hash, options Options, a, b []byte) Error {
	return CompareWithBuffer(nil, h, options, a, b)
}

// CompareWithBuffer is identical to [Compare] but allows reusing a buffer
// to reduce dynamic memory allocation. Ideally, provide a buffer
// with the capacity of `h.Size()*2`.
func CompareWithBuffer(buffer []byte, h hash.Hash, options Options, a, b []byte) Error {
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
		return Error{Err: ErrQueriesDiffer, Line: 1, Column: 1}
	}
	return Error{}
}

// AppendQueryHash parses s and appends its hash to buffer ignoring comments,
// spaces, tabs, line-breaks and carriage-returns, applying options.
func AppendQueryHash(buffer []byte, h Hash, options Options, s []byte) ([]byte, Error) {
	h.Reset()
	// No SkipIgnorables here: the reader does it, and keeps the offsets of a
	// [parser.Error] relative to s.
	if err := parser.ReadDocument(h, options, s); err.Err != nil {
		// Returning err as an error allocates, which is why the reader hands
		// it back as it is.
		return nil, err
	}
	return h.Sum(buffer), Error{}
}
