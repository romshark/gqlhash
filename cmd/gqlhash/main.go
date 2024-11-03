package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"debug/buildinfo"
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
	Version                = `1.1.0`
	SupportedHashFunctions = "sha1, sha2, sha3, md5, blake2b, blake2s, " +
		"fnv, crc32, crc64"
	SupportedOutputFormats = "hex, base64"
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

	if !isValidOutputFormatName(*fFormat) {
		fmt.Fprintf(
			os.Stderr, "unsupported format %q, use any of: "+
				SupportedOutputFormats+"\n",
			*fFormat,
		)
		os.Exit(1)
	}
	if !isValidHashFuncName(*fHashFunction) {
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
	switch name := *fHashFunction; {
	case strings.EqualFold(name, "sha1"):
		hasher = sha1.New()
	case strings.EqualFold(name, "sha2"):
		hasher = sha256.New()
	case strings.EqualFold(name, "sha3"):
		hasher = sha3.New512()
	case strings.EqualFold(name, "md5"):
		hasher = md5.New()
	case strings.EqualFold(name, "blake2b"):
		hasher, err = blake2b.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2b hasher: %w", err))
		}
	case strings.EqualFold(name, "blake2s"):
		hasher, err = blake2s.New256(nil)
		if err != nil {
			panic(fmt.Errorf("initializing blake2s hasher: %w", err))
		}
	case strings.EqualFold(name, "fnv"):
		hasher = fnv.New64()
	case strings.EqualFold(name, "crc32"):
		hasher = crc32.NewIEEE()
	case strings.EqualFold(name, "crc64"):
		hasher = crc64.New(crc64.MakeTable(crc64.ISO))
	default:
		panic(fmt.Errorf("unsupported hash function: %q", name))
	}

	sum, err := gqlhash.AppendQueryHash(nil, hasher, input)
	if err != nil {
		panic(err)
	}

	var sumStr string
	if strings.EqualFold(*fFormat, "hex") {
		sumStr = hex.EncodeToString(sum)
	} else if strings.EqualFold(*fFormat, "base64") {
		sumStr = base64.StdEncoding.EncodeToString(sum)
	}
	fmt.Fprint(os.Stdout, sumStr)
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

func isValidOutputFormatName(s string) bool {
	return strings.EqualFold(s, "hex") ||
		strings.EqualFold(s, "base64")
}

func isValidHashFuncName(s string) bool {
	return strings.EqualFold(s, "sha1") ||
		strings.EqualFold(s, "sha2") ||
		strings.EqualFold(s, "sha3") ||
		strings.EqualFold(s, "md5") ||
		strings.EqualFold(s, "blake2b") ||
		strings.EqualFold(s, "blake2s") ||
		strings.EqualFold(s, "fnv") ||
		strings.EqualFold(s, "crc32") ||
		strings.EqualFold(s, "crc64")
}
