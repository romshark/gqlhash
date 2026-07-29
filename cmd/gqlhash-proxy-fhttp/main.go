// Command gqlhash-proxy-fhttp is gqlhash-proxy served with fasthttp instead of
// net/http. It takes the same flags, makes the same decision and answers the
// same way; what differs is the HTTP implementation carrying it.
//
// It's a binary of its own rather than a flag on gqlhash-proxy so that the
// default proxy links no HTTP implementation it doesn't serve with. Take this
// one when forwarded volume or the memory footprint is what you're sized for,
// and read GQLHASH_PROXY_FHTTP.md first: it gives up per-request
// cancellation, HTTP/2 and a well-worn HTTP parser in exchange.
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
