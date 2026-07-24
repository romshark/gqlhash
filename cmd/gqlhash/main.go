package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha3"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"hash/fnv"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/zeebo/blake3"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/blake2s"

	"github.com/romshark/gqlhash"
)

// Version is the release version, injected at build time by GoReleaser
// via -ldflags "-X main.Version=...". Defaults to "dev" for local builds.
var Version = "dev"

const (
	SupportedHashFunctions = "sha1, sha2, sha3, md5, blake2b, blake2s, " +
		"blake3, fnv, fnv1a, xxh64, crc32, crc64"
	SupportedOutputFormats = "hex, base32, base64"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr, os.Stdin))
}

func run(
	args []string,
	stdout, stderr io.Writer,
	stdin io.Reader,
) (exitCode int) {
	cli := flag.NewFlagSet(args[0], flag.ExitOnError)
	fFile := cli.String(
		"file",
		"",
		"Path to GraphQL file containing executable operations",
	)
	fFormat := cli.String(
		"format",
		"hex",
		"Hash format ("+SupportedOutputFormats+")",
	)
	fHashFunction := cli.String(
		"hash",
		"sha1",
		"Selects the hash function "+
			"("+SupportedHashFunctions+").\n"+
			"sha2 is SHA-256.\n"+
			"sha3 is SHA3-512.\n"+
			"blake2b is unkeyed.\n"+
			"blake2s is unkeyed.\n"+
			"blake3 is unkeyed, 256 bits wide.\n"+
			"fnv is FNV-1, 64 bits wide.\n"+
			"fnv1a is FNV-1a, 64 bits wide.\n"+
			"xxh64 is XXH64, unseeded.\n"+
			"crc32 uses the IEEE polynomial.\n"+
			"crc64 uses ISO polynomial, defined in ISO 3309 and used in HDLC.",
	)
	fIgnoreInputs := cli.Bool(
		"ignore-inputs",
		false,
		"Ignore input values so queries differing only in argument and\n"+
			"default values produce the same hash.",
	)
	fIgnoreVariables := cli.Bool(
		"ignore-variables",
		false,
		"Ignore variables entirely (definitions and usages). Implies\n"+
			"-ignore-inputs.",
	)
	fVersion := cli.Bool(
		"version",
		false,
		`Print version to stdout and exit`,
	)
	if err := cli.Parse(args[1:]); err != nil {
		panic(fmt.Errorf("parsing CLI arguments: %w", err))
	}

	if *fVersion {
		return printVersionInfoAndExit(stdout)
	}

	outputFormat := parseFormat(*fFormat)
	if outputFormat == 0 {
		_, _ = fmt.Fprintf(
			stderr, "unsupported format %q, use any of: "+
				SupportedOutputFormats+"\n",
			*fFormat,
		)
		return 2
	}

	hashFunc := parseHashFunction(*fHashFunction)
	if hashFunc == 0 {
		_, _ = fmt.Fprintf(
			stderr, "unsupported hash function %q, use any of: "+
				SupportedHashFunctions+"\n",
			*fHashFunction,
		)
		return 2
	}

	var input []byte
	var err error
	if *fFile != "" {
		if input, err = os.ReadFile(*fFile); err != nil {
			_, _ = fmt.Fprintf(stderr, "error reading file %q: %v\n", *fFile, err)
			return 1
		}
	} else {
		input, err = io.ReadAll(stdin)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "error reading stdin: %v\n", err)
			return 1
		}
	}

	if len(input) < 1 {
		_, _ = fmt.Fprintln(stderr, "no input")
		return 1
	}

	hasher := newHasher(hashFunc)
	if hasher == nil {
		panic(fmt.Errorf("unsupported hash function: %q", *fHashFunction))
	}

	sum, err := gqlhash.AppendQueryHashWithOptions(nil, hasher, gqlhash.Options{
		IgnoreInputs:    *fIgnoreInputs,
		IgnoreVariables: *fIgnoreVariables,
	}, input)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "syntax error: %v\n", err.Error())
		return 1
	}

	var encoded string
	switch outputFormat {
	case FormatHex:
		encoded = hex.EncodeToString(sum)
	case FormatBase32:
		encoded = base32.StdEncoding.EncodeToString(sum)
	case FormatBase64:
		encoded = base64.StdEncoding.EncodeToString(sum)
	default:
		panic(fmt.Errorf("unsupported output format: %q", *fFormat))
	}
	if _, err = io.WriteString(stdout, encoded); err != nil {
		panic(fmt.Errorf("writing hash to stdout: %w", err))
	}
	return 0
}

func printVersionInfoAndExit(w io.Writer) (exitCode int) {
	_, _ = fmt.Fprintf(w, "gqlhash v%s\n\n", Version)
	_, _ = fmt.Fprintln(w, "MIT License")
	_, _ = fmt.Fprint(w, "Copyright (c) 2024 Roman Scharkov (github.com/romshark)\n\n")

	if info, ok := debug.ReadBuildInfo(); ok {
		_, _ = fmt.Fprintf(w, "%v\n", info)
	}

	return 0
}

// newHasher returns a new hasher for f, or nil if f is unsupported.
func newHasher(f HashFunction) hash.Hash {
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

func parseFormat(s string) Format {
	switch {
	case strings.EqualFold(s, "hex"):
		return FormatHex
	case strings.EqualFold(s, "base32"):
		return FormatBase32
	case strings.EqualFold(s, "base64"):
		return FormatBase64
	}
	return 0
}

func parseHashFunction(s string) HashFunction {
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
