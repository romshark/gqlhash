// Command gqlhash-proxy-fhttp is gqlhash-proxy served with fasthttp instead of
// net/http. It takes the same flags, makes the same decision and answers the
// same way; what differs is the HTTP implementation carrying it.
//
// It's a binary of its own rather than a flag on gqlhash-proxy so that the
// default proxy links no HTTP implementation it doesn't serve with.
//
// EXPERIMENTAL, AND NOT FOR PRODUCTION USE. Run gqlhash-proxy in front of
// anything that matters. This command is for benchmarking, and for proving the
// acceptance suite holds two implementations to the same rules rather than
// describing one of them. It gives up per-request cancellation and HTTP/2, its
// parser is younger and far less exercised than net/http's, and a client's
// request trailer reaches the API as an ordinary header — which walks past the
// Connection-named-header check and cannot be closed on the fasthttp side.
// See GQLHASH_PROXY_FHTTP.md.
package main

import (
	"context"
	"os"

	"github.com/romshark/gqlhash/v2/internal/app/proxy"
	"github.com/romshark/gqlhash/v2/internal/app/proxyfast"
)

// Version is the release version, injected at build time by GoReleaser
// via -ldflags "-X main.Version=...". Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	os.Exit(proxy.RunWith(context.Background(), "gqlhash-proxy-fhttp", Version,
		os.Args, os.Stdout, os.Stderr, proxyfast.Underlay))
}
