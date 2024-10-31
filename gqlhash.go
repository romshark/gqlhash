// Package gqlhash provides GraphQL query hashing functions for
// the latest GraphQL specification: https://spec.graphql.org/October2021/
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
func Compare[S string | []byte](h hash.Hash, a, b S) error {
	ha, err := AppendQueryHash(nil, h, a)
	if err != nil {
		return err
	}
	hb, err := AppendQueryHash(nil, h, b)
	if err != nil {
		return err
	}
	if !bytes.Equal(ha, hb) {
		return ErrQueriesDiffer
	}
	return nil
}

// AppendQueryHash parses s and appends its hash to buffer ignoring comments, spaces, tabs,
// line-breaks and carriage-returns.
func AppendQueryHash[S string | []byte](buffer []byte, h Hash, s S) ([]byte, error) {
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
