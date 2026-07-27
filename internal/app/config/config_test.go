package config_test

import (
	"strings"
	"testing"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
)

func TestParseIgnore(t *testing.T) {
	f := func(t *testing.T, expect gqlhash.Ignore, expectOK bool, input string) {
		t.Helper()
		a, ok := config.ParseIgnore(input)
		if ok != expectOK {
			t.Errorf("expected ok: %t; received: %t; input: %q", expectOK, ok, input)
		}
		if a != expect {
			t.Errorf("expected: %#v; received: %#v", expect, a)
		}
	}

	f(t, 0, false, "")
	f(t, 0, false, "unsupported")
	f(t, 0, false, "inputs_")
	f(t, 0, false, "_inputs")
	// The two flags of v1 are gone, so their names name no mode.
	f(t, 0, false, "ignore-inputs")
	f(t, gqlhash.IgnoreNothing, true, "nothing")
	f(t, gqlhash.IgnoreNothing, true, "Nothing")
	f(t, gqlhash.IgnoreNothing, true, "NOTHING")
	f(t, gqlhash.IgnoreInputs, true, "inputs")
	f(t, gqlhash.IgnoreInputs, true, "Inputs")
	f(t, gqlhash.IgnoreInputs, true, "INPUTS")
	f(t, gqlhash.IgnoreVariables, true, "variables")
	f(t, gqlhash.IgnoreVariables, true, "Variables")
	f(t, gqlhash.IgnoreVariables, true, "VARIABLES")
}

func TestParseFormat(t *testing.T) {
	f := func(t *testing.T, expect config.Format, input string) {
		t.Helper()
		if a := config.ParseFormat(input); a != expect {
			t.Errorf("expected: %#v; received: %#v", expect, a)
		}
	}

	f(t, 0, "")
	f(t, 0, "unsupported")
	f(t, 0, "hex_")
	f(t, 0, "_hex")
	f(t, config.FormatHex, "hex")
	f(t, config.FormatHex, "Hex")
	f(t, config.FormatHex, "HEX")
	f(t, config.FormatBase32, "base32")
	f(t, config.FormatBase32, "Base32")
	f(t, config.FormatBase32, "BASE32")
	f(t, config.FormatBase64, "base64")
	f(t, config.FormatBase64, "Base64")
	f(t, config.FormatBase64, "BASE64")
	f(t, config.FormatBase64URL, "base64url")
	f(t, config.FormatBase64URL, "Base64URL")
	f(t, config.FormatBase64URL, "BASE64URL")
}

func TestParseHashFunction(t *testing.T) {
	f := func(t *testing.T, expect config.HashFunction, input string) {
		t.Helper()
		if a := config.ParseHashFunction(input); a != expect {
			t.Errorf("expected: %#v; received: %#v", expect, a)
		}
	}

	f(t, 0, "")
	f(t, 0, "unsupported")
	f(t, 0, "sha1_")
	f(t, 0, "_sha1")
	f(t, config.HashFunctionSHA1, "sha1")
	f(t, config.HashFunctionSHA1, "SHA1")
	f(t, config.HashFunctionSHA2, "sha2")
	f(t, config.HashFunctionSHA2, "SHA2")
	f(t, config.HashFunctionSHA3, "sha3")
	f(t, config.HashFunctionSHA3, "SHA3")
	f(t, config.HashFunctionMD5, "md5")
	f(t, config.HashFunctionMD5, "MD5")
	f(t, config.HashFunctionBLAKE2B, "blake2b")
	f(t, config.HashFunctionBLAKE2B, "Blake2B")
	f(t, config.HashFunctionBLAKE2B, "Blake2b")
	f(t, config.HashFunctionBLAKE2B, "BLAKE2B")
	f(t, config.HashFunctionBLAKE2S, "blake2s")
	f(t, config.HashFunctionBLAKE2S, "Blake2S")
	f(t, config.HashFunctionBLAKE2S, "Blake2s")
	f(t, config.HashFunctionBLAKE2S, "BLAKE2S")
	f(t, config.HashFunctionBLAKE3, "blake3")
	f(t, config.HashFunctionBLAKE3, "Blake3")
	f(t, config.HashFunctionBLAKE3, "BLAKE3")
	f(t, config.HashFunctionFNV, "fnv")
	f(t, config.HashFunctionFNV, "Fnv")
	f(t, config.HashFunctionFNV, "FNV")
	f(t, config.HashFunctionFNV1A, "fnv1a")
	f(t, config.HashFunctionFNV1A, "Fnv1a")
	f(t, config.HashFunctionFNV1A, "FNV1A")
	f(t, config.HashFunctionXXH64, "xxh64")
	f(t, config.HashFunctionXXH64, "XXH64")
	f(t, config.HashFunctionXXH64, "Xxh64")
	f(t, config.HashFunctionCRC32, "crc32")
	f(t, config.HashFunctionCRC32, "CRC32")
	f(t, config.HashFunctionCRC64, "crc64")
	f(t, config.HashFunctionCRC64, "CRC64")
}

// TestNewHasher asserts that every supported name maps to a hasher, and that the
// digest widths are the ones the documentation of the flag promises.
func TestNewHasher(t *testing.T) {
	widths := map[string]int{
		"sha1":    20,
		"sha2":    32,
		"sha3":    64,
		"md5":     16,
		"blake2b": 32,
		"blake2s": 32,
		"blake3":  32,
		"fnv":     8,
		"fnv1a":   8,
		"xxh64":   8,
		"crc32":   4,
		"crc64":   8,
	}
	names := strings.Split(config.SupportedHashFunctions, ", ")
	if len(names) != len(widths) {
		t.Fatalf("expected %d supported functions; the list names %d",
			len(widths), len(names))
	}

	for _, name := range names {
		f := config.ParseHashFunction(name)
		if f == 0 {
			t.Errorf("%s: expected the name to parse", name)
			continue
		}
		h := config.NewHasher(f)
		if h == nil {
			t.Errorf("%s: expected a hasher", name)
			continue
		}
		if h.Size() != widths[name] {
			t.Errorf("%s: expected %d bytes; received %d",
				name, widths[name], h.Size())
		}
		// A hasher starts empty, whatever it is.
		if got := len(h.Sum(nil)); got != widths[name] {
			t.Errorf("%s: expected a sum of %d bytes; received %d",
				name, widths[name], got)
		}
	}

	// An unparsed name has no hasher.
	if h := config.NewHasher(0); h != nil {
		t.Errorf("expected no hasher for the zero value; received %T", h)
	}
}
