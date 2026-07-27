// Command gqlhash hashes GraphQL executable documents, ignoring differences in
// formatting.
//
// The proxy is gqlhash-proxy, a separate binary, so an HTTP server and a metrics
// client stay out of this one.
package main

import (
	"os"

	"github.com/romshark/gqlhash/v2/internal/app/hasher"
)

// Version is the release version, injected at build time by GoReleaser
// via -ldflags "-X main.Version=...". Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	os.Exit(hasher.Run("gqlhash", Version, os.Args, os.Stdout, os.Stderr, os.Stdin))
}
