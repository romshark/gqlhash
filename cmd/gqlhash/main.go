package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"debug/buildinfo"
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
	"os/exec"
	"strings"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/sha3"

	"github.com/romshark/gqlhash"
)

const (
	Version                = `1.2.0`
	SupportedHashFunctions = "sha1, sha2, sha3, md5, blake2b, blake2s, " +
		"fnv, crc32, crc64"
	SupportedOutputFormats = "hex, base32, base64"
)

func main() {
	fFile := flag.String(
		"file",
		"",
		"Path to GraphQL file containing executable operations",
	)
	fFormat := flag.String(
		"format",
		"hex",
		"Hash format ("+SupportedOutputFormats+")",
	)
	fHashFunction := flag.String(
		"hash",
		"sha1",
		"Selects the hash function "+
			"("+SupportedHashFunctions+").\n"+
			"sha2 is SHA-256.\n"+
			"sha3 is SHA3-512.\n"+
			"blake2b is unkeyed.\n"+
			"blake2s is unkeyed.\n"+
			"crc32 uses the IEEE polynomial.\n"+
			"crc64 uses ISO polynomial, defined in ISO 3309 and used in HDLC.",
	)
	fVersion := flag.Bool(
		"version",
		false,
		`Print version to stdout and exit`,
	)
	flag.Parse()

	if *fVersion {
		PrintVersionInfoAndExit()
	}

	outputFormat := parseFormat(*fFormat)
	if outputFormat == 0 {
		fmt.Fprintf(
			os.Stderr, "unsupported format %q, use any of: "+
				SupportedOutputFormats+"\n",
			*fFormat,
		)
		os.Exit(1)
	}

	hashFunc := parseHashFunction(*fHashFunction)
	if hashFunc == 0 {
		fmt.Fprintf(
			os.Stderr, "unsupported hash function %q, use any of: "+
				SupportedHashFunctions+"\n",
			*fHashFunction,
		)
		os.Exit(1)
	}

	var input []byte
	var err error
	if *fFile != "" {
		if input, err = os.ReadFile(*fFile); err != nil {
			fmt.Fprintf(os.Stderr, "error reading file %q: %v\n", *fFile, err)
			os.Exit(1)
		}
	} else {
		input, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
	}

	if len(input) < 1 {
		fmt.Fprintln(os.Stderr, "no input")
		os.Exit(1)
	}

	var hasher hash.Hash
	switch hashFunc {
	case HashFunctionSHA1:
		hasher = sha1.New()
	case HashFunctionSHA2:
		hasher = sha256.New()
	case HashFunctionSHA3:
		hasher = sha3.New512()
	case HashFunctionMD5:
		hasher = md5.New()
	case HashFunctionBLAKE2B:
		hasher, err = blake2b.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2b hasher: %w", err))
		}
	case HashFunctionBLAKE2S:
		hasher, err = blake2s.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2s hasher: %w", err))
		}
	case HashFunctionFNV:
		hasher = fnv.New64()
	case HashFunctionCRC32:
		hasher = crc32.NewIEEE()
	case HashFunctionCRC64:
		hasher = crc64.New(crc64.MakeTable(crc64.ISO))
	default:
		panic(fmt.Errorf("unsupported hash function: %q", *fHashFunction))
	}

	sum, err := gqlhash.AppendQueryHash(nil, hasher, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "syntax error: %v\n", err.Error())
		os.Exit(1)
	}

	switch outputFormat {
	case FormatHex:
		if _, err = hex.NewEncoder(os.Stdout).Write(sum); err != nil {
			panic(fmt.Errorf("encoding hex to stdout: %w", err))
		}
	case FormatBase32:
		if _, err = base32.NewEncoder(
			base32.StdEncoding, os.Stdout,
		).Write(sum); err != nil {
			panic(fmt.Errorf("encoding base32 to stdout: %w", err))
		}
	case FormatBase64:
		if _, err = base64.NewEncoder(
			base64.StdEncoding, os.Stdout,
		).Write(sum); err != nil {
			panic(fmt.Errorf("encoding base64 to stdout: %w", err))
		}
	default:
		panic(fmt.Errorf("unsupported output format: %q", *fFormat))
	}
}

func PrintVersionInfoAndExit() {
	p, err := exec.LookPath(os.Args[0])
	if err != nil {
		fmt.Printf("resolving executable file path: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(p)
	if err != nil {
		fmt.Printf("opening executable file %q: %v\n", os.Args[0], err)
		os.Exit(1)
	}

	info, err := buildinfo.Read(f)
	if err != nil {
		fmt.Printf("Reading build information: %v\n", err)
	}

	fmt.Printf("gqlhash v%s\n\n", Version)
	fmt.Println("MIT License")
	fmt.Print("Copyright (c) 2024 Roman Scharkov (github.com/romshark)\n\n")
	fmt.Printf("%v\n", info)

	os.Exit(0)
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
	case strings.EqualFold(s, "fnv"):
		return HashFunctionFNV
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
	HashFunctionFNV
	HashFunctionCRC32
	HashFunctionCRC64
)
