// Package acceptance is the conformance suite of the gqlhash proxy: what it
// does, stated against a running server rather than against the Go code serving
// it. A server that passes every test here answers like the original, so an
// implementation of it can be shown to be complete rather than argued to be.
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
// What an implementation has to do to be testable here:
//
//   - take -server.listen, -control.listen, -upstream.url and -allowlist,
//     and listen on the addresses it was given,
//   - shut down on SIGINT and exit 0.
//
// Nothing is asked of what it logs: a test learns it's serving by connecting to it.
//
// The tests that need no flags of their own share one server per target and
// publish the allowlist they need through its control server, so none of them
// may depend on having run after another. Run them with -shuffle=on,
// which the Makefile does, and one that does fails.
//
// The rest is the behavior under test.
package acceptance
