package main

import (
	"crypto/sha1"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/romshark/gqlhash"
)

const Version = `1.0.0`

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
	fVersion := flag.Bool(
		"version",
		false,
		`Print version to stdout and exit`,
	)
	flag.Parse()

	if *fVersion {
		PrintVersionInfoAndExit()
	}

	if !strings.EqualFold(*fFormat, "hex") &&
		!strings.EqualFold(*fFormat, "base64") {
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
	fmt.Print("Copyright (c) 2024 Roman Sharkov\n\n")
	fmt.Printf("%v\n", info)

	os.Exit(0)
}
