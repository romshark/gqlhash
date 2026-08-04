package acceptance

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/romshark/gqlhash/v2/internal/app/versioninfo"
)

// The exit codes the commands use. A deployment reads these to tell a
// configuration it can fix from a start it should retry.
const (
	exitOK      = 0 // It did what was asked and stopped.
	exitFailed  = 1 // It couldn't start: the allowlist, or an address in use.
	exitBadArgs = 2 // The arguments are wrong, and no retry will help.
)

// TestCLIVersionAndHelp covers the two arguments that print and stop.
// Neither serves anything, so neither needs the flags a run requires.
func TestCLIVersionAndHelp(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// -version answers on stdout, which is what a pipeline reads.
		code, stdout, stderr := run(t, tgt, "-version")
		if code != exitOK {
			t.Errorf("-version: expected %d; received %d: %s", exitOK, code, stderr)
		}
		// The name and the version are alone on the first line, so
		// `-version | head -1` reads them whichever binary ran, and the notice follows.
		// A run under -proxy.bin may carry any name, so what's pinned is the shape:
		// `<name> v<version>` and the two lines under it.
		lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(lines) != 3 || !strings.Contains(lines[0], " v") {
			t.Errorf("-version: expected `<name> v<version>` and a notice under it;"+
				" received %q", stdout)
			return
		}
		if lines[1] != versioninfo.Copyright || lines[2] != versioninfo.License {
			t.Errorf("-version: expected the notice every command answers with;"+
				" received %q", stdout)
		}
		if stderr != "" {
			t.Errorf("-version: expected nothing on stderr; received %q", stderr)
		}

		// -help answers on stderr, leaving stdout for what a pipeline reads.
		code, stdout, stderr = run(t, tgt, "-help")
		if code != exitOK {
			t.Errorf("-help: expected %d; received %d", exitOK, code)
		}
		if stdout != "" {
			t.Errorf("-help: expected nothing on stdout; received %q", stdout)
		}
		for _, flag := range []string{"-upstream.url", "-allowlist", "-server.listen"} {
			if !strings.Contains(stderr, flag) {
				t.Errorf("-help: expected %s to be named; received %s", flag, stderr)
			}
		}
	})
}

// TestCLIRejectsArguments covers the arguments a run refuses to start on.
// Each is a configuration nobody should retry, so each is exit 2 with a reason.
//
// The reasons are this repository's own wording. Where the message comes from
// the flag library instead, only the code is asserted: an implementation in
// another language has a library of its own.
func TestCLIRejectsArguments(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// What a run needs, for the cases that are about something else.
		base := []string{"-upstream.url", "http://127.0.0.1:1/graphql", "-allowlist", dir}
		certFile, keyFile, _ := serverKeyPair(t)
		_, otherKey, _ := serverKeyPair(t)
		notPEM := filepath.Join(dir, "not.pem")
		if err := os.WriteFile(notPEM, []byte("no certificate here"), 0o600); err != nil {
			t.Fatal(err)
		}
		with := func(args ...string) []string {
			return append(append([]string{}, base...), args...)
		}

		for _, tc := range []struct {
			name   string
			args   []string
			reason string // The wording of this repository, where it's ours.
		}{
			// An argument that isn't a flag. A library that ignores trailing
			// arguments would serve with defaults nobody asked for.
			{"a positional argument", with("serve"), `unexpected argument "serve"`},
			{"a flag nobody defined", with("-nosuch"), ""},

			// What a run can't be assembled without.
			{"no upstream", []string{"-allowlist", dir}, "-upstream.url is required"},
			{
				"no allowlist",
				[]string{"-upstream.url", "http://api:4000/graphql"},
				"-allowlist is required",
			},
			{
				"a relative upstream",
				[]string{"-upstream.url", "api/graphql", "-allowlist", dir},
				"is no absolute URL",
			},
			{
				"no control address", with("-control.listen", ""),
				"-control.listen must name an address",
			},

			// A value outside what the flag takes.
			{
				"an ignore mode nobody has", with("-ignore", "bogus"),
				"unsupported ignore mode",
			},
			{
				"a log level nobody has", with("-log.level", "bogus"),
				"unsupported log level",
			},
			{
				"a negative lifetime", with("-upstream.max-conn-lifetime", "-1s"),
				"must be 0 or more",
			},

			// Relations between flags, which each value alone satisfies.
			{
				"a write timeout under the upstream one",
				with("-server.write-timeout", "5s", "-upstream.timeout", "30s"),
				"must be above -upstream.timeout",
			},
			{
				"fewer idle connections than per host",
				with("-upstream.max-idle-conns", "1",
					"-upstream.max-idle-conns-per-host", "64"),
				"-upstream.max-idle-conns must be 0 or at least",
			},
			{
				"a read timeout under the header one",
				with("-server.read-timeout", "1s",
					"-server.read-header-timeout", "10s"),
				"must be at least -server.read-header-timeout",
			},

			// A duration below zero, on each of the five that take one.
			// Zero leaves a timeout off; a negative one is a value nobody meant,
			// and accepting it would leave the timeout off just the same.
			{
				"a negative read timeout",
				with("-server.read-timeout", "-1s"), "must be 0 or more",
			},
			{
				"a negative read header timeout",
				with("-server.read-header-timeout", "-1s"), "must be 0 or more",
			},
			{
				"a negative write timeout",
				with("-server.write-timeout", "-1s"), "must be 0 or more",
			},
			{
				"a negative idle timeout",
				with("-server.idle-timeout", "-1s"), "must be 0 or more",
			},
			// The one where 0 doesn't mean "off" either: a shutdown waits that
			// long for the requests in flight, so both 0 and a negative value
			// abandon them.
			{
				"a negative shutdown timeout",
				with("-server.shutdown-timeout", "-1s"), "must be 0 or more",
			},

			// The TLS files, read at startup so a proxy never binds a port it
			// can't serve or an upstream it can't verify.
			{
				"a certificate without a key",
				with("-server.tls.cert", certFile), "go together",
			},
			{
				"a key without a certificate",
				with("-server.tls.key", keyFile), "go together",
			},
			{
				"a certificate that isn't there",
				with("-server.tls.cert", filepath.Join(dir, "absent.pem"),
					"-server.tls.key", keyFile), "-server.tls.cert",
			},
			{
				"a certificate and a key that aren't a pair",
				with("-server.tls.cert", certFile, "-server.tls.key", otherKey),
				"-server.tls.cert",
			},
			{
				"an upstream CA over http", with("-upstream.tls.ca", certFile),
				"no https URL",
			},
			{
				"an upstream CA holding no certificate",
				[]string{
					"-upstream.url", "https://api:4000/graphql",
					"-allowlist", dir, "-upstream.tls.ca", notPEM,
				},
				"holds no PEM certificate",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, stdout, stderr := run(t, tgt, tc.args...)
				if code != exitBadArgs {
					t.Errorf("expected %d; received %d: %s", exitBadArgs, code, stderr)
				}
				if stdout != "" {
					t.Errorf("expected nothing on stdout; received %q", stdout)
				}
				if strings.TrimSpace(stderr) == "" {
					t.Error("expected a reason on stderr")
				}
				if tc.reason != "" && !strings.Contains(stderr, tc.reason) {
					t.Errorf("expected %q; received %s", tc.reason, stderr)
				}
			})
		}
	})
}

// TestCLIRejectsForgeableHash covers the hash functions a proxy refuses.
// The allowlist is a set of hashes, so a function that isn't collision resistant is
// a forgeable allowlist: a document nobody allowed hashes to one that is.
//
// The hasher of gqlhash takes these; the proxy is where they're refused,
// so an implementation sharing one table between the two fails here.
func TestCLIRejectsForgeableHash(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		for _, name := range []string{"sha1", "md5", "xxhash", "crc32", "fnv"} {
			t.Run(name, func(t *testing.T) {
				code, _, stderr := run(t, tgt,
					"-upstream.url", "http://127.0.0.1:1/graphql",
					"-allowlist", dir, "-hash", name)
				if code != exitBadArgs {
					t.Errorf("expected %d; received %d: %s", exitBadArgs, code, stderr)
				}
				if !strings.Contains(stderr, "unsupported hash function") {
					t.Errorf("expected the reason; received %s", stderr)
				}
			})
		}
	})
}

// TestCLIVersionDoesNotBeatBadArguments pins the order: the arguments are
// parsed before -version is answered, so a command line that is wrong stays wrong.
// Answering the version first would hide a typo in a deployment that
// prints it at startup.
func TestCLIVersionDoesNotBeatBadArguments(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		code, stdout, stderr := run(t, tgt, "-version", "-nosuch")
		if code != exitBadArgs {
			t.Errorf("expected %d; received %d", exitBadArgs, code)
		}
		if stdout != "" {
			t.Errorf("expected no version; received %q", stdout)
		}
		if strings.TrimSpace(stderr) == "" {
			t.Error("expected a reason on stderr")
		}
	})
}

// TestCLIStartFailures covers what a run can't recover from once the arguments
// are right: an allowlist it can't read and an address it can't have.
// Each is exit 1, told apart from exit 2 so a supervisor knows a retry may work.
func TestCLIStartFailures(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Close() }()

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"an allowlist that isn't there", []string{
				"-upstream.url", "http://127.0.0.1:1/graphql",
				"-allowlist", dir + "/nope",
			}},
			{"a data-plane address in use", []string{
				"-upstream.url", "http://127.0.0.1:1/graphql", "-allowlist", dir,
				"-server.listen", held.Addr().String(),
				"-control.listen", "127.0.0.1:0",
			}},
			// The control server is no optional extra: a proxy that can't serve
			// /reload and /metrics doesn't serve at all.
			{"a control address in use", []string{
				"-upstream.url", "http://127.0.0.1:1/graphql", "-allowlist", dir,
				"-server.listen", "127.0.0.1:0",
				"-control.listen", held.Addr().String(),
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, stdout, stderr := run(t, tgt, tc.args...)
				if code != exitFailed {
					t.Errorf("expected %d; received %d: %s", exitFailed, code, stderr)
				}
				if stdout != "" {
					t.Errorf("expected nothing on stdout; received %q", stdout)
				}
				if strings.TrimSpace(stderr) == "" {
					t.Error("expected the failure to be reported on stderr")
				}
			})
		}
	})
}
