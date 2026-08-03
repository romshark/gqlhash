// Package versioninfo writes what a command answers -version with:
// the name it ran as, its release version, and the notice they're released under.
//
// A package of its own rather than a copy in each command, so the two can't
// drift apart and the copyright is written once. It's a leaf: the hashing
// command and the proxy both reach it without one importing the other.
package versioninfo

import (
	"fmt"
	"io"
)

// Copyright is who holds the rights to what ran, and License names the terms.
// LICENSE carries them in full; a version notice names them and no more.
const (
	Copyright = "Copyright (c) 2026 Roman Scharkov (github.com/romshark/gqlhash)"
	License   = "MIT License"
)

// Print writes the version of the command that ran as name.
//
// The name and the version come first, alone on the first line, which is what a
// script reading `-version | head -1` takes and where every CLI that prints more
// than a version puts it. The notice follows.
//
// The build behind it — the Go version, the module, every dependency — is what
// `go version -m $(command -v gqlhash)` prints for any Go binary, in more detail
// than a command could, so neither of these prints it itself.
func Print(w io.Writer, name, version string) {
	_, _ = fmt.Fprintf(w, "%s v%s\n%s\n%s\n", name, version, Copyright, License)
}
