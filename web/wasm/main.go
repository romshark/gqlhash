//go:build js && wasm

// Command wasm exposes gqlhash to JavaScript. It's compiled to WebAssembly
// (GOOS=js GOARCH=wasm) and drives the static demo page in ../.
//
// It installs a single global object, `gqlhash`, with:
//
//	gqlhash.version                 // gqlhash module version
//	gqlhash.hashFunctions           // [{id, label}, ...]
//	gqlhash.formats                 // [{id, label}, ...]
//	gqlhash.hash(source, options)   // {hash} | {error: {...}}
package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"hash/fnv"
	"runtime/debug"
	"syscall/js"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/blake2s"

	"github.com/romshark/gqlhash"
)

func main() {
	js.Global().Set("gqlhash", js.ValueOf(map[string]any{
		"version":       version(),
		"hashFunctions": hashFunctionList(),
		"formats":       formatList(),
		"hash":          js.FuncOf(hashQuery),
	}))

	// Keep the Go runtime alive so the exported functions stay callable.
	<-make(chan struct{})
}

// hashQuery is the JS entry point. args[0] is the GraphQL document, args[1] an
// options object: {hash, format, ignoreInputs, ignoreVariables}. It returns
// {hash: string} on success and {error: {message, line, column, offset}} for a
// document that doesn't parse.
func hashQuery(_ js.Value, args []js.Value) any {
	if len(args) < 1 || args[0].Type() != js.TypeString {
		return fail("expected the GraphQL document as first argument")
	}
	source := args[0].String()

	var options js.Value
	if len(args) > 1 {
		options = args[1]
	}

	hasher := newHasher(optString(options, "hash", "sha1"))
	if hasher == nil {
		return fail("unsupported hash function")
	}
	encode := encoderFor(optString(options, "format", "hex"))
	if encode == nil {
		return fail("unsupported output format")
	}

	sum, err := gqlhash.AppendQueryHashWithOptions(nil, hasher, gqlhash.Options{
		IgnoreInputs:    optBool(options, "ignoreInputs"),
		IgnoreVariables: optBool(options, "ignoreVariables"),
	}, []byte(source))
	if err != nil {
		// No position: this version of the parser reports the error without
		// saying where it stopped. The page treats offset, line and column as
		// optional and just prints the message when they're absent.
		return map[string]any{"error": map[string]any{
			"message": err.Error(),
		}}
	}

	return map[string]any{
		"hash": encode(sum),
		"bits": len(sum) * 8,
	}
}

// fail reports a problem with the call itself, not with the document.
func fail(message string) any {
	return map[string]any{"error": map[string]any{"message": message}}
}

func optString(options js.Value, key, fallback string) string {
	if options.Type() != js.TypeObject {
		return fallback
	}
	if v := options.Get(key); v.Type() == js.TypeString {
		return v.String()
	}
	return fallback
}

func optBool(options js.Value, key string) bool {
	if options.Type() != js.TypeObject {
		return false
	}
	return options.Get(key).Truthy()
}

// newHasher returns a new hasher for id, or nil if id is unsupported.
// The ids match the -hash flag of cmd/gqlhash.
func newHasher(id string) hash.Hash {
	switch id {
	case "sha1":
		return sha1.New()
	case "sha2":
		return sha256.New()
	case "sha3":
		return sha3.New512()
	case "md5":
		return md5.New()
	case "blake2b":
		h, err := blake2b.New256(nil)
		if err != nil {
			return nil
		}
		return h
	case "blake2s":
		h, err := blake2s.New256(nil)
		if err != nil {
			return nil
		}
		return h
	case "blake3":
		return blake3.New()
	case "fnv":
		return fnv.New64()
	case "fnv1a":
		return fnv.New64a()
	case "xxh64":
		return xxhash.New()
	case "crc32":
		return crc32.NewIEEE()
	case "crc64":
		return crc64.New(crc64.MakeTable(crc64.ISO))
	}
	return nil
}

// encoderFor returns the encoder for id, or nil if id is unsupported.
// The ids match the -format flag of cmd/gqlhash.
func encoderFor(id string) func([]byte) string {
	switch id {
	case "hex":
		return hex.EncodeToString
	case "base32":
		return base32.StdEncoding.EncodeToString
	case "base64":
		return base64.StdEncoding.EncodeToString
	case "base64url":
		return base64.URLEncoding.EncodeToString
	}
	return nil
}

// hashFunctionList mirrors the -hash flag documentation of cmd/gqlhash so the
// page never has to keep its own copy of the list.
func hashFunctionList() []any {
	return []any{
		option("sha1", "SHA-1 (160 bit)"),
		option("sha2", "SHA-256 (256 bit)"),
		option("sha3", "SHA3-512 (512 bit)"),
		option("md5", "MD5 (128 bit)"),
		option("blake2b", "BLAKE2b, unkeyed (256 bit)"),
		option("blake2s", "BLAKE2s, unkeyed (256 bit)"),
		option("blake3", "BLAKE3, unkeyed (256 bit)"),
		option("fnv", "FNV-1 (64 bit)"),
		option("fnv1a", "FNV-1a (64 bit)"),
		option("xxh64", "XXH64, unseeded (64 bit)"),
		option("crc32", "CRC-32, IEEE (32 bit)"),
		option("crc64", "CRC-64, ISO 3309 (64 bit)"),
	}
}

func formatList() []any {
	return []any{
		option("hex", "hex"),
		option("base32", "base32"),
		option("base64", "base64"),
		option("base64url", "base64url"),
	}
}

func option(id, label string) any {
	return map[string]any{"id": id, "label": label}
}

// version reports the version of the gqlhash module this binary was built from.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
