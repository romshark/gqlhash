package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/romshark/gqlhash/v2"
)

// Hasher is what the gqlhash command was asked to do.
type Hasher struct {
	// File is the document to read, or empty for stdin.
	File string

	Format  Format
	Hash    HashFunction
	Ignore  gqlhash.Ignore
	Version bool
}

// Proxy is what the proxy command was asked to do.
type Proxy struct {
	Listen        string
	Upstream      *url.URL
	Allowlist     string
	Watch         bool
	WatchInterval time.Duration

	// Hash is one of the collision-resistant functions, see
	// [SupportedProxyHashFunctions].
	Hash   HashFunction
	Ignore gqlhash.Ignore
	Exact  bool

	MaxBody        int64
	AllowBatch     bool
	OpaqueErrors   bool
	LogRequests    bool
	TrustForwarded bool

	// MaxIdleConnsPerHost is what caps connection reuse: there is one upstream,
	// so every forwarded request draws from that one pool. MaxIdleConns is the
	// ceiling over it. HTTP2 allows h2 to the upstream, which multiplexes onto
	// one connection and makes the pool sizes matter little.
	MaxIdleConnsPerHost int
	MaxIdleConns        int
	HTTP2               bool

	// LogLevel is left as it was given. The command owns the logger and turns it
	// into a level, so this package needs no logging dependency and neither does
	// the hashing command.
	LogLevel string
	LogJSON  bool

	Timeout  time.Duration
	Shutdown time.Duration
	Status   string
	Metrics  string
	Version  bool
}

// SupportedProxyHashFunctions are the hash functions the proxy accepts.
//
// Why not the full set of [SupportedHashFunctions]: the security property of an
// allowlist is collision resistance. Anyone who finds a second document with an
// allowed hash gets it executed upstream. crc32, crc64, fnv, fnv1a and xxh64 are
// collidable by construction, md5 and sha1 are broken.
const SupportedProxyHashFunctions = "sha2, sha3, blake2b, blake2s, blake3"

// ParseHasher reads the flags of the gqlhash command.
//
// run is false when the caller is done and must return exitCode, which covers
// -help, a bad flag and a bad value alike. name is the command as invoked.
func ParseHasher(
	name string, args []string, stderr io.Writer,
) (cfg Hasher, exitCode int, run bool) {
	cli := flag.NewFlagSet(name, flag.ContinueOnError)
	cli.SetOutput(stderr)
	var (
		fFile   = cli.String("file", "", "Path to a file holding the document")
		fFormat = cli.String("format", "hex",
			"Hash format ("+SupportedOutputFormats+")")
		fHash = cli.String("hash", "sha1",
			"Selects the hash function ("+SupportedHashFunctions+").\n"+
				"sha2 is SHA-256.\n"+
				"sha3 is SHA3-512.\n"+
				"blake2b is unkeyed.\n"+
				"blake2s is unkeyed.\n"+
				"blake3 is unkeyed, 256 bits wide.\n"+
				"fnv is FNV-1, 64 bits wide.\n"+
				"fnv1a is FNV-1a, 64 bits wide.\n"+
				"xxh64 is XXH64, unseeded.\n"+
				"crc32 uses the IEEE polynomial.\n"+
				"crc64 uses ISO polynomial, defined in ISO 3309 and used in HDLC.")
		fIgnore = cli.String("ignore", "nothing",
			"Selects what to leave out of the hash ("+SupportedIgnoreModes+").\n"+
				"nothing leaves out formatting and comments only.\n"+
				"inputs also leaves out every argument value, so queries differing\n"+
				"only in their argument and default values hash alike.\n"+
				"variables leaves out what inputs does and the variable definitions\n"+
				"too, so a parameterized query matches its literal form.")
		fVersion = cli.Bool("version", false,
			"Print the version to stdout and exit")
	)
	if code, ok := parse(cli, args, stderr); !ok {
		return cfg, code, false
	}

	cfg.File, cfg.Version = *fFile, *fVersion
	if cfg.Version {
		// The caller prints the version, so nothing else has to be valid.
		return cfg, 0, true
	}
	if cfg.Format = ParseFormat(*fFormat); cfg.Format == 0 {
		return cfg, unsupported(stderr, "format", *fFormat, SupportedOutputFormats),
			false
	}
	if cfg.Hash = ParseHashFunction(*fHash); cfg.Hash == 0 {
		return cfg, unsupported(stderr, "hash function", *fHash,
			SupportedHashFunctions), false
	}
	var ok bool
	if cfg.Ignore, ok = ParseIgnore(*fIgnore); !ok {
		return cfg, unsupported(stderr, "ignore mode", *fIgnore,
			SupportedIgnoreModes), false
	}
	return cfg, 0, true
}

// EnvPrefix is what the environment form of a proxy flag starts with, see
// [EnvName].
const EnvPrefix = "GQLHASH_PROXY_"

// EnvName is the environment variable that stands for the flag named flag.
//
// Only the proxy reads these. It's a long-running service configured by a
// deployment, while the hashing command is invoked per document: an environment
// variable that silently changed -hash there would change the hashes a pipeline
// produces without anything saying so.
func EnvName(flag string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

// ParseProxy reads the flags of the proxy command.
//
// run is false when the caller is done and must return exitCode. name is the
// command as invoked.
func ParseProxy(
	name string, args []string, stderr io.Writer,
) (cfg Proxy, exitCode int, run bool) {
	cli := flag.NewFlagSet(name, flag.ContinueOnError)
	cli.SetOutput(stderr)
	var (
		fListen    = cli.String("listen", ":8080", "Address to listen on")
		fUpstream  = cli.String("upstream", "", "URL of the GraphQL API to forward to")
		fAllowlist = cli.String("allowlist", "",
			"Directory holding the allowed documents as .graphql and .gql files")
		fWatch = cli.Bool("watch", false,
			"Reload the allowlist when the directory changes")
		fWatchInterval = cli.Duration("watch-interval", 2*time.Second,
			"How often to poll the allowlist directory under -watch")
		fHash = cli.String("hash", "sha2",
			"Hash function ("+SupportedProxyHashFunctions+").\n"+
				"Only collision-resistant functions are accepted here.")
		fIgnore = cli.String("ignore", "nothing",
			"What to leave out of the hash ("+SupportedIgnoreModes+")")
		fExact = cli.Bool("exact", false,
			"Compare canonical forms instead of hashes, which cannot collide")
		fMaxBody = cli.Int64("max-body", 1<<20,
			"Largest request body to accept, in bytes")
		fAllowBatch = cli.Bool("allow-batch", false,
			"Accept batched requests, where every document must be allowed")
		fOpaqueErrors = cli.Bool("opaque-errors", false,
			"Answer every rejection with 403 and no detail")
		fLogRequests = cli.Bool("log-requests", false,
			"Log every forwarded request at debug level")
		fTrustForwarded = cli.Bool("trust-forwarded", false,
			"Keep the X-Forwarded-* headers of the request and append to them.\n"+
				"Set this only behind a trusted load balancer: a client that\n"+
				"reaches the proxy directly can otherwise claim any address.")
		fLogLevel = cli.String("log-level", "info",
			"Log level (debug, info, warn, error)")
		fLogJSON             = cli.Bool("log-json", true, "Log JSON instead of readable text")
		fTimeout             = cli.Duration("timeout", 30*time.Second, "Upstream request timeout")
		fMaxIdleConnsPerHost = cli.Int("upstream-max-idle-conns-per-host", 64,
			"Connections to keep open to the upstream between requests.\n"+
				"This is what caps connection reuse under load.")
		fMaxIdleConns = cli.Int("upstream-max-idle-conns", 256,
			"Ceiling over -upstream-max-idle-conns-per-host, 0 for none")
		fHTTP2 = cli.Bool("upstream-http2", true,
			"Allow HTTP/2 to an https upstream, which multiplexes the requests\n"+
				"onto one connection instead of one connection each.\n"+
				"An http upstream is HTTP/1.1 either way, h2c is never used.")
		fVersion = cli.Bool("version", false, "Print the version to stdout and exit")
		fStatus  = cli.String("status", "", "Path to serve the status on, if any")
		fMetrics = cli.String("metrics", "",
			"Address to serve Prometheus metrics on, such as 127.0.0.1:9090.\n"+
				"Empty leaves them off and a request pays nothing for them.\n"+
				"The path is /metrics.")
		fShutdown = cli.Duration("shutdown-timeout", 10*time.Second,
			"How long to wait for in-flight requests on shutdown")
	)
	cli.Usage = func() {
		_, _ = fmt.Fprintf(cli.Output(), "Usage of %s:\n", name)
		cli.PrintDefaults()
		_, _ = fmt.Fprintf(cli.Output(),
			"\nEvery flag can be given through the environment instead, as %s\n"+
				"followed by its name with the dashes as underscores, such as %s.\n"+
				"A flag given on the command line wins over the environment.\n",
			EnvPrefix, EnvName("max-body"))
	}
	// The environment is read before parsing, so the command line wins.
	if code, ok := applyEnv(cli, stderr); !ok {
		return cfg, code, false
	}
	if code, ok := parse(cli, args, stderr); !ok {
		return cfg, code, false
	}

	cfg = Proxy{
		Listen: *fListen, Allowlist: *fAllowlist,
		Watch: *fWatch, WatchInterval: *fWatchInterval,
		Exact: *fExact, MaxBody: *fMaxBody,
		AllowBatch:   *fAllowBatch,
		OpaqueErrors: *fOpaqueErrors, LogRequests: *fLogRequests,
		TrustForwarded: *fTrustForwarded,
		LogLevel:       *fLogLevel, LogJSON: *fLogJSON,
		Timeout: *fTimeout, Shutdown: *fShutdown,
		Status: *fStatus, Metrics: *fMetrics, Version: *fVersion,
		MaxIdleConnsPerHost: *fMaxIdleConnsPerHost,
		MaxIdleConns:        *fMaxIdleConns, HTTP2: *fHTTP2,
	}
	if cfg.Version {
		// The caller prints the version, so nothing else has to be given.
		return cfg, 0, true
	}

	if *fUpstream == "" {
		_, _ = fmt.Fprintln(stderr, "-upstream is required")
		return cfg, 2, false
	}
	upstream, err := url.Parse(*fUpstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		_, _ = fmt.Fprintf(stderr, "-upstream %q is no absolute URL\n", *fUpstream)
		return cfg, 2, false
	}
	cfg.Upstream = upstream

	if cfg.Allowlist == "" {
		_, _ = fmt.Fprintln(stderr, "-allowlist is required")
		return cfg, 2, false
	}
	if cfg.Hash = ParseProxyHashFunction(*fHash); cfg.Hash == 0 {
		return cfg, unsupported(stderr, "hash function", *fHash,
			SupportedProxyHashFunctions), false
	}
	var ok bool
	if cfg.Ignore, ok = ParseIgnore(*fIgnore); !ok {
		return cfg, unsupported(stderr, "ignore mode", *fIgnore,
			SupportedIgnoreModes), false
	}

	if cfg.MaxIdleConnsPerHost < 1 {
		_, _ = fmt.Fprintln(stderr,
			"-upstream-max-idle-conns-per-host must be 1 or more")
		return cfg, 2, false
	}
	// A total below the per-host limit caps it, which reads as the per-host
	// value having no effect.
	if cfg.MaxIdleConns < 0 || (cfg.MaxIdleConns > 0 &&
		cfg.MaxIdleConns < cfg.MaxIdleConnsPerHost) {
		_, _ = fmt.Fprintf(stderr, "-upstream-max-idle-conns must be 0 or at "+
			"least -upstream-max-idle-conns-per-host (%d)\n",
			cfg.MaxIdleConnsPerHost)
		return cfg, 2, false
	}
	return cfg, 0, true
}

// applyEnv presets the flags from the environment, see [EnvName].
func applyEnv(cli *flag.FlagSet, stderr io.Writer) (exitCode int, ok bool) {
	ok = true
	cli.VisitAll(func(f *flag.Flag) {
		name := EnvName(f.Name)
		value, set := os.LookupEnv(name)
		if !set {
			return
		}
		if err := f.Value.Set(value); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s=%q: %v\n", name, value, err)
			exitCode, ok = 2, false
		}
	})
	return exitCode, ok
}

// ParseProxyHashFunction is [ParseHashFunction] restricted to the functions an
// allowlist may rely on. It returns 0 for every other name.
func ParseProxyHashFunction(s string) HashFunction {
	switch f := ParseHashFunction(s); f {
	case HashFunctionSHA2, HashFunctionSHA3, HashFunctionBLAKE2B,
		HashFunctionBLAKE2S, HashFunctionBLAKE3:
		return f
	}
	return 0
}

// HashName returns the flag value that names f, or "" for the zero value.
func HashName(f HashFunction) string {
	for _, name := range strings.Split(SupportedHashFunctions, ", ") {
		if ParseHashFunction(name) == f {
			return name
		}
	}
	return ""
}

// IgnoreName returns the flag value that names i.
func IgnoreName(i gqlhash.Ignore) string {
	switch i {
	case gqlhash.IgnoreInputs:
		return "inputs"
	case gqlhash.IgnoreVariables:
		return "variables"
	}
	return "nothing"
}

// parse reads args and rejects what the flag package would let through.
func parse(cli *flag.FlagSet, args []string, stderr io.Writer) (int, bool) {
	if err := cli.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	// Every command takes its input from stdin or a flag, so there is nothing for
	// a positional argument to mean. Without this the flag package ignores it and
	// a mistyped command silently hashes stdin.
	if cli.NArg() > 0 {
		if cli.Arg(0) == "proxy" {
			// Reaching for the proxy through this command is a plausible guess,
			// so it's answered with the name that has it.
			_, _ = fmt.Fprintln(stderr, "the proxy is the gqlhash-proxy command")
			return 2, false
		}
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", cli.Arg(0))
		return 2, false
	}
	return 0, true
}

// unsupported reports a value that names nothing and returns the exit code.
func unsupported(stderr io.Writer, what, value, supported string) int {
	_, _ = fmt.Fprintf(stderr, "unsupported %s %q, use any of: %s\n",
		what, value, supported)
	return 2
}
