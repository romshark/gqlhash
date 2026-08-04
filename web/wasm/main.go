//go:build js && wasm

// Command wasm exposes gqlhash to JavaScript. It's compiled to WebAssembly
// (GOOS=js GOARCH=wasm) and drives the static demo page in ../.
//
// It installs a single global object, `gqlhash`, with:
//
//	gqlhash.version                 // gqlhash module version
//	gqlhash.hashFunctions           // [{id, label, kind}, ...]
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

	"github.com/romshark/gqlhash/v2"
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

	sum, err := gqlhash.AppendHash(nil, hasher, gqlhash.Options{
		Ignore: ignoreLevel(options),
	}, source)
	if err.IsErr() {
		e := map[string]any{"message": err.Err.Error()}
		// A writer error carries no offset, and the page treats offset, line
		// and column as optional, so it just prints the message when they're
		// absent.
		if err.ErrOffset >= 0 {
			line, column := gqlhash.Position(source, err.ErrOffset)
			e["offset"], e["line"], e["column"] = err.ErrOffset, line, column
		}
		return map[string]any{"error": e}
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

// ignoreLevel reads the two booleans the page sends. ignoreVariables is the
// wider of the two and implies ignoreInputs, as the -ignore flag of cmd/gqlhash
// has it.
func ignoreLevel(options js.Value) gqlhash.Ignore {
	switch {
	case optBool(options, "ignoreVariables"):
		return gqlhash.IgnoreVariables
	case optBool(options, "ignoreInputs"):
		return gqlhash.IgnoreInputs
	}
	return gqlhash.IgnoreNothing
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

// What a hash function is worth to an allowlist, which rests on one property:
// collision resistance. The three groups are the README's — collision
// resistant, broken, and the checksums, which are collidable by construction.
// The last two are for grouping or bucketing documents, never for deciding
// whether one may run.
const (
	kindResistant = "resistant"
	kindBroken    = "broken"
	kindChecksum  = "checksum"
)

// hashFunctionList holds every hash the -hash flag of cmd/gqlhash takes, so
// the page never has to keep its own copy of the list, nor its own view of
// which entries in it are safe to allowlist with.
//
// Widest first, and the same width in the flag's own order. A list ordered by
// what it's worth to an allowlist would put every safe one at the top, which
// reads as a recommendation the kind tags make better; this way the digest a
// reader is looking for is where its width says it is.
func hashFunctionList() []any {
	return []any{
		hashOption("sha3", "SHA3-512 (512 bit)", kindResistant),
		hashOption("sha2", "SHA-256 (256 bit)", kindResistant),
		hashOption("blake2b", "BLAKE2b, unkeyed (256 bit)", kindResistant),
		hashOption("blake2s", "BLAKE2s, unkeyed (256 bit)", kindResistant),
		hashOption("blake3", "BLAKE3, unkeyed (256 bit)", kindResistant),
		hashOption("sha1", "SHA-1 (160 bit)", kindBroken),
		hashOption("md5", "MD5 (128 bit)", kindBroken),
		hashOption("fnv", "FNV-1 (64 bit)", kindChecksum),
		hashOption("fnv1a", "FNV-1a (64 bit)", kindChecksum),
		hashOption("xxh64", "XXH64, unseeded (64 bit)", kindChecksum),
		hashOption("crc64", "CRC-64, ISO 3309 (64 bit)", kindChecksum),
		hashOption("crc32", "CRC-32, IEEE (32 bit)", kindChecksum),
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

func hashOption(id, label, kind string) any {
	return map[string]any{"id": id, "label": label, "kind": kind}
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
