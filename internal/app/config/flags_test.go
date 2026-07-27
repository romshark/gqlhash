package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
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
	if cfg.File != "" || cfg.Version {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Format != config.FormatHex {
		t.Errorf("expected hex by default; received %v", cfg.Format)
	}
	if cfg.Hash != config.HashFunctionSHA1 {
		t.Errorf("expected sha1 by default; received %v", cfg.Hash)
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

	// -version parses and leaves the printing to the caller.
	errOut.Reset()
	cfg, code, run = config.ParseHasher("gqlhash", hasherArgs("-version"), &errOut)
	if !run || code != 0 || !cfg.Version {
		t.Errorf("expected -version to parse; %+v code %d run %t", cfg, code, run)
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

	// A positional argument is rejected instead of being ignored, and asking the
	// hashing command for the proxy names the command that has it.
	f(t, 2, `unexpected argument "typo"`, "typo")
	f(t, 2, "gqlhash-proxy command", "proxy")
}

func TestParseProxy(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-upstream", "http://api:4000/graphql", "-allowlist", "./queries",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected the required flags to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	if cfg.Listen != ":8080" || cfg.Upstream.String() != "http://api:4000/graphql" ||
		cfg.Allowlist != "./queries" {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if cfg.Hash != config.HashFunctionSHA2 {
		t.Errorf("expected sha2 by default; received %v", cfg.Hash)
	}
	if cfg.MaxBody != 1<<20 || cfg.Timeout != 30*time.Second ||
		cfg.Shutdown != 10*time.Second || cfg.WatchInterval != 2*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.MaxIdleConnsPerHost != 64 || cfg.MaxIdleConns != 256 || !cfg.HTTP2 {
		t.Errorf("unexpected upstream defaults: %+v", cfg)
	}
	if !cfg.LogJSON || cfg.LogLevel != "info" {
		t.Errorf("unexpected log defaults: %+v", cfg)
	}
	for _, off := range []bool{
		cfg.Watch, cfg.Exact, cfg.AllowBatch, cfg.OpaqueErrors,
		cfg.LogRequests, cfg.TrustForwarded, cfg.Version,
	} {
		if off {
			t.Errorf("expected every switch off by default: %+v", cfg)
			break
		}
	}

	// Every flag reaches the config.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs(
		"-listen", "127.0.0.1:1", "-upstream", "https://api/graphql",
		"-allowlist", "./q", "-watch", "-watch-interval", "5s",
		"-hash", "blake3", "-ignore", "inputs", "-exact",
		"-max-body", "4096", "-allow-batch", "-opaque-errors",
		"-log-requests", "-trust-forwarded", "-log-level", "debug",
		"-log-json=false", "-timeout", "1s", "-status", "/healthz",
		"-metrics", "127.0.0.1:2", "-shutdown-timeout", "2s",
		"-upstream-max-idle-conns-per-host", "8", "-upstream-max-idle-conns", "16",
		"-upstream-http2=false",
	), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected these flags to parse; code %d, stderr: %s",
			code, errOut.String())
	}
	want := config.Proxy{
		Listen: "127.0.0.1:1", Allowlist: "./q", Watch: true,
		WatchInterval: 5 * time.Second, Hash: config.HashFunctionBLAKE3,
		Ignore: gqlhash.IgnoreInputs, Exact: true, MaxBody: 4096,
		AllowBatch: true, OpaqueErrors: true, LogRequests: true,
		TrustForwarded: true, LogLevel: "debug", LogJSON: false,
		Timeout: time.Second, Shutdown: 2 * time.Second, Status: "/healthz",
		Metrics: "127.0.0.1:2", MaxIdleConnsPerHost: 8, MaxIdleConns: 16,
		HTTP2: false,
	}
	want.Upstream = cfg.Upstream // Compared separately, it's a pointer.
	if cfg != want {
		t.Errorf("expected %+v; received %+v", want, cfg)
	}
	if cfg.Upstream.String() != "https://api/graphql" {
		t.Errorf("unexpected upstream: %s", cfg.Upstream)
	}

	// -version parses without the flags a run would need.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy", proxyArgs("-version"), &errOut)
	if !run || code != 0 || !cfg.Version {
		t.Errorf("expected -version to parse; %+v code %d run %t", cfg, code, run)
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
	f(t, 2, "-upstream is required")
	f(t, 2, "no absolute URL", "-upstream", "not-a-url")
	f(t, 2, "no absolute URL", "-upstream", "/only/a/path")
	f(t, 2, "-allowlist is required", "-upstream", "http://x")

	const ok = "-upstream"
	// The proxy takes only the collision-resistant functions, though the hashing
	// form offers all twelve.
	for _, weak := range []string{"sha1", "md5", "crc32", "crc64", "fnv", "fnv1a",
		"xxh64"} {
		f(t, 2, "unsupported hash function", ok, "http://x", "-allowlist", ".",
			"-hash", weak)
		if config.ParseHashFunction(weak) == 0 {
			t.Errorf("expected gqlhash to still offer %q", weak)
		}
	}
	f(t, 2, "unsupported ignore mode", ok, "http://x", "-allowlist", ".",
		"-ignore", "everything")
	f(t, 2, `unexpected argument "typo"`, "typo")

	// An idle pool of zero would fall back to the two connections of the standard
	// library, which is no pool at this rate.
	f(t, 2, "-upstream-max-idle-conns-per-host must be 1 or more",
		ok, "http://x", "-allowlist", ".", "-upstream-max-idle-conns-per-host", "0")
	// A total below the per-host limit caps it, so it's rejected instead of
	// leaving the per-host value without effect.
	f(t, 2, "-upstream-max-idle-conns must be 0 or at least",
		ok, "http://x", "-allowlist", ".", "-upstream-max-idle-conns", "8")
}

// TestParseProxyEnv covers the environment form of the flags. The proxy is
// configured by a deployment, which has no command line to edit.
func TestParseProxyEnv(t *testing.T) {
	t.Setenv(config.EnvName("listen"), "127.0.0.1:9")
	t.Setenv(config.EnvName("upstream"), "http://from-env/graphql")
	t.Setenv(config.EnvName("allowlist"), "/queries")
	t.Setenv(config.EnvName("max-body"), "4096")
	t.Setenv(config.EnvName("watch"), "true")
	t.Setenv(config.EnvName("upstream-max-idle-conns-per-host"), "128")

	var errOut strings.Builder
	cfg, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(), &errOut)
	if !run || code != 0 {
		t.Fatalf("expected the environment to stand in for the flags; "+
			"code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Listen != "127.0.0.1:9" || cfg.Allowlist != "/queries" ||
		cfg.MaxBody != 4096 || !cfg.Watch || cfg.MaxIdleConnsPerHost != 128 ||
		cfg.Upstream.String() != "http://from-env/graphql" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	// A flag on the command line wins over the environment.
	errOut.Reset()
	cfg, code, run = config.ParseProxy("gqlhash-proxy",
		proxyArgs("-listen", "127.0.0.1:10"), &errOut)
	if !run || code != 0 {
		t.Fatalf("code %d, stderr: %s", code, errOut.String())
	}
	if cfg.Listen != "127.0.0.1:10" || cfg.Allowlist != "/queries" {
		t.Errorf("expected the flag to win and the rest to stand: %+v", cfg)
	}

	// A value the flag can't take names the variable it came from.
	t.Setenv(config.EnvName("max-body"), "not-a-number")
	errOut.Reset()
	if _, code, run := config.ParseProxy("gqlhash-proxy", proxyArgs(),
		&errOut); run || code != 2 {
		t.Errorf("expected a bad value to fail; code %d run %t", code, run)
	} else if !strings.Contains(errOut.String(), config.EnvName("max-body")) {
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
	if hasher.Hash != config.HashFunctionSHA1 {
		t.Errorf("expected sha1 to stand; received %v", hasher.Hash)
	}
}

func TestEnvName(t *testing.T) {
	for flag, want := range map[string]string{
		"listen":                           "GQLHASH_PROXY_LISTEN",
		"max-body":                         "GQLHASH_PROXY_MAX_BODY",
		"upstream-max-idle-conns-per-host": "GQLHASH_PROXY_UPSTREAM_MAX_IDLE_CONNS_PER_HOST",
	} {
		if got := config.EnvName(flag); got != want {
			t.Errorf("-%s: expected %q; received %q", flag, want, got)
		}
	}
}

// TestFlagInventory pins the flag set of both commands. A new flag fails this test
// until it's listed here, which is the reminder to cover it.
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
		"file":    "",
		"format":  `"hex"`,
		"hash":    `"sha1"`,
		"ignore":  `"nothing"`,
		"version": "",
	})

	f(t, func(n string, a []string, w *strings.Builder) (int, bool) {
		_, code, run := config.ParseProxy(n, a, w)
		return code, run
	}, proxyArgs("-help"), map[string]string{
		"allow-batch":      "",
		"allowlist":        "",
		"exact":            "",
		"hash":             `"sha2"`,
		"ignore":           `"nothing"`,
		"listen":           `":8080"`,
		"log-json":         "true",
		"log-level":        `"info"`,
		"log-requests":     "",
		"max-body":         "1048576",
		"metrics":          "",
		"opaque-errors":    "",
		"shutdown-timeout": "10s",
		"status":           "",
		"timeout":          "30s",
		"trust-forwarded":  "",
		"upstream":         "",
		"version":          "",
		"watch":            "",
		"watch-interval":   "2s",

		"upstream-max-idle-conns":          "256",
		"upstream-max-idle-conns-per-host": "64",
		"upstream-http2":                   "true",
	})
}

func TestNames(t *testing.T) {
	// Every supported name round-trips through the parse and back.
	for _, name := range strings.Split(config.SupportedHashFunctions, ", ") {
		if got := config.HashName(config.ParseHashFunction(name)); got != name {
			t.Errorf("expected %q; received %q", name, got)
		}
	}
	if got := config.HashName(0); got != "" {
		t.Errorf("expected no name for the zero value; received %q", got)
	}

	for _, name := range strings.Split(config.SupportedIgnoreModes, ", ") {
		mode, ok := config.ParseIgnore(name)
		if !ok {
			t.Fatalf("expected %q to parse", name)
		}
		if got := config.IgnoreName(mode); got != name {
			t.Errorf("expected %q; received %q", name, got)
		}
	}
}

// TestVersionPrecedence pins that -version needs nothing else to be valid. Both
// forms answer who the binary is without doing any work.
func TestVersionPrecedence(t *testing.T) {
	var errOut strings.Builder
	cfg, code, run := config.ParseHasher("gqlhash",
		hasherArgs("-version", "-hash", "sha9", "-format", "rot13"), &errOut)
	if !run || code != 0 || !cfg.Version {
		t.Errorf("hasher: expected -version to win; code %d run %t stderr %q",
			code, run, errOut.String())
	}

	errOut.Reset()
	proxyCfg, code, run := config.ParseProxy("gqlhash",
		proxyArgs("-version", "-hash", "sha9"), &errOut)
	if !run || code != 0 || !proxyCfg.Version {
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
