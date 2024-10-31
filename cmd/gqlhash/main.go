package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/romshark/gqlhash"
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
		`Hash format (hex, base64)`,
	)
	flag.Parse()

	if strings.EqualFold(*fFormat, "hex") ||
		strings.EqualFold(*fFormat, "base64") {
		fmt.Fprint(os.Stderr, "unsupported format, use any of: hex, base64")
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

	sum, err := gqlhash.AppendQueryHash(nil, sha1.New(), input)
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
