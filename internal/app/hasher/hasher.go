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
	"runtime/debug"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
)

// Run hashes the document of stdin or of -file and writes the result to stdout.
//
// name and version are what -version reports. Every command passes its own,
// so the output names the binary the caller ran and the version its build injected.
//
// args[0] is the name of the command as invoked, as in [os.Args].
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
		// config.ParseHasher takes no other value, so this is a function that was
		// added to the vocabulary and not to the table that builds them.
		_, _ = fmt.Fprintf(stderr, "unsupported hash function: %d\n", cfg.Hash)
		return 1
	}
	sum, errHash := gqlhash.AppendHash(nil, h,
		gqlhash.Options{Ignore: cfg.Ignore}, input)
	if errHash.IsErr() {
		// A hash never fails a write, so the error is a syntax error and carries
		// a position. The format is the one editors and CI annotations parse:
		// file:line:column: message.
		line, column := gqlhash.Position(input, errHash.Offset)
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

	// A write that fails is what a closed pipe looks like: `gqlhash -file q.graphql
	// | head -1` leaves nobody to read the answer. Every other failure of this
	// command is an exit code, and so is this one, rather than a goroutine dump
	// over whatever the reader did get.
	if _, err = io.WriteString(stdout, encoded); err != nil {
		_, _ = fmt.Fprintf(stderr, "writing the hash: %v\n", err)
		return 1
	}
	return 0
}

// printVersion writes the version and the build information to w.
func printVersion(w io.Writer, name, version string) (exitCode int) {
	_, _ = fmt.Fprintf(w, "%s v%s\n\n", name, version)
	_, _ = fmt.Fprintln(w, "MIT License")
	_, _ = fmt.Fprint(w, "Copyright (c) 2026 Roman Scharkov (github.com/romshark/gqlhash)\n\n")

	if info, ok := debug.ReadBuildInfo(); ok {
		_, _ = fmt.Fprintf(w, "%v\n", info)
	}

	return 0
}
