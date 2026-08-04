// Package gqlhash hashes GraphQL executable documents of the latest GraphQL
// specification (https://spec.graphql.org/September2025/), ignoring differences
// in formatting. It hashes the canonical form that [parser.Parse] writes.
package gqlhash

import (
	"bytes"
	"hash"

	"github.com/romshark/gqlhash/v2/parser"
)

// Result says whether hashing failed and where (see [parser.Result]).
type Result = parser.Result

var (
	ErrUnexpectedEOF      = parser.ErrUnexpectedEOF
	ErrUnexpectedToken    = parser.ErrUnexpectedToken
	ErrUnexpectedVariable = parser.ErrUnexpectedVariable

	ErrInvalidEscape        = parser.ErrInvalidEscape
	ErrMalformedNumber      = parser.ErrMalformedNumber
	ErrMalformedUTF8        = parser.ErrMalformedUTF8
	ErrUnescapedControlChar = parser.ErrUnescapedControlChar

	// ErrTooDeep is a document nesting deeper than [Options.DepthLimit] allows.
	ErrTooDeep = parser.ErrTooDeep
)

// The defaults an [Options] with no value of its own takes,
// and the size a [Hasher] starts at.
const (
	// DefaultDepthLimit is the nesting an [Options] with no DepthLimit takes.
	DefaultDepthLimit = parser.DefaultDepthLimit

	// DefaultBufferSize is how many bytes of canonical form a fresh [Hasher] holds.
	// A starting size and no limit, see [NewHasherWithBuffer].
	DefaultBufferSize = parser.DefaultBufferSize
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
// [Result] is the zero value. equal is false whenever the [Result] holds one.
//
// Order is significant: the same fields in a different order differ.
func Compare[S string | []byte](
	h Hash, options Options, a, b S,
) (equal bool, err Result) {
	return CompareWithBuffer(nil, h, options, a, b)
}

// CompareWithBuffer is [Compare] with a reusable buffer for the two sums.
// A buffer of capacity h.Size()*2 avoids the allocation.
func CompareWithBuffer[S string | []byte](
	buffer []byte, h Hash, options Options, a, b S,
) (equal bool, err Result) {
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
	return bytes.Equal(buffer[:size], buffer[size:]), Result{}
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
	return NewHasherWithBuffer[S](h, options, 0)
}

// NewHasherWithBuffer is [NewHasher] with the canonical-form buffer sized by the caller;
// 0 takes [DefaultBufferSize].
//
// The buffer grows to whatever a document needs, so this only decides what the
// first documents cost: sized for the largest, nothing grows it again.
func NewHasherWithBuffer[S string | []byte](
	h Hash, options Options, bufferSize int,
) *Hasher[S] {
	return &Hasher[S]{
		parser:  parser.NewParser[S](bufferSize),
		hash:    h,
		options: options,
		sums:    make([]byte, 0, h.Size()*2),
	}
}

// Append reads the document s and appends its hash to buffer, resetting the hash of h.
// A rejected document leaves buffer as it was, as the AppendX convention promises.
func (h *Hasher[S]) Append(buffer []byte, s S) ([]byte, Result) {
	h.hash.Reset()
	if err := h.parser.Parse(h.hash, h.options, s); err.Err != nil {
		return buffer, err
	}
	return h.hash.Sum(buffer), Result{}
}

// Compare is [Compare] with the hash and the options of h.
func (h *Hasher[S]) Compare(a, b S) (equal bool, err Result) {
	h.hash.Reset()
	if err = h.parser.Parse(h.hash, h.options, a); err.Err != nil {
		return false, err
	}
	sums := h.hash.Sum(h.sums[:0])
	size := len(sums)

	h.hash.Reset()
	// Kept on every way out,
	// so a rejected document doesn't cost the next call its capacity.
	if err = h.parser.Parse(h.hash, h.options, b); err.Err != nil {
		h.sums = sums
		return false, err
	}
	sums = h.hash.Sum(sums)
	h.sums = sums

	return bytes.Equal(sums[:size], sums[size:]), Result{}
}

// AppendHash reads the document s and appends its hash to buffer,
// applying options and resetting h.
// A rejected document leaves buffer as it was, as the AppendX convention promises.
//
// On a per-request path use a [Hasher], which allocates nothing.
func AppendHash[S string | []byte](
	buffer []byte, h Hash, options Options, s S,
) ([]byte, Result) {
	h.Reset()
	if err := parser.Parse(h, options, s); err.Err != nil {
		return buffer, err
	}
	return h.Sum(buffer), Result{}
}
