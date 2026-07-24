package main

import (
	"os"
	"testing"

	"github.com/romshark/gqlhash"
)

// benchHashFunctions lists every supported function in the order it appears
// in SupportedHashFunctions.
var benchHashFunctions = []struct {
	Name string
	Func HashFunction
}{
	{"sha1", HashFunctionSHA1},
	{"sha2", HashFunctionSHA2},
	{"sha3", HashFunctionSHA3},
	{"md5", HashFunctionMD5},
	{"blake2b", HashFunctionBLAKE2B},
	{"blake2s", HashFunctionBLAKE2S},
	{"fnv", HashFunctionFNV},
	{"fnv1a", HashFunctionFNV1A},
	{"xxh64", HashFunctionXXH64},
	{"crc32", HashFunctionCRC32},
	{"crc64", HashFunctionCRC64},
}

// BenchmarkHashFunctions measures the end-to-end cost of hashing one query
// with each supported hash function. The parser work is identical across all
// of them and dominates the total, so differences here are small. See
// BenchmarkHashFunctionsRaw for the cost of the hash functions in isolation.
func BenchmarkHashFunctions(b *testing.B) {
	query, err := os.ReadFile("../../testdata/big.graphql")
	if err != nil {
		b.Fatal(err)
	}

	for _, f := range benchHashFunctions {
		b.Run(f.Name, func(b *testing.B) {
			hasher := newHasher(f.Func)
			if hasher == nil {
				b.Fatalf("no hasher for %q", f.Name)
			}
			buf := make([]byte, 0, hasher.Size())
			b.SetBytes(int64(len(query)))
			b.ReportAllocs()

			for b.Loop() {
				buf, err = gqlhash.AppendQueryHash(buf[:0], hasher, query)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkHashFunctionsRaw measures each hash function on its own, without
// any parsing, over a buffer the size of a typical query.
func BenchmarkHashFunctionsRaw(b *testing.B) {
	query, err := os.ReadFile("../../testdata/big.graphql")
	if err != nil {
		b.Fatal(err)
	}

	for _, f := range benchHashFunctions {
		b.Run(f.Name, func(b *testing.B) {
			hasher := newHasher(f.Func)
			if hasher == nil {
				b.Fatalf("no hasher for %q", f.Name)
			}
			buf := make([]byte, 0, hasher.Size())
			b.SetBytes(int64(len(query)))
			b.ReportAllocs()

			for b.Loop() {
				hasher.Reset()
				_, _ = hasher.Write(query)
				buf = hasher.Sum(buf[:0])
			}
		})
	}
}
