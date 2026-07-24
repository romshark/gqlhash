package main

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
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
	f(t, 0, nil, stdout(`00790a44dd9ef781d2b7e56d3c791ee8297a32af`),
		args(), "{foo}")
	f(t, 0, nil, stdout(`00790a44dd9ef781d2b7e56d3c791ee8297a32af`),
		args(), "\n{\n\tfoo\n}\n")
	f(t, 0, nil, stdout(`00790a44dd9ef781d2b7e56d3c791ee8297a32af`),
		args(`-format`, `hex`), "{foo}")

	f(t, 0, nil, stdout(`AHkKRN2e94HSt+VtPHke6Cl6Mq8=`),
		args(`-format`, `base64`), "{foo}")
	f(t, 0, nil, stdout(`AHkKRN2e94HSt-VtPHke6Cl6Mq8=`),
		args(`-format`, `base64url`), "{foo}")
	f(t, 0, nil, stdout(`AB4QURG5T33YDUVX4VWTY6I65AUXUMVP`),
		args(`-format`, `base32`), "{foo}")

	f(t, 0, nil, stdout(`bb73ddf48baecb383eab5085e72eb325`+
		`adf990b204b3ae84b0fe82ac77d4704d`),
		args(`-format`, `hex`, `-hash`, `sha2`), "{foo}")
	f(t, 0, nil, stdout(`249c1537af1305b6c33818b23758df6d`+
		`1d42942959cc03f3703a86838c2e71d1`+
		`b1666eb5f4d28371d78cd5064cf5f453`+
		`2f163c5bd4a5c11903c1a365897e9a04`),
		args(`-format`, `hex`, `-hash`, `sha3`), "{foo}")
	f(t, 0, nil, stdout(`26bb7f5938c24756e3d9e5dac0577e6f`),
		args(`-format`, `hex`, `-hash`, `md5`), "{foo}")
	f(t, 0, nil, stdout(`b976303832871433b162dae14fb6504f`+
		`b593391b297bfc0204166750c1f945e0`),
		args(`-format`, `hex`, `-hash`, `blake2b`), "{foo}")
	f(t, 0, nil, stdout(`1311412899a149a732286d27f460b6d1`+
		`71c5a6c0ebf128bb8258c85017204af5`),
		args(`-format`, `hex`, `-hash`, `blake2s`), "{foo}")
	f(t, 0, nil, stdout(`3e988d618ad5cc152e791e683b5ece5b`+
		`74aea4c4b14c68b6f436f142ee252b28`),
		args(`-format`, `hex`, `-hash`, `blake3`), "{foo}")
	f(t, 0, nil, stdout(`cdd3df8c52548af0`),
		args(`-format`, `hex`, `-hash`, `fnv`), "{foo}")
	f(t, 0, nil, stdout(`370dd5d549c14f5e`),
		args(`-format`, `hex`, `-hash`, `fnv1a`), "{foo}")
	f(t, 0, nil, stdout(`1f6e896a45206c1b`),
		args(`-format`, `hex`, `-hash`, `xxh64`), "{foo}")
	f(t, 0, nil, stdout(`0dabfb06`),
		args(`-format`, `hex`, `-hash`, `crc32`), "{foo}")
	f(t, 0, nil, stdout(`77cc3c305bf54e20`),
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
	f(t, 0, nil, stdout(`00790a44dd9ef781d2b7e56d3c791ee8297a32af`),
		args(`-file`, testInputGraphQL), "this must not be read")

	// Input file doesn't exist
	f(t, 1, stderr(`error reading file "non-existing-file.graphql": `+
		`open non-existing-file.graphql: no such file or directory`+"\n"), nil,
		args(`-file`, "non-existing-file.graphql"), "this must not be read")
}

// TestRunOutputEncodings checks that base32 and base64 output decodes back to
// the same digest as hex, across several hash functions (i.e. digest lengths).
func TestRunOutputEncodings(t *testing.T) {
	get := func(t *testing.T, hf, format string) string {
		t.Helper()
		out, errOut := new(IORecorder), new(IORecorder)
		if code := run(args("-hash", hf, "-format", format), out, errOut,
			strings.NewReader("{foo}")); code != 0 {
			t.Fatalf("hash %s format %s: code %d; stderr %v", hf, format, code, *errOut)
		}
		return strings.Join(*out, "")
	}

	for hf := range strings.SplitSeq(SupportedHashFunctions, ", ") {
		raw, err := hex.DecodeString(get(t, hf, "hex"))
		if err != nil || len(raw) == 0 {
			t.Fatalf("%s hex decode: err=%v len=%d", hf, err, len(raw))
		}
		if got, err := base64.StdEncoding.DecodeString(
			get(t, hf, "base64"),
		); err != nil ||
			!bytes.Equal(got, raw) {
			t.Errorf("%s: base64 does not round-trip to the digest: %v", hf, err)
		}
		if got, err := base64.URLEncoding.DecodeString(
			get(t, hf, "base64url"),
		); err != nil ||
			!bytes.Equal(got, raw) {
			t.Errorf("%s: base64url does not round-trip to the digest: %v", hf, err)
		}
		if got, err := base32.StdEncoding.DecodeString(
			get(t, hf, "base32"),
		); err != nil ||
			!bytes.Equal(got, raw) {
			t.Errorf("%s: base32 does not round-trip to the digest: %v", hf, err)
		}
	}
}

func TestRunIgnoreOptions(t *testing.T) {
	hash := func(t *testing.T, stdin string, a ...string) string {
		t.Helper()
		out, errOut := new(IORecorder), new(IORecorder)
		if code := run(args(a...), out, errOut, strings.NewReader(stdin)); code != 0 {
			t.Fatalf("code %d; stderr: %v", code, *errOut)
		}
		return strings.Join(*out, "")
	}

	// -ignore-inputs: queries differing only in input values (and their types)
	// hash the same, but differ from the default (full) hash.
	full := hash(t, `{ f(x: 1) }`)
	in1 := hash(t, `{ f(x: 1) }`, "-ignore-inputs")
	in2 := hash(t, `{ f(x: "different") }`, "-ignore-inputs")
	if in1 != in2 {
		t.Errorf("-ignore-inputs: expected equal hashes, got %q and %q", in1, in2)
	}
	if in1 == full {
		t.Error("-ignore-inputs must change the hash")
	}

	// -ignore-variables implies -ignore-inputs and also drops the variable, so a
	// parameterized query matches its literal, unparameterized form.
	v1 := hash(t, `query Q($v: Int) { f(a: $v) }`, "-ignore-variables")
	v2 := hash(t, `query Q { f(a: 1) }`, "-ignore-variables")
	if v1 != v2 {
		t.Errorf("-ignore-variables: expected equal hashes, got %q and %q", v1, v2)
	}
	// Under -ignore-inputs a variable usage is ignored like any other value,
	// but the variable signature (definition) is kept.
	if hash(t, `{ f(a: $v) }`, "-ignore-inputs") !=
		hash(t, `{ f(a: 1) }`, "-ignore-inputs") {
		t.Error("-ignore-inputs must ignore a variable usage like a literal")
	}
	if hash(t, `query ($v: ID) { f(a: $v) }`, "-ignore-inputs") ==
		hash(t, `{ f(a: 1) }`, "-ignore-inputs") {
		t.Error("-ignore-inputs must keep the variable signature")
	}
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

	original := Version
	t.Cleanup(func() { Version = original })
	Version = "1.2.3-test"

	f(t, 0, []string{"gqlhash v1.2.3-test"}, args("-version"))
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
	f(t, FormatBase64URL, "base64url")
	f(t, FormatBase64URL, "Base64URL")
	f(t, FormatBase64URL, "BASE64URL")
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
	f(t, HashFunctionBLAKE3, "blake3")
	f(t, HashFunctionBLAKE3, "Blake3")
	f(t, HashFunctionBLAKE3, "BLAKE3")
	f(t, HashFunctionFNV, "fnv")
	f(t, HashFunctionFNV, "Fnv")
	f(t, HashFunctionFNV, "FNV")
	f(t, HashFunctionFNV1A, "fnv1a")
	f(t, HashFunctionFNV1A, "Fnv1a")
	f(t, HashFunctionFNV1A, "FNV1A")
	f(t, HashFunctionXXH64, "xxh64")
	f(t, HashFunctionXXH64, "XXH64")
	f(t, HashFunctionXXH64, "Xxh64")
	f(t, HashFunctionCRC32, "crc32")
	f(t, HashFunctionCRC32, "CRC32")
	f(t, HashFunctionCRC64, "crc64")
	f(t, HashFunctionCRC64, "CRC64")
}
