package config_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
	"github.com/romshark/gqlhash/v2/parser"
)

// hasherArgs prefixes the command name the way a shell does.
func hasherArgs(a ...string) []string { return append([]string{"gqlhash"}, a...) }

// proxyArgs prefixes the command name.
func proxyArgs(a ...string) []string { return append([]string{"gqlhash-proxy"}, a...) }

func TestParseHasher(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseHasher("gqlhash", hasherArgs(), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected the defaults to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.File != "" || cfg.CmdPrintVersion {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Format != config.FormatHex {
		t.Errorf("expected hex by default; received %v", cfg.Format)
	}
	// sha2 by default, the same function the proxy defaults to and the narrowest
	// thing an allowlist needs of a hash. TestParseProxy pins the proxy's.
	if cfg.Hash != config.HashFunctionSHA2 {
		t.Errorf("expected sha2 by default; received %v", cfg.Hash)
	}
	if cfg.Ignore != gqlhash.IgnoreNothing {
		t.Errorf("expected nothing ignored by default; received %v", cfg.Ignore)
	}

	// Every flag reaches the config.
	errOut.Reset()
	cfg, code, run = config.ParseHasher("gqlhash", hasherArgs(
		"-file", "q.graphql", "-format", "base64url", "-hash", "blake3",
		"-ignore", "variables",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected these flags to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.File != "q.graphql" || cfg.Format != config.FormatBase64URL ||
		cfg.Hash != config.HashFunctionBLAKE3 ||
		cfg.Ignore != gqlhash.IgnoreVariables {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// A depth limit below 1 is the default, and the config carries that rather
	// than what was typed: it's the limit in force, and the proxy logs it.
	for _, given := range []string{"0", "-5"} {
		errOut.Reset()
		cfg, code, run = config.ParseHasher("gqlhash",
			hasherArgs("-depth-limit", given), &errOut)
		if !run || code != 0 {
			t.Fatalf("expected -depth-limit %s to parse; code %d, stderr: %s",
				given, code, errOut.String())
		}
		if cfg.DepthLimit != parser.DefaultDepthLimit {
			t.Errorf("expected -depth-limit %s to take the default %d; received %d",
				given, parser.DefaultDepthLimit, cfg.DepthLimit)
		}
	}
}

func TestParseHasherErrors(t *testing.T) {
	f := func(t *testing.T, expectCode int, expectStderr string, a ...string) {
		t.Helper()
		var errOut strings.Builder
		_, code, run := config.ParseHasher("gqlhash", hasherArgs(a...), &errOut)
		if run {
			t.Errorf("expected the caller to be done; args %v", a)
		}
		if code != expectCode {
			t.Errorf("expected code %d; received %d; args %v", expectCode, code, a)
		}
		if expectStderr != "" && !strings.Contains(errOut.String(), expectStderr) {
			t.Errorf("expected %q in stderr; received %q", expectStderr, errOut.String())
		}
	}

	// A help request is no error.
	f(t, 0, "", "-help")

	f(t, 2, "", "-nonexistent")
	f(t, 2, "unsupported format", "-format", "rot13")
	f(t, 2, "unsupported hash function", "-hash", "sha9")
	f(t, 2, "unsupported ignore mode", "-ignore", "everything")

	// A positional argument is rejected instead of being ignored,
	// and asking the hashing command for the proxy names the command that has it.
	f(t, 2, `unexpected argument "typo"`, "typo")
	f(t, 2, "the proxy is the "+config.ProxyCommand+" command", "proxy")
}

func TestParseProxy(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api:4000/graphql", "-allowlist", "./queries",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected the required flags to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Server.Listen != ":8080" || cfg.Upstream.URL.String() != "http://api:4000/graphql" ||
		cfg.AllowlistDir != "./queries" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.HashFunc != config.HashFunctionSHA2 {
		t.Errorf("expected sha2 by default; received %v", cfg.HashFunc)
	}
	if cfg.Server.MaxBody != 1<<20 || cfg.Upstream.Timeout != 30*time.Second ||
		cfg.Server.ShutdownTimeout != 10*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Server.ReadHeaderTimeout != 10*time.Second ||
		cfg.Server.ReadTimeout != 30*time.Second ||
		cfg.Server.IdleTimeout != 120*time.Second {
		t.Errorf("unexpected listener timeouts: %+v", cfg)
	}
	// An unset write timeout follows -upstream.timeout.
	if cfg.Server.WriteTimeout != cfg.Upstream.Timeout+10*time.Second {
		t.Errorf("expected the write timeout to follow -upstream.timeout; received %v",
			cfg.Server.WriteTimeout)
	}
	// The control server has no off switch, so its address has a default.
	if cfg.Control.Address != "127.0.0.1:9090" || cfg.Control.Token != "" {
		t.Errorf("unexpected control defaults: %+v", cfg)
	}
	if cfg.Upstream.MaxIdleConnsPerHost != 64 || cfg.Upstream.MaxIdleConns != 256 || !cfg.Upstream.HTTP2 {
		t.Errorf("unexpected upstream defaults: %+v", cfg)
	}
	if !cfg.Log.JSON || cfg.Log.Level != "info" {
		t.Errorf("unexpected log defaults: %+v", cfg)
	}
	// Batching is off by default, which is 0 documents rather than a switch.
	if cfg.Server.MaxBatch != 0 {
		t.Errorf("expected no batching by default; received %d", cfg.Server.MaxBatch)
	}
	for _, off := range []bool{
		cfg.OpaqueErrors,
		cfg.Log.Requests, cfg.TrustForwarded, cfg.CmdPrintVersion,
	} {
		if off {
			t.Errorf("expected every switch off by default: %+v", cfg)
			break
		}
	}

	// Every flag reaches the config.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-server.listen", "127.0.0.1:1", "-upstream.url", "https://api/graphql",
		"-allowlist", "./q", "-control.listen", "127.0.0.1:3",
		"-hash", "blake3", "-ignore", "inputs",
		"-server.max-body", "4096", "-server.max-batch", "5", "-opaque-errors",
		"-log.requests", "-trust-forwarded", "-log.level", "debug",
		"-log.json=false", "-upstream.timeout", "1s",
		"-server.shutdown-timeout", "2s", "-server.read-header-timeout", "3s",
		"-server.read-timeout", "17s", "-server.write-timeout", "23s",
		"-server.idle-timeout", "29s",
		"-upstream.max-idle-conns-per-host", "8", "-upstream.max-idle-conns", "16",
		"-upstream.http2=false",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected these flags to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	want := config.Proxy{
		AllowlistDir: "./q",
		HashFunc:     config.HashFunctionBLAKE3,
		Ignore:       gqlhash.IgnoreInputs,
		OpaqueErrors: true, TrustForwarded: true,
		Server: config.ProxyServer{
			Listen:            "127.0.0.1:1",
			MaxBody:           4096,
			MaxBatch:          5,
			ShutdownTimeout:   2 * time.Second,
			ReadHeaderTimeout: 3 * time.Second,
			ReadTimeout:       17 * time.Second,
			WriteTimeout:      23 * time.Second,
			IdleTimeout:       29 * time.Second,
		},
		Upstream: config.ProxyUpstream{
			Timeout:             time.Second,
			MaxIdleConnsPerHost: 8,
			MaxIdleConns:        16,
			HTTP2:               false,
		},
		DepthLimit: parser.DefaultDepthLimit,
		Control:    config.ProxyControl{Address: "127.0.0.1:3"},
		Log: config.ProxyLog{
			Level: "debug", JSON: false, Requests: true,
		},
	}
	want.Upstream.URL = cfg.Upstream.URL // Compared separately, it's a pointer.
	if cfg != want {
		t.Errorf("expected %+v; received %+v", want, cfg)
	}
	if cfg.Upstream.URL.String() != "https://api/graphql" {
		t.Errorf("unexpected upstream: %s", cfg.Upstream.URL)
	}

	// The URL is kept as it was given, the query of an endpoint included:
	// it's merged into every forwarded request rather than replaced.
	// A fragment is kept and ignored, as it is by every HTTP client.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "https://api/graphql?env=staging&key=abc#frag",
		"-allowlist", "./q",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected an endpoint query to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Upstream.URL.RawQuery != "env=staging&key=abc" {
		t.Errorf("expected the endpoint query kept; received %q",
			cfg.Upstream.URL.RawQuery)
	}

	// A zero timeout is no timeout at all, which is allowed and doesn't fall back
	// to following -upstream.timeout.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
		"-server.write-timeout", "0", "-server.read-timeout", "0",
		"-server.idle-timeout", "0",
		"-server.read-header-timeout", "0",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected zero timeouts to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Server.WriteTimeout != 0 || cfg.Server.ReadTimeout != 0 || cfg.Server.IdleTimeout != 0 ||
		cfg.Server.ReadHeaderTimeout != 0 {
		t.Errorf("expected every timeout off; received %+v", cfg)
	}

	// An unbounded upstream leaves the write timeout off too,
	// rather than deriving the 10s that would cut off every answer slower than that —
	// and cut it off as a dropped connection, the write deadline having passed.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
		"-upstream.timeout", "0",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected a zero upstream timeout to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Server.WriteTimeout != 0 {
		t.Errorf("expected the write timeout off where -upstream.timeout is; "+
			"received %v", cfg.Server.WriteTimeout)
	}

	// A depth limit below 1 is the default here too, so the startup log reports
	// the limit the proxy applies rather than the number it was given.
	for _, given := range []string{"0", "-5"} {
		errOut.Reset()
		cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
			"-upstream.url", "http://api/graphql", "-allowlist", "./q",
			"-depth-limit", given,
		), &errOut)
		if !run || code != 0 {
			t.Fatalf("expected -depth-limit %s to parse; code %d, stderr: %s",
				given, code, errOut.String())
		}
		if cfg.DepthLimit != parser.DefaultDepthLimit {
			t.Errorf("expected -depth-limit %s to take the default %d; received %d",
				given, parser.DefaultDepthLimit, cfg.DepthLimit)
		}
	}
}

func TestParseProxyErrors(t *testing.T) {
	f := func(t *testing.T, expectCode int, expectStderr string, a ...string) {
		t.Helper()
		var errOut strings.Builder
		_, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(a...), &errOut)
		if run {
			t.Errorf("expected the caller to be done; args %v", a)
		}
		if code != expectCode {
			t.Errorf("expected code %d; received %d; args %v", expectCode, code, a)
		}
		if expectStderr != "" && !strings.Contains(errOut.String(), expectStderr) {
			t.Errorf("expected %q in stderr; received %q", expectStderr, errOut.String())
		}
	}

	f(t, 0, "", "-help")
	f(t, 2, "", "-nonexistent")
	f(t, 2, "-upstream.url is required")
	f(t, 2, "no absolute URL", "-upstream.url", "not-a-url")
	f(t, 2, "no absolute URL", "-upstream.url", "/only/a/path")
	f(t, 2, "-allowlist is required", "-upstream.url", "http://x")

	// Credentials reach no request, so they're refused rather than dropped.
	f(t, 2, "must carry no credentials",
		"-upstream.url", "http://user:pass@api/graphql", "-allowlist", ".")
	f(t, 2, "must carry no credentials",
		"-upstream.url", "http://user@api/graphql", "-allowlist", ".")
	// Percent-encoded userinfo is userinfo, and an @ in the path is not.
	f(t, 2, "must carry no credentials",
		"-upstream.url", "http://us%65r:p%61ss@api/graphql", "-allowlist", ".")
	// The message refusing a password doesn't echo it.
	var errPassword strings.Builder
	_, _, _ = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://user:hunter2@api/graphql", "-allowlist", ".",
	), &errPassword)
	if strings.Contains(errPassword.String(), "hunter2") {
		t.Errorf("expected the password redacted; received %q", errPassword.String())
	}

	const ok = "-upstream.url"

	// The proxy takes only the collision-resistant functions, though the hashing
	// form offers all twelve.
	for _, weak := range []string{
		"sha1", "md5", "crc32", "crc64", "fnv", "fnv1a",
		"xxh64",
	} {
		f(t, 2, "unsupported hash function", ok, "http://x", "-allowlist", ".",
			"-hash", weak)
		if config.ParseHashFunction(weak) == 0 {
			t.Errorf("expected gqlhash to still offer %q", weak)
		}
	}
	f(t, 2, "unsupported ignore mode", ok, "http://x", "-allowlist", ".",
		"-ignore", "everything")
	// The control server can't be turned off.
	f(t, 2, "-control.listen must name an address",
		ok, "http://x", "-allowlist", ".", "-control.listen", "")
	f(t, 2, `unexpected argument "typo"`, "typo")
	// The hint the hasher gives would send a caller to the command they already
	// ran, so the proxy treats "proxy" as the argument it doesn't take.
	f(t, 2, `unexpected argument "proxy"`, "proxy")

	// A write timeout at or below -upstream.timeout would cut off a response
	// the upstream is still allowed to be sending.
	f(t, 2, "-server.write-timeout 5s must be above -upstream.timeout 30s",
		ok, "http://x", "-allowlist", ".", "-server.write-timeout", "5s")
	f(t, 2, "must be above -upstream.timeout",
		ok, "http://x", "-allowlist", ".", "-server.write-timeout", "30s")
	// A read timeout below the header timeout would decide first.
	f(t, 2, "-server.read-timeout 1s must be at least -server.read-header-timeout 10s",
		ok, "http://x", "-allowlist", ".", "-server.read-timeout", "1s")
	f(t, 2, "-server.idle-timeout must be 0 or more",
		ok, "http://x", "-allowlist", ".", "-server.idle-timeout", "-1s")
	// A negative upstream timeout would reach the write timeout derived from it.
	f(t, 2, "-upstream.timeout must be 0 or more",
		ok, "http://x", "-allowlist", ".", "-upstream.timeout", "-5s")
	// A negative shutdown timeout is the one 0 doesn't leave off:
	// both wait for nothing, so a shutdown abandons the requests it was still serving.
	f(t, 2, "-server.shutdown-timeout must be 0 or more",
		ok, "http://x", "-allowlist", ".", "-server.shutdown-timeout", "-1s")

	// A body limit below 1 refuses every POST, an empty one included.
	f(t, 2, "-server.max-body must be 1 or more",
		ok, "http://x", "-allowlist", ".", "-server.max-body", "0")
	f(t, 2, "-server.max-body must be 1 or more",
		ok, "http://x", "-allowlist", ".", "-server.max-body", "-1")

	// An idle pool of zero would fall back to the two connections of the standard
	// library, which is no pool at this rate.
	f(t, 2, "-upstream.max-idle-conns-per-host must be 1 or more",
		ok, "http://x", "-allowlist", ".", "-upstream.max-idle-conns-per-host", "0")
	// A total below the per-host limit caps it, so it's rejected instead
	// of leaving the per-host value without effect.
	f(t, 2, "-upstream.max-idle-conns must be 0 or at least",
		ok, "http://x", "-allowlist", ".", "-upstream.max-idle-conns", "8")
}

// TestParseProxyEnv covers the environment form of the flags.
// The proxy is configured by a deployment, which has no command line to edit.
func TestParseProxyEnv(t *testing.T) {
	t.Setenv(config.EnvName("server.listen"), "127.0.0.1:9")
	t.Setenv(config.EnvName("upstream.url"), "http://from-env/graphql")
	t.Setenv(config.EnvName("allowlist"), "/queries")
	t.Setenv(config.EnvName("server.max-body"), "4096")
	// The token has no flag, so the environment is the only way to give it. It's
	// trimmed, since a secret file or a here-doc carries a newline.
	t.Setenv(config.EnvName("control.token"), "  from-the-environment\n")
	t.Setenv(config.EnvName("upstream.max-idle-conns-per-host"), "128")

	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected the environment to stand in for the flags; "+
			"code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Server.Listen != "127.0.0.1:9" || cfg.AllowlistDir != "/queries" ||
		cfg.Server.MaxBody != 4096 || cfg.Upstream.MaxIdleConnsPerHost != 128 ||
		cfg.Control.Token != "from-the-environment" ||
		cfg.Upstream.URL.String() != "http://from-env/graphql" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// A flag on the command line wins over the environment.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy",
		proxyArgs("-server.listen", "127.0.0.1:10"), &errOut)
	if !run || code != 0 {
		t.Fatalf("code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Server.Listen != "127.0.0.1:10" || cfg.AllowlistDir != "/queries" {
		t.Errorf("expected the flag to win and the rest to stand: %+v", cfg)
	}

	// A value the flag can't take names the variable it came from.
	t.Setenv(config.EnvName("server.max-body"), "not-a-number")
	errOut.Reset()
	if _, gotCode, gotRun := config.ParseProxy("gqlhash-proxy", proxyArgs(),
		&errOut); gotRun || gotCode != 2 {
		t.Errorf("expected a bad value to fail; code %d run %t", gotCode, gotRun)
	} else if !strings.Contains(errOut.String(), config.EnvName("server.max-body")) {
		t.Errorf("expected the variable to be named; received %q", errOut.String())
	}

	// The hashing command reads no environment: a variable that changed -hash
	// there would change what a pipeline produces with nothing to show for it.
	t.Setenv("GQLHASH_HASH", "blake3")
	t.Setenv(config.EnvName("hash"), "blake3")
	errOut.Reset()
	hasher, code, run := config.ParseHasher("gqlhash", hasherArgs(), &errOut)
	if !run || code != 0 {
		t.Fatalf("code %d, stderr: %s", code, errOut.String())
	}
	if hasher.Hash != config.HashFunctionSHA2 {
		t.Errorf("expected the default to stand; received %v", hasher.Hash)
	}
}

func TestEnvName(t *testing.T) {
	for flag, want := range map[string]string{
		"server.listen":                    "GQLHASH_PROXY_SERVER_LISTEN",
		"server.max-body":                  "GQLHASH_PROXY_SERVER_MAX_BODY",
		"upstream.max-idle-conns-per-host": "GQLHASH_PROXY_UPSTREAM_MAX_IDLE_CONNS_PER_HOST",
	} {
		if got := config.EnvName(flag); got != want {
			t.Errorf("-%s: expected %q; received %q", flag, want, got)
		}
	}
}

// TestFlagInventory pins the flag set of both commands.
// A new flag fails this test until it's listed here, which is the reminder to cover it.
func TestFlagInventory(t *testing.T) {
	f := func(
		t *testing.T,
		parse func(string, []string, *strings.Builder) (int, bool),
		args []string,
		defaults map[string]string,
	) {
		t.Helper()
		var errOut strings.Builder
		if code, run := parse("gqlhash", args, &errOut); run || code != 0 {
			t.Fatalf("expected -help to end the call with 0; code %d run %t",
				code, run)
		}
		usage := errOut.String()
		for name, def := range defaults {
			if !strings.Contains(usage, "  -"+name) {
				t.Errorf("flag -%s is missing from the usage", name)
			}
			if def != "" && !strings.Contains(usage, "(default "+def+")") {
				t.Errorf("flag -%s: default %s is missing from the usage", name, def)
			}
		}
		if n := strings.Count(usage, "\n  -"); n != len(defaults) {
			t.Errorf("expected %d flags; the usage lists %d:\n%s",
				len(defaults), n, usage)
		}
	}

	f(t, func(n string, a []string, w *strings.Builder) (int, bool) {
		_, code, run := config.ParseHasher(n, a, w)
		return code, run
	}, hasherArgs("-help"), map[string]string{
		"depth-limit": "128",
		"file":        "",
		"format":      `"hex"`,
		"hash":        `"sha2"`,
		"ignore":      `"nothing"`,
		"version":     "",
	})

	f(t, func(n string, a []string, w *strings.Builder) (int, bool) {
		_, code, run := config.ParseProxy(n, a, w)
		return code, run
	}, proxyArgs("-help"), map[string]string{
		// The flag package prints no default for a zero int,
		// and the help text says what 0 means, see -server.max-batch.
		"server.max-batch":           "",
		"allowlist":                  "",
		"depth-limit":                "128",
		"hash":                       `"sha2"`,
		"ignore":                     `"nothing"`,
		"server.listen":              `":8080"`,
		"log.json":                   "true",
		"log.level":                  `"info"`,
		"log.requests":               "",
		"server.max-body":            "1048576",
		"opaque-errors":              "",
		"server.shutdown-timeout":    "10s",
		"server.tls.cert":            "",
		"server.tls.key":             "",
		"upstream.timeout":           "30s",
		"server.read-header-timeout": "10s",
		"server.read-timeout":        "30s",
		"server.write-timeout":       "",
		"server.idle-timeout":        "2m0s",
		"trust-forwarded":            "",
		"upstream.url":               "",
		"version":                    "",
		"control.listen":             `"127.0.0.1:9090"`,

		"upstream.max-idle-conns":          "256",
		"upstream.max-idle-conns-per-host": "64",
		"upstream.max-conn-lifetime":       "",
		"upstream.http2":                   "true",
		"upstream.tls.ca":                  "",
	})
}

func TestNames(t *testing.T) {
	// Every supported name round-trips through the parse and back.
	for name := range strings.SplitSeq(config.SupportedHashFunctions, ", ") {
		if got := config.HashName(config.ParseHashFunction(name)); got != name {
			t.Errorf("expected %q; received %q", name, got)
		}
	}
	if got := config.HashName(0); got != "" {
		t.Errorf("expected no name for the zero value; received %q", got)
	}

	for name := range strings.SplitSeq(config.SupportedIgnoreModes, ", ") {
		mode, ok := config.ParseIgnore(name)
		if !ok {
			t.Fatalf("expected %q to parse", name)
		}
		if got := config.IgnoreName(mode); got != name {
			t.Errorf("expected %q; received %q", name, got)
		}
	}
}

// TestVersionPrecedence pins that -version needs nothing else to be valid:
// it parses, it leaves the printing to the caller, and it wins over a
// bad value elsewhere and over the flags a proxy run would otherwise require.
// Both forms answer who the binary is without doing any work.
func TestVersionPrecedence(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseHasher("gqlhash",
		hasherArgs("-version", "-hash", "sha9", "-format", "rot13"), &errOut)
	if !run || code != 0 || !cfg.CmdPrintVersion {
		t.Errorf("hasher: expected -version to win; code %d run %t stderr %q",
			code, run, errOut.String())
	}

	errOut.Reset()
	proxyCfg, code, run := config.ParseProxy("gqlhash",
		proxyArgs("-version", "-hash", "sha9"), &errOut)
	if !run || code != 0 || !proxyCfg.CmdPrintVersion {
		t.Errorf("proxy: expected -version to win; code %d run %t stderr %q",
			code, run, errOut.String())
	}

	// A broken flag still loses to nothing: -version is a flag, not an escape.
	errOut.Reset()
	if _, code, run := config.ParseHasher("gqlhash",
		hasherArgs("-version", "-nonexistent"), &errOut); run || code != 2 {
		t.Errorf("expected an unknown flag to fail; code %d run %t", code, run)
	}
}

// TestParseProxyForHTTP1Only covers a command whose
// HTTP implementation can't speak h2.
// Asking for it stops the run rather than quietly serving something else, and
// leaving the flag alone turns it off so what's logged is what's served.
func TestParseProxyForHTTP1Only(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseProxyFor(true, "gqlhash-proxy-fhttp",
		proxyArgs("-upstream.url", "http://api/graphql", "-allowlist", "./q"),
		&errOut)
	if !run || code != 0 {
		t.Fatalf("expected it to parse without -upstream.http2; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Upstream.HTTP2 {
		t.Error("expected h2 off where the server can't speak it")
	}

	// Asking for it explicitly stops the run and says where h2 belongs.
	errOut.Reset()
	if _, code, run = config.ParseProxyFor(true, "gqlhash-proxy-fhttp",
		proxyArgs("-upstream.url", "http://api/graphql", "-allowlist", "./q",
			"-upstream.http2=true"), &errOut); run || code != 2 {
		t.Fatalf("expected -upstream.http2 to be refused; code %d run %t", code, run)
	}
	for _, want := range []string{
		"gqlhash-proxy-fhttp", "HTTP/1.1 only",
		"Terminate HTTP/2 ahead of this proxy", "-upstream.http2=false",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("expected %q in the refusal; received %q", want, errOut.String())
		}
	}

	// Turning it off explicitly is the way to ask for this command knowingly.
	errOut.Reset()
	if _, code, run = config.ParseProxyFor(true, "gqlhash-proxy-fhttp",
		proxyArgs("-upstream.url", "http://api/graphql", "-allowlist", "./q",
			"-upstream.http2=false"), &errOut); !run || code != 0 {
		t.Fatalf("expected -upstream.http2=false to parse; code %d, stderr: %s",
			code, errOut.String())
	}

	// The default command has no such restriction.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy",
		proxyArgs("-upstream.url", "http://api/graphql", "-allowlist", "./q"),
		&errOut)
	if !run || code != 0 {
		t.Fatalf("expected the default to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if !cfg.Upstream.HTTP2 {
		t.Errorf("expected nethttp with h2 left on; received %+v", cfg.Server)
	}
}

// TestParseProxyMaxConnLifetime covers the flag that lets an upstream pool
// follow a name that stands for more than one backend.
func TestParseProxyMaxConnLifetime(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
		"-upstream.max-conn-lifetime", "30s",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected it to parse; code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Upstream.MaxConnLifetime != 30*time.Second {
		t.Errorf("expected 30s; received %v", cfg.Upstream.MaxConnLifetime)
	}

	// Off by default: a single upstream has nothing to rebalance onto.
	errOut.Reset()
	if cfg, _, _ = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
	), &errOut); cfg.Upstream.MaxConnLifetime != 0 {
		t.Errorf("expected it off by default; received %v", cfg.Upstream.MaxConnLifetime)
	}

	// A negative lifetime is a mistake, not "off".
	errOut.Reset()
	if _, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
		"-upstream.max-conn-lifetime", "-1s",
	), &errOut); run || code != 2 {
		t.Fatalf("expected a negative lifetime to be refused; code %d run %t", code, run)
	}
	if !strings.Contains(errOut.String(), "-upstream.max-conn-lifetime must be 0 or more") {
		t.Errorf("unexpected message: %s", errOut.String())
	}
}

// TestParseProxyTLSCA covers -upstream.tls.ca: the certificates are read at startup,
// so a file that can't be used is a start failure and not an upstream
// that turns out to be unreachable at the first forward.
func TestParseProxyTLSCA(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "ca.pem")
	writeCA(t, good)
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("this is no certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A readable PEM against an https upstream is the case the flag exists for.
	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "https://api/graphql", "-allowlist", "./q",
		"-upstream.tls.ca", good,
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected a PEM file to parse; code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Upstream.TLSCA == nil {
		t.Error("expected the certificates to be read")
	}
	if config.TLSClientConfig(cfg.Upstream.TLSCA) == nil {
		t.Error("expected a TLS config carrying them")
	}

	// Unset, the host's trust store answers,
	// which is a nil config rather than an empty pool trusting nothing.
	cfg, _, _ = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "https://api/graphql", "-allowlist", "./q",
	), &strings.Builder{})
	if cfg.Upstream.TLSCA != nil || config.TLSClientConfig(cfg.Upstream.TLSCA) != nil {
		t.Error("expected no CA and no TLS config without the flag")
	}

	f := func(t *testing.T, expectStderr string, a ...string) {
		t.Helper()
		var errOut strings.Builder
		_, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(a...), &errOut)
		if run || code != 2 {
			t.Errorf("expected a start failure; code %d, args %v", code, a)
		}
		if !strings.Contains(errOut.String(), expectStderr) {
			t.Errorf("expected %q in stderr; received %q", expectStderr, errOut.String())
		}
	}
	const url, list = "-upstream.url", "-allowlist"
	f(t, "-upstream.tls.ca", url, "https://api/graphql", list, "./q",
		"-upstream.tls.ca", filepath.Join(dir, "absent.pem"))
	f(t, "holds no PEM certificate", url, "https://api/graphql", list, "./q",
		"-upstream.tls.ca", notPEM)
	// Nothing is verified over http, so naming a CA there is a mistake worth
	// reporting rather than a setting that quietly does nothing.
	f(t, "no https URL", url, "http://api/graphql", list, "./q",
		"-upstream.tls.ca", good)
}

// writeCA writes a self-signed certificate to path,
// which is all the flag reads: it never has to verify anything.
func writeCA(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gqlhash test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestParseProxyServerTLS covers -server.tls.cert and -server.tls.key:
// they go together, and the pair is loaded at startup so a proxy that can't
// serve what it was told to serve never binds.
func TestParseProxyServerTLS(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "s.pem"), filepath.Join(dir, "s.key")
	writeKeyPair(t, cert, key)
	otherCert, otherKey := filepath.Join(dir, "o.pem"), filepath.Join(dir, "o.key")
	writeKeyPair(t, otherCert, otherKey)

	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
		"-server.tls.cert", cert, "-server.tls.key", key,
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected a key pair to parse; code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Server.TLSCert == nil {
		t.Error("expected the certificate to be loaded")
	}
	if config.TLSServerConfig(cfg.Server.TLSCert) == nil {
		t.Error("expected a TLS config carrying it")
	}

	// Neither flag is plain HTTP, which is the default and no error.
	cfg, _, _ = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream.url", "http://api/graphql", "-allowlist", "./q",
	), &strings.Builder{})
	if cfg.Server.TLSCert != nil || config.TLSServerConfig(cfg.Server.TLSCert) != nil {
		t.Error("expected no certificate and no TLS config without the flags")
	}

	f := func(t *testing.T, expectStderr string, a ...string) {
		t.Helper()
		var errOut strings.Builder
		_, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(a...), &errOut)
		if run || code != 2 {
			t.Errorf("expected a start failure; code %d, args %v", code, a)
		}
		if !strings.Contains(errOut.String(), expectStderr) {
			t.Errorf("expected %q in stderr; received %q", expectStderr, errOut.String())
		}
	}
	const url, list = "-upstream.url", "-allowlist"
	base := []string{url, "http://api/graphql", list, "./q"}
	// One without the other serves neither HTTP nor HTTPS as asked for.
	f(t, "go together", append(append([]string{}, base...), "-server.tls.cert", cert)...)
	f(t, "go together", append(append([]string{}, base...), "-server.tls.key", key)...)
	f(t, "-server.tls.cert", append(append([]string{}, base...),
		"-server.tls.cert", filepath.Join(dir, "absent.pem"), "-server.tls.key", key)...)
	// A certificate and a key that aren't a pair.
	f(t, "-server.tls.cert", append(append([]string{}, base...),
		"-server.tls.cert", cert, "-server.tls.key", otherKey)...)
}

// writeKeyPair writes a self-signed certificate for 127.0.0.1 and its key.
func writeKeyPair(t *testing.T, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "gqlhash test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, blockType string, b []byte) {
		if err := os.WriteFile(path,
			pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: b}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(certPath, "CERTIFICATE", der)
	write(keyPath, "PRIVATE KEY", keyDER)
}
