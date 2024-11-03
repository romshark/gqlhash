// Package gqlhash provides GraphQL query hashing functions for
// the latest GraphQL specification: https://spec.graphql.org/October2021/
// that ignore formatting differences.
package gqlhash

import (
	"bytes"
	"errors"
	"hash"

	"github.com/romshark/gqlhash/parser"
)

var (
	ErrUnexpectedEOF   = parser.ErrUnexpectedEOF
	ErrUnexpectedToken = parser.ErrUnexpectedToken
	ErrQueriesDiffer   = errors.New("queries differ")
)

// Hash is a subset of the standard `hash.Hash`.
type Hash = parser.Hash

// Compare returns nil if GraphQL queries a and b are equal comparing their
// hashes while ignoring comments, spaces, tabs, line-breaks and carriage-returns.
// Returns ErrQueriesDiffer if the queries are valid GraphQL but different.
// The order of fields must be preserved, otherwise a difference will be observed.
func Compare(h hash.Hash, a, b []byte) error {
	return CompareWithBuffer(nil, h, a, b)
}

// CompareWithBuffer is identical to Compare but allows reusing a buffer
// to reduce dynamic memory allocation. Ideally, provide a buffer
// with the capacity of `h.Size()*2`.
func CompareWithBuffer(buffer []byte, h hash.Hash, a, b []byte) (err error) {
	size := h.Size()
	if buffer == nil {
		buffer = make([]byte, 0, size*2)
	} else {
		buffer = buffer[:0]
	}
	buffer, err = AppendQueryHash(buffer, h, a)
	if err != nil {
		return err
	}
	buffer, err = AppendQueryHash(buffer, h, b)
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
	h.Reset()
	s = parser.SkipIgnorables(s)
	if err := parser.ExpectNoEOF(s); err != nil {
		return nil, err
	}
	if err := parser.ReadDocument(h, s); err != nil {
		return nil, err
	}
	return h.Sum(buffer), nil
}
