// Package config is the configuration vocabulary of the commands:
// which hash function to use, which output format,
// and how much of a document to leave out.
//
// It's a leaf package, so the hashing command and the proxy share
// it without one importing the other.
package config

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"hash/fnv"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/blake2s"

	"github.com/romshark/gqlhash/v2"
)

// hashFunctions is the one place a hash function is spelled out: the flag value
// naming it, the constant it parses to, whether an allowlist may rely on it,
// and how one is made. Everything else derives from this, so adding one is one edit
// rather than five that fail quietly.
//
// The order is the help strings' order: the default first, then the rest of the
// collision-resistant ones — which makes [SupportedProxyHashFunctions] the front
// of the list — then the two that are broken, then the ones collidable by
// construction. A caller reading the help top to bottom reads them worst-last.
var hashFunctions = []struct {
	name  string
	value HashFunction

	// proxySafe is whether the proxy accepts it, see [SupportedProxyHashFunctions].
	proxySafe bool

	new func() hash.Hash
}{
	{name: "sha2", value: HashFunctionSHA2, proxySafe: true, new: sha256.New},
	{
		name: "sha3", value: HashFunctionSHA3, proxySafe: true,
		new: func() hash.Hash { return sha3.New512() },
	},
	{
		name: "blake2b", value: HashFunctionBLAKE2B, proxySafe: true,
		new: func() hash.Hash {
			h, err := blake2b.New256(nil)
			if err != nil {
				panic(fmt.Errorf("initializing blake2b hasher: %w", err))
			}
			return h
		},
	},
	{
		name: "blake2s", value: HashFunctionBLAKE2S, proxySafe: true,
		new: func() hash.Hash {
			h, err := blake2s.New256(nil)
			if err != nil {
				panic(fmt.Errorf("initializing blake2s hasher: %w", err))
			}
			return h
		},
	},
	{
		name: "blake3", value: HashFunctionBLAKE3, proxySafe: true,
		new: func() hash.Hash { return blake3.New() },
	},
	// Broken: offered for a cache key or a bucket, refused by the proxy.
	{name: "sha1", value: HashFunctionSHA1, new: sha1.New},
	{name: "md5", value: HashFunctionMD5, new: md5.New},
	// Collidable by construction, so the same again and more so.
	{name: "fnv", value: HashFunctionFNV, new: func() hash.Hash { return fnv.New64() }},
	{
		name: "fnv1a", value: HashFunctionFNV1A,
		new: func() hash.Hash { return fnv.New64a() },
	},
	{
		name: "xxh64", value: HashFunctionXXH64,
		new: func() hash.Hash { return xxhash.New() },
	},
	{
		name: "crc32", value: HashFunctionCRC32,
		new: func() hash.Hash { return crc32.NewIEEE() },
	},
	{
		name: "crc64", value: HashFunctionCRC64,
		new: func() hash.Hash { return crc64.New(crc64.MakeTable(crc64.ISO)) },
	},
}

// ignoreModes and outputFormats are the same table for the other two flags.
var ignoreModes = []struct {
	name  string
	value gqlhash.Ignore
}{
	{"nothing", gqlhash.IgnoreNothing},
	{"inputs", gqlhash.IgnoreInputs},
	{"variables", gqlhash.IgnoreVariables},
}

var outputFormats = []struct {
	name  string
	value Format
}{
	{"hex", FormatHex},
	{"base32", FormatBase32},
	{"base64", FormatBase64},
	{"base64url", FormatBase64URL},
}

// The values a flag takes, in table order. They read as one line of help,
// so the punctuation here is the help text.
var (
	SupportedHashFunctions = names(hashFunctions,
		func(i int) (string, bool) { return hashFunctions[i].name, true })
	SupportedProxyHashFunctions = names(hashFunctions,
		func(i int) (string, bool) {
			return hashFunctions[i].name, hashFunctions[i].proxySafe
		})
	SupportedOutputFormats = names(outputFormats,
		func(i int) (string, bool) { return outputFormats[i].name, true })
	SupportedIgnoreModes = names(ignoreModes,
		func(i int) (string, bool) { return ignoreModes[i].name, true })
)

// names lists the names take reports for the entries of table.
func names[E any](table []E, take func(i int) (name string, ok bool)) string {
	var b strings.Builder
	for i := range table {
		name, ok := take(i)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
	}
	return b.String()
}

// NewHasher returns a new hasher for f, and false if f names none.
//
// The second return keeps an unknown function from surfacing later:
// a nil [hash.Hash] fails at the first Reset, on the request path,
// with nothing left pointing at the configuration that was wrong.
func NewHasher(f HashFunction) (hash.Hash, bool) {
	for _, e := range hashFunctions {
		if e.value == f {
			return e.new(), true
		}
	}
	return nil, false
}

// ParseHashFunction returns the hash function s names, and 0 for every name
// that is none of them.
func ParseHashFunction(s string) HashFunction {
	for _, e := range hashFunctions {
		if strings.EqualFold(s, e.name) {
			return e.value
		}
	}
	return 0
}

// ParseProxyHashFunction is [ParseHashFunction] restricted to the functions an
// allowlist may rely on. It returns 0 for every other name.
func ParseProxyHashFunction(s string) HashFunction {
	for _, e := range hashFunctions {
		if e.proxySafe && strings.EqualFold(s, e.name) {
			return e.value
		}
	}
	return 0
}

// HashName returns the flag value that names f, or "" for the zero value.
func HashName(f HashFunction) string {
	for _, e := range hashFunctions {
		if e.value == f {
			return e.name
		}
	}
	return ""
}

// ParseIgnore returns the ignore mode s names, and false if it names none.
// Unlike [ParseFormat] and [ParseHashFunction] it needs the second return:
// the zero value of [gqlhash.Ignore] is the valid IgnoreNothing.
func ParseIgnore(s string) (gqlhash.Ignore, bool) {
	for _, e := range ignoreModes {
		if strings.EqualFold(s, e.name) {
			return e.value, true
		}
	}
	return 0, false
}

// IgnoreName returns the flag value that names i.
func IgnoreName(i gqlhash.Ignore) string {
	for _, e := range ignoreModes {
		if e.value == i {
			return e.name
		}
	}
	return ""
}

// ParseFormat returns the output format s names, and 0 for every name that is
// none of them.
func ParseFormat(s string) Format {
	for _, e := range outputFormats {
		if strings.EqualFold(s, e.name) {
			return e.value
		}
	}
	return 0
}

type Format int8

const (
	_ Format = iota
	FormatHex
	FormatBase32
	FormatBase64
	FormatBase64URL
)

type HashFunction int8

const (
	_ HashFunction = iota
	HashFunctionSHA1
	HashFunctionSHA2
	HashFunctionSHA3
	HashFunctionMD5
	HashFunctionBLAKE2B
	HashFunctionBLAKE2S
	HashFunctionBLAKE3
	HashFunctionFNV
	HashFunctionFNV1A
	HashFunctionXXH64
	HashFunctionCRC32
	HashFunctionCRC64
)
