// Command gqlhash-proxy serves an allowlist of GraphQL documents in front of a
// GraphQL API. It forwards a request whose document is on the list and rejects
// every other request.
//
// The hashing command is gqlhash, a separate binary: this one holds no hashing
// command line interface.
package main

import (
	"context"
	"os"

	"github.com/romshark/gqlhash/v2/internal/app/proxy"
)

// Version is the release version, injected at build time by GoReleaser
// via -ldflags "-X main.Version=...". Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	os.Exit(proxy.Run(context.Background(), "gqlhash-proxy", Version, os.Args,
		os.Stdout, os.Stderr))
}
