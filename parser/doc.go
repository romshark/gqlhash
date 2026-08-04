// Package parser reads GraphQL executable documents of the latest GraphQL
// specification (https://spec.graphql.org/September2025/) and writes their
// canonical form to an [io.Writer], typically a hash.
//
// The canonical form leaves out what doesn't affect execution: comments,
// whitespace, line terminators, commas and descriptions. What has more than one
// spelling is normalized: a string value is written as the value it stands for,
// a block string as its BlockStringValue and a type reference as its structure.
// Two documents that differ only in their formatting therefore produce the same
// canonical form, and hence the same hash.
package parser
