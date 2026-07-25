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

var (
	ErrUnexpectedEOF      = parser.ErrUnexpectedEOF
	ErrUnexpectedToken    = parser.ErrUnexpectedToken
	ErrUnexpectedVariable = parser.ErrUnexpectedVariable
	ErrQueriesDiffer      = errors.New("queries differ")
)

// Hash is a subset of the standard [hash.Hash].
type Hash = parser.Hash

// Options configures how a document is hashed (see [parser.Options]).
type Options = parser.Options

// Compare returns nil if GraphQL queries a and b are equal comparing their
// hashes while ignoring comments, spaces, tabs, line-breaks and carriage-returns.
// Returns [ErrQueriesDiffer] if the queries are valid GraphQL but different.
// The order of fields must be preserved, otherwise a difference will be observed.
func Compare(h hash.Hash, a, b []byte) error {
	return CompareWithOptions(nil, h, Options{}, a, b)
}

// CompareWithBuffer is identical to [Compare] but allows reusing a buffer
// to reduce dynamic memory allocation. Ideally, provide a buffer
// with the capacity of `h.Size()*2`.
func CompareWithBuffer(buffer []byte, h hash.Hash, a, b []byte) error {
	return CompareWithOptions(buffer, h, Options{}, a, b)
}

// CompareWithOptions is identical to [CompareWithBuffer] but applies options, so
// two queries can be compared while ignoring input values and/or variables
// (see [Options]). Pass a nil buffer to have one allocated.
func CompareWithOptions(
	buffer []byte, h hash.Hash, options Options, a, b []byte,
) (err error) {
	size := h.Size()
	if buffer == nil {
		buffer = make([]byte, 0, size*2)
	} else {
		buffer = buffer[:0]
	}
	buffer, err = appendQueryHash(buffer, h, a, options)
	if err != nil {
		return err
	}
	buffer, err = appendQueryHash(buffer, h, b, options)
	if err != nil {
		return err
	}
	if !bytes.Equal(buffer[:size], buffer[size:]) {
		return ErrQueriesDiffer
	}
	return nil
}

// AppendQueryHash parses s and appends its hash to buffer ignoring
// comments, spaces, tabs, line-breaks and carriage-returns.
func AppendQueryHash(buffer []byte, h Hash, s []byte) ([]byte, error) {
	return appendQueryHash(buffer, h, s, Options{})
}

// AppendQueryHashWithOptions is identical to [AppendQueryHash] but applies options.
func AppendQueryHashWithOptions(
	buffer []byte, h Hash, options Options, s []byte,
) ([]byte, error) {
	return appendQueryHash(buffer, h, s, options)
}

func appendQueryHash(
	buffer []byte, h Hash, s []byte, options Options,
) ([]byte, error) {
	h.Reset()
	s = parser.SkipIgnorables(s)
	if err := parser.ExpectNoEOF(s); err != nil {
		return nil, err
	}
	if err := parser.ReadDocumentWithOptions(h, options, s); err != nil {
		return nil, err
	}
	return h.Sum(buffer), nil
}
