// Package hasher is the hashing command line interface of gqlhash.
//
// The proxy is a separate binary, so nothing here links an HTTP server,
// a metrics client or a schema validator.
package hasher

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
	"github.com/romshark/gqlhash/v2/internal/app/versioninfo"
)

// Run hashes the document of stdin or of -file and writes the result to stdout.
//
// name and version are what -version reports, so the output names the binary
// the caller ran. args[0] is the command as invoked, as in [os.Args].
func Run(
	name, version string,
	args []string,
	stdout, stderr io.Writer,
	stdin io.Reader,
) (exitCode int) {
	cfg, code, run := config.ParseHasher(args[0], args, stderr)
	if !run {
		return code
	}
	if cfg.CmdPrintVersion {
		return printVersion(stdout, name, version)
	}

	var input []byte
	var err error
	source := "<stdin>"
	if cfg.File != "" {
		source = cfg.File
		if input, err = os.ReadFile(cfg.File); err != nil {
			_, _ = fmt.Fprintf(stderr, "error reading file %q: %v\n", cfg.File, err)
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

	h, ok := config.NewHasher(cfg.Hash)
	if !ok {
		// config.ParseHasher takes no other value, so this is a function added
		// to the vocabulary and not to the table that builds them.
		_, _ = fmt.Fprintf(stderr, "unsupported hash function: %d\n", cfg.Hash)
		return 1
	}
	sum, errHash := gqlhash.AppendHash(nil, h,
		gqlhash.Options{Ignore: cfg.Ignore, DepthLimit: cfg.DepthLimit}, input)
	if errHash.IsErr() {
		// A hash never fails a write, so this carries a position.
		// The format is the one editors and CI annotations parse.
		line, column := gqlhash.Position(input, errHash.ErrOffset)
		_, _ = fmt.Fprintf(stderr, "%s:%d:%d: syntax error: %v\n",
			source, line, column, errHash.Err)
		return 1
	}

	var encoded string
	switch cfg.Format {
	case config.FormatHex:
		encoded = hex.EncodeToString(sum)
	case config.FormatBase32:
		encoded = base32.StdEncoding.EncodeToString(sum)
	case config.FormatBase64:
		encoded = base64.StdEncoding.EncodeToString(sum)
	case config.FormatBase64URL:
		encoded = base64.URLEncoding.EncodeToString(sum)
	default:
		// config.ParseHasher takes no other value, so this is a format that was
		// added to the vocabulary and not to this switch.
		_, _ = fmt.Fprintf(stderr, "unsupported output format: %d\n", cfg.Format)
		return 1
	}

	// The hash and a newline, which is what every tool of this kind writes —
	// shasum, md5, git hash-object, openssl dgst — and what makes the output a
	// line: without it `read h < hash.txt` hands the hash over and reports
	// failure, a `while read` over the file runs no iteration at all, and two
	// hashes appended to one file run together. Command substitution strips it,
	// so `$(gqlhash …)` reads the same either way.
	//
	// One write, so a hash reaches a pipe whole.
	//
	// A failed write is what a closed pipe looks like: `gqlhash -file q.graphql
	// | head -1` leaves nobody to read the answer. An exit code like every other
	// failure here, rather than a goroutine dump over what the reader did get.
	if _, err = io.WriteString(stdout, encoded+"\n"); err != nil {
		_, _ = fmt.Fprintf(stderr, "writing the hash: %v\n", err)
		return 1
	}
	return 0
}

// printVersion answers -version, which the proxy command answers the same way,
// see [versioninfo.Print].
func printVersion(w io.Writer, name, version string) (exitCode int) {
	versioninfo.Print(w, name, version)
	return 0
}
