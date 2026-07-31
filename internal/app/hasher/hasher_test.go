package hasher_test

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
	"github.com/romshark/gqlhash/v2/internal/app/hasher"
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
		code := hasher.Run("gqlhash", "dev", args, stdout, stderr, strings.NewReader(stdin))
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

	// Err arguments The config package tests every rejected value.
	// This one case proves the message reaches the stderr of Run.
	f(t, 2, stderr(`unsupported hash function "sha9", use any of: `+
		config.SupportedHashFunctions+"\n"), nil,
		args(`-hash`, `sha9`), "{foo}")

	f(t, 1, stderr("no input\n"), nil,
		args(), "")

	// GraphQL syntax error. It points at where the document stopped, in the
	// file:line:column: message format that editors and CI annotations parse.
	f(t, 1, stderr("<stdin>:1:2: syntax error: unexpected EOF\n"), nil,
		args(), "{")
	f(t, 1, stderr("<stdin>:2:9: syntax error: unexpected token:"+
		" malformed number\n"), nil,
		args(), "query Q {\n  f(a: 01)\n}")

	// File input
	tempDir := t.TempDir()
	testInputGraphQL := filepath.Join(tempDir, "test-input.graphql")
	if err := os.WriteFile(testInputGraphQL, []byte(`{ foo }`), 0o644); err != nil {
		t.Fatalf("writing test input file: %v", err)
	}
	f(t, 0, nil, stdout(`00790a44dd9ef781d2b7e56d3c791ee8297a32af`),
		args(`-file`, testInputGraphQL), "this must not be read")

	// A syntax error in a file names the file, which is what makes the position
	// resolvable for an editor.
	invalidGraphQL := filepath.Join(tempDir, "invalid.graphql")
	if err := os.WriteFile(invalidGraphQL, []byte("query Q {\n  f(a: 01)\n}"),
		0o644); err != nil {
		t.Fatalf("writing test input file: %v", err)
	}
	f(t, 1, stderr(invalidGraphQL+":2:9: syntax error: unexpected token:"+
		" malformed number\n"), nil,
		args(`-file`, invalidGraphQL), "this must not be read")

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
		if code := hasher.Run("gqlhash", "dev", args("-hash", hf, "-format", format), out, errOut,
			strings.NewReader("{foo}")); code != 0 {
			t.Fatalf("hash %s format %s: code %d; stderr %v", hf, format, code, *errOut)
		}
		return strings.Join(*out, "")
	}

	for hf := range strings.SplitSeq(config.SupportedHashFunctions, ", ") {
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

// TestRunIgnoreOptions asserts that every -ignore mode reaches [gqlhash.Options],
// which is all the command adds. What the modes mean for a document is the
// parser's, see TestParseIgnoreInputs and TestParseIgnoreVariables there.
func TestRunIgnoreOptions(t *testing.T) {
	// The document carries a value and a variable definition, so each mode
	// leaves out something the previous one kept and the three hashes differ.
	const doc = `query Q($v: Int) { f(a: $v, b: 1) }`

	modes := []struct {
		flag   string
		expect gqlhash.Ignore
	}{
		{"nothing", gqlhash.IgnoreNothing},
		{"inputs", gqlhash.IgnoreInputs},
		{"variables", gqlhash.IgnoreVariables},
	}

	// -hash defaults to sha1 and -format to hex, so this is what the command
	// must print if, and only if, the flag reached Options.
	expected := make(map[string]string, len(modes))
	for _, m := range modes {
		h, _ := config.NewHasher(config.HashFunctionSHA1)
		sum, err := gqlhash.AppendHash(nil, h,
			gqlhash.Options{Ignore: m.expect}, doc)
		if err.IsErr() {
			t.Fatalf("%s: %v", m.flag, err)
		}
		expected[m.flag] = hex.EncodeToString(sum)
	}
	// A mode taken for another one has to be visible in the document above,
	// otherwise the comparisons below hold whatever the flag does.
	if len(slices.Compact(slices.Sorted(maps.Values(expected)))) != len(modes) {
		t.Fatalf("the document must hash differently under every mode: %v", expected)
	}

	for _, m := range modes {
		t.Run(m.flag, func(t *testing.T) {
			out, errOut := new(IORecorder), new(IORecorder)
			if code := hasher.Run("gqlhash", "dev", args("-ignore="+m.flag),
				out, errOut, strings.NewReader(doc)); code != 0 {
				t.Fatalf("code %d; stderr: %v", code, *errOut)
			}
			if received := strings.Join(*out, ""); received != expected[m.flag] {
				t.Errorf("expected the hash of Ignore %v (%s); received %s",
					m.expect, expected[m.flag], received)
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	f := func(
		t *testing.T,
		name, version string,
		expectCode int, expectStdoutContains []string,
		args []string,
	) {
		t.Helper()
		stdout, stderr := new(IORecorder), new(IORecorder)
		code := hasher.Run(name, version, args, stdout, stderr, nil)
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

	// The output names the binary that ran, so a bug report says which one,
	// and carries the license and the build information a report needs.
	f(t, "gqlhash", "1.2.3-test", 0,
		[]string{"gqlhash v1.2.3-test", "MIT License", "Copyright",
			"github.com/romshark/gqlhash/v2"},
		args("-version"))
}

// TestRunWriteFails covers a stdout that can't be written to, which is what a
// closed pipe looks like: `gqlhash -file q.graphql | head -1` leaves nobody to
// read the answer. Every other failure of this command is an exit code, and this
// one is too, rather than a goroutine dump over whatever the reader did get.
func TestRunWriteFails(t *testing.T) {
	errOut := new(IORecorder)
	code := hasher.Run("gqlhash", "dev", args(), brokenPipe{}, errOut,
		strings.NewReader("{ foo }"))

	if code != 1 {
		t.Errorf("expected code 1; received %d", code)
	}
	if got := strings.Join(*errOut, ""); !strings.Contains(got, "writing the hash") ||
		!strings.Contains(got, "broken pipe") {
		t.Errorf("expected the reason on stderr; received %q", got)
	}
}

// brokenPipe is a stdout nobody is reading.
type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) {
	return 0, errors.New("write /dev/stdout: broken pipe")
}
