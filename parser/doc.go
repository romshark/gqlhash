// Package parser reads GraphQL executable documents of the latest GraphQL
// specification (https://spec.graphql.org/September2025/) and writes their
// canonical form to a hash.
//
// The canonical form leaves out everything that doesn't affect execution -
// comments, whitespace, line terminators, commas and descriptions - and
// normalizes what has more than one spelling: a string value is hashed by the
// value it stands for, a block string by its BlockStringValue and a type
// reference by its structure. Two documents that differ only in their
// formatting therefore produce the same hash.
package parser
