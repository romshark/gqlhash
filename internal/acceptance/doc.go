// Package acceptance is the conformance suite of the gqlhash proxy: what it
// does, stated against a running server rather than the Go code serving it.
// A server that passes every test here answers like the original,
// so an implementation can be shown complete rather than argued to be.
//
// Every test starts a real process on ports of its own and talks to it over
// HTTP. Nothing here imports the implementation, so a server written in another
// language is tested the same way:
//
//	go test ./internal/acceptance -proxy.bin /path/to/your-proxy
//
// Without -proxy.bin the two commands of this repository are built and both are
// run through everything here, which is what makes the pair answer alike.
//
// The second of them, gqlhash-proxy-fhttp, is experimental and not a build to deploy,
// and running it doubles what the suite costs. -proxy.fhttp=false leaves it out:
//
//	go test ./internal/acceptance -proxy.fhttp=false   # or: make acceptance FHTTP=0
//
// Every rule it keeps is one gqlhash-proxy keeps too, so a run without it still
// covers every rule — what it stops covering is whether the two agree, which is
// the reason the pair is here at all. Leave it out while working on a rule;
// leave it in before committing one.
// It's ignored where -proxy.bin names the targets outright.
//
// What an implementation has to do to be testable here:
//
//   - take -server.listen, -control.listen, -upstream.url and -allowlist,
//     and listen on the addresses it was given,
//   - shut down on SIGINT and exit 0.
//
// Nothing is asked of what it logs: a test learns it's serving by connecting to it.
//
// The tests that need no flags of their own share one server per target and
// publish the allowlist they need through its control server, so none may
// depend on having run after another. Under -shuffle=on,
// which the Makefile passes, one that does fails.
//
// The rest is the behavior under test.
package acceptance
