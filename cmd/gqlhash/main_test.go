package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type (
	IORecorder []string
	Stdout     IORecorder
	Stderr     IORecorder
)

func (r *IORecorder) Write(data []byte) (int, error) {
	*r = append(*r, string(data))
	return len(data), nil
}

func args(a ...string) []string { return append([]string{"gqlhash"}, a...) }
func stderr(w ...string) Stderr { return Stderr(w) }
func stdout(w ...string) Stdout { return Stdout(w) }

func TestRun(t *testing.T) {
	f := func(
		t *testing.T,
		expectCode int, expectStderr Stderr, expectStdout Stdout,
		args []string, stdin string,
	) {
		t.Helper()
		stdout, stderr := new(IORecorder), new(IORecorder)
		code := run(args, stdout, stderr, strings.NewReader(stdin))
		if code != expectCode {
			t.Errorf("expected code: %d; received: %d", expectCode, code)
		}
		if !slices.Equal([]string(expectStdout), []string(*stdout)) {
			t.Errorf("expected stdout: %v; received: %v", expectStdout, *stdout)
		}
		if !slices.Equal([]string(expectStderr), []string(*stderr)) {
			t.Errorf("expected stderr: %v; received: %v", expectStderr, *stderr)
		}
	}

	// OK
	f(t, 0, nil, stdout(`2d73057de8d557231e49afab76d4a8f1f6fee4f6`),
		args(), "{foo}")
	f(t, 0, nil, stdout(`2d73057de8d557231e49afab76d4a8f1f6fee4f6`),
		args(), "\n{\n\tfoo\n}\n")
	f(t, 0, nil, stdout(`2d73057de8d557231e49afab76d4a8f1f6fee4f6`),
		args(`-format`, `hex`), "{foo}")

	f(t, 0, nil, stdout(`LXMFfejVVyMeSa+rdtSo8fb+`),
		args(`-format`, `base64`), "{foo}")
	f(t, 0, nil, stdout(`FVZQK7PI2VLSGHSJV6VXNVFI6H3P5ZHW`),
		args(`-format`, `base32`), "{foo}")

	f(t, 0, nil, stdout(`ec302dda203507b68c0e67788e27a992`+
		`bc392ae52f5a53b1687b2c05d036de2b`),
		args(`-format`, `hex`, `-hash`, `sha2`), "{foo}")
	f(t, 0, nil, stdout(`8fb70f49c8113eed544a9db07d279e3f`+
		`42aa2e7891db9e210c06daa6d892fa82`+
		`2b5e9e80af399295d1793978f619e0e7`+
		`50d2f59d38f8b7c69c183e11391ed2ad`),
		args(`-format`, `hex`, `-hash`, `sha3`), "{foo}")
	f(t, 0, nil, stdout(`6da8fb6da0232e6a3ff35462f8dbfcf3`),
		args(`-format`, `hex`, `-hash`, `md5`), "{foo}")
	f(t, 0, nil, stdout(`777c1a4f616083cfeb327521199e137d`+
		`82818ea624a784bc453177e499f5fac3`),
		args(`-format`, `hex`, `-hash`, `blake2b`), "{foo}")
	f(t, 0, nil, stdout(`1cc14e924dee9c0111c54cbbf7bd3691`+
		`17b64209ffcbd39e901bebc7fdd7c784`),
		args(`-format`, `hex`, `-hash`, `blake2s`), "{foo}")
	f(t, 0, nil, stdout(`a0ff20a63ae62338`),
		args(`-format`, `hex`, `-hash`, `fnv`), "{foo}")
	f(t, 0, nil, stdout(`9a642f5c`),
		args(`-format`, `hex`, `-hash`, `crc32`), "{foo}")
	f(t, 0, nil, stdout(`298fed3c30006037`),
		args(`-format`, `hex`, `-hash`, `crc64`), "{foo}")

	// Err arguments
	f(t, 2, stderr(`unsupported format "base10", use any of: `+
		SupportedOutputFormats+"\n"), nil,
		args(`-format`, `base10`), "{foo}")
	f(t, 2, stderr(`unsupported hash function "sha9", use any of: `+
		SupportedHashFunctions+"\n"), nil,
		args(`-hash`, `sha9`), "{foo}")

	// Err
	f(t, 1, stderr("no input\n"), nil,
		args(), "")

	// GraphQL Syntax error
	f(t, 1, stderr("syntax error: unexpected EOF\n"), nil,
		args(), "{")

	// File input
	tempDir := t.TempDir()
	testInputGraphQL := filepath.Join(tempDir, "test-input.graphql")
	if err := os.WriteFile(testInputGraphQL, []byte(`{ foo }`), 0o644); err != nil {
		t.Fatalf("writing test input file: %v", err)
	}
	f(t, 0, nil, stdout(`2d73057de8d557231e49afab76d4a8f1f6fee4f6`),
		args(`-file`, testInputGraphQL), "this must not be read")

	// Input file doesn't exist
	f(t, 1, stderr(`error reading file "non-existing-file.graphql": `+
		`open non-existing-file.graphql: no such file or directory`+"\n"), nil,
		args(`-file`, "non-existing-file.graphql"), "this must not be read")
}

func TestRunVersion(t *testing.T) {
	f := func(
		t *testing.T,
		expectCode int, expectStdoutContains []string,
		args []string,
	) {
		t.Helper()
		stdout, stderr := new(IORecorder), new(IORecorder)
		code := run(args, stdout, stderr, nil)
		if code != expectCode {
			t.Errorf("expected code: %d; received: %d", expectCode, code)
		}
		if *stderr != nil {
			t.Errorf("expected no stderr, received: %v", *stderr)
		}
		stdoutStr := strings.Join(*stdout, "")
		for _, s := range expectStdoutContains {
			if !strings.Contains(stdoutStr, s) {
				t.Errorf("expected stdout to contain: %q; received: %v", s, *stdout)
			}
		}
	}

	f(t, 0, []string{"gqlhash v" + Version}, args("-version"))
}

func TestParseFormat(t *testing.T) {
	f := func(t *testing.T, expect Format, input string) {
		t.Helper()
		if a := parseFormat(input); a != expect {
			t.Errorf("expected: %#v; received: %#v", expect, a)
		}
	}

	f(t, 0, "")
	f(t, 0, "unsupported")
	f(t, 0, "hex_")
	f(t, 0, "_hex")
	f(t, FormatHex, "hex")
	f(t, FormatHex, "Hex")
	f(t, FormatHex, "HEX")
	f(t, FormatBase32, "base32")
	f(t, FormatBase32, "Base32")
	f(t, FormatBase32, "BASE32")
	f(t, FormatBase64, "base64")
	f(t, FormatBase64, "Base64")
	f(t, FormatBase64, "BASE64")
}

func TestParseHashFunction(t *testing.T) {
	f := func(t *testing.T, expect HashFunction, input string) {
		t.Helper()
		if a := parseHashFunction(input); a != expect {
			t.Errorf("expected: %#v; received: %#v", expect, a)
		}
	}

	f(t, 0, "")
	f(t, 0, "unsupported")
	f(t, 0, "sha1_")
	f(t, 0, "_sha1")
	f(t, HashFunctionSHA1, "sha1")
	f(t, HashFunctionSHA1, "SHA1")
	f(t, HashFunctionSHA2, "sha2")
	f(t, HashFunctionSHA2, "SHA2")
	f(t, HashFunctionSHA3, "sha3")
	f(t, HashFunctionSHA3, "SHA3")
	f(t, HashFunctionMD5, "md5")
	f(t, HashFunctionMD5, "MD5")
	f(t, HashFunctionBLAKE2B, "blake2b")
	f(t, HashFunctionBLAKE2B, "Blake2B")
	f(t, HashFunctionBLAKE2B, "Blake2b")
	f(t, HashFunctionBLAKE2B, "BLAKE2B")
	f(t, HashFunctionBLAKE2S, "blake2s")
	f(t, HashFunctionBLAKE2S, "Blake2S")
	f(t, HashFunctionBLAKE2S, "Blake2s")
	f(t, HashFunctionBLAKE2S, "BLAKE2S")
	f(t, HashFunctionFNV, "fnv")
	f(t, HashFunctionFNV, "Fnv")
	f(t, HashFunctionFNV, "FNV")
	f(t, HashFunctionCRC32, "crc32")
	f(t, HashFunctionCRC32, "CRC32")
	f(t, HashFunctionCRC64, "crc64")
	f(t, HashFunctionCRC64, "CRC64")
}
