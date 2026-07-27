// Package config is the configuration vocabulary of the commands: which hash
// function to use, which output format, and how much of a document to leave out.
//
// It's a leaf package, so the hashing command and the proxy share it without one
// importing the other.
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

const (
	SupportedHashFunctions = "sha1, sha2, sha3, md5, blake2b, blake2s, " +
		"blake3, fnv, fnv1a, xxh64, crc32, crc64"
	SupportedOutputFormats = "hex, base32, base64, base64url"
	SupportedIgnoreModes   = "nothing, inputs, variables"
)

// newHasher returns a new hasher for f, or nil if f is unsupported.
func NewHasher(f HashFunction) hash.Hash {
	switch f {
	case HashFunctionSHA1:
		return sha1.New()
	case HashFunctionSHA2:
		return sha256.New()
	case HashFunctionSHA3:
		return sha3.New512()
	case HashFunctionMD5:
		return md5.New()
	case HashFunctionBLAKE2B:
		h, err := blake2b.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2b hasher: %w", err))
		}
		return h
	case HashFunctionBLAKE2S:
		h, err := blake2s.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2s hasher: %w", err))
		}
		return h
	case HashFunctionBLAKE3:
		return blake3.New()
	case HashFunctionFNV:
		return fnv.New64()
	case HashFunctionFNV1A:
		return fnv.New64a()
	case HashFunctionXXH64:
		return xxhash.New()
	case HashFunctionCRC32:
		return crc32.NewIEEE()
	case HashFunctionCRC64:
		return crc64.New(crc64.MakeTable(crc64.ISO))
	}
	return nil
}

// parseIgnore returns the ignore mode s names, and false if it names none.
// Unlike [parseFormat] and [parseHashFunction] it needs the second return:
// the zero value of [gqlhash.Ignore] is the valid IgnoreNothing.
func ParseIgnore(s string) (gqlhash.Ignore, bool) {
	switch {
	case strings.EqualFold(s, "nothing"):
		return gqlhash.IgnoreNothing, true
	case strings.EqualFold(s, "inputs"):
		return gqlhash.IgnoreInputs, true
	case strings.EqualFold(s, "variables"):
		return gqlhash.IgnoreVariables, true
	}
	return 0, false
}

func ParseFormat(s string) Format {
	switch {
	case strings.EqualFold(s, "hex"):
		return FormatHex
	case strings.EqualFold(s, "base32"):
		return FormatBase32
	case strings.EqualFold(s, "base64"):
		return FormatBase64
	case strings.EqualFold(s, "base64url"):
		return FormatBase64URL
	}
	return 0
}

func ParseHashFunction(s string) HashFunction {
	switch {
	case strings.EqualFold(s, "sha1"):
		return HashFunctionSHA1
	case strings.EqualFold(s, "sha2"):
		return HashFunctionSHA2
	case strings.EqualFold(s, "sha3"):
		return HashFunctionSHA3
	case strings.EqualFold(s, "md5"):
		return HashFunctionMD5
	case strings.EqualFold(s, "blake2b"):
		return HashFunctionBLAKE2B
	case strings.EqualFold(s, "blake2s"):
		return HashFunctionBLAKE2S
	case strings.EqualFold(s, "blake3"):
		return HashFunctionBLAKE3
	case strings.EqualFold(s, "fnv"):
		return HashFunctionFNV
	case strings.EqualFold(s, "fnv1a"):
		return HashFunctionFNV1A
	case strings.EqualFold(s, "xxh64"):
		return HashFunctionXXH64
	case strings.EqualFold(s, "crc32"):
		return HashFunctionCRC32
	case strings.EqualFold(s, "crc64"):
		return HashFunctionCRC64
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
