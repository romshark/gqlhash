package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/parser"
)

type Hasher struct {
	// File is the document to read, or empty for stdin.
	File string

	// Format is the encoding of the hash, Hash the function it's made with and
	// Ignore what to leave out of it.
	Format Format
	Hash   HashFunction
	Ignore gqlhash.Ignore

	// DepthLimit is how deeply a document may nest before it's refused,
	// see [gqlhash.Options].
	DepthLimit int

	// CmdPrintVersion says the command was asked for its version and nothing else,
	// so the caller prints it and returns instead of hashing.
	CmdPrintVersion bool
}

// Proxy is what the proxy command was asked to do.
type Proxy struct {
	// AllowlistDir is the directory the allowed documents are read from.
	AllowlistDir string

	// HashFunc is one of the collision-resistant functions,
	// see [SupportedProxyHashFunctions].
	HashFunc HashFunction

	// Ignore is what to leave out of the hash of a document.
	Ignore gqlhash.Ignore

	// DepthLimit is how deeply a document may nest before it's refused,
	// see [gqlhash.Options].
	DepthLimit int

	// AllowBatch accepts a batch of documents, every one of which has to be allowed.
	AllowBatch bool

	// OpaqueErrors answers every rejection with 403 and no detail.
	// TrustForwarded keeps the X-Forwarded-* headers a request arrives with,
	// which only a proxy behind a trusted load balancer may do.
	OpaqueErrors   bool
	TrustForwarded bool

	Server   ProxyServer
	Upstream ProxyUpstream
	Control  ProxyControl
	Log      ProxyLog

	// CmdPrintVersion says the command was asked for its version and nothing else,
	// so the caller prints it and returns instead of serving.
	CmdPrintVersion bool
}

// ProxyServer is the listener that takes the traffic. Its timeouts bound what a
// client can hold open, and a zero value leaves the one it stands for off.
type ProxyServer struct {
	Listen string

	// Underlay names the HTTP implementation serving the traffic, which is a
	// property of the binary rather than a flag. It's reported in the startup
	// log so an operator can tell which one is running.
	Underlay string

	// MaxBody is the largest request body to accept, in bytes.
	MaxBody int64

	// ShutdownTimeout is how long the requests in flight are waited for on the way out.
	ShutdownTimeout time.Duration

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// ProxyUpstream is the GraphQL API a request is forwarded to.
type ProxyUpstream struct {
	URL *url.URL

	// Timeout is how long a request to it may take.
	Timeout time.Duration

	// MaxIdleConnsPerHost is what caps connection reuse: there is one upstream,
	// so every forwarded request draws from that one pool.
	// MaxIdleConns is the ceiling over it, 0 for none.
	MaxIdleConnsPerHost int
	MaxIdleConns        int

	// MaxConnLifetime retires an upstream connection once it's this old, 0 to
	// keep it for as long as the upstream will. It's what lets a pool follow an
	// upstream that moves: a name resolving to several backends is balanced per
	// connection, so a pool that never turns over never reaches a backend that
	// wasn't there when it was filled.
	MaxConnLifetime time.Duration

	// HTTP2 allows h2 to an https upstream, which multiplexes onto one connection
	// and makes the pool sizes matter little.
	HTTP2 bool
}

// ProxyControl is the server that answers the metrics and the endpoints that
// change what the proxy does.
type ProxyControl struct {
	// Address is never empty: there is no way to run without this server.
	Address string

	// Token is what a request to an endpoint that changes something must carry as
	// a bearer token, empty for no check. It comes from the environment alone,
	// never from a flag.
	Token string
}

// ProxyLog is how the proxy writes its log.
type ProxyLog struct {
	// Level is left as it was given.
	// The command owns the logger and turns it into a level,
	// so this package needs no logging dependency and neither does the hashing command.
	Level string

	// JSON writes JSON instead of readable text. Requests logs every forwarded
	// request at debug level.
	JSON     bool
	Requests bool
}

// ProxyCommand is the command that serves the proxy,
// which the hasher names where a caller reaches for it as a subcommand.
const ProxyCommand = "gqlhash-proxy"

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
		fDepthLimit = cli.Int("depth-limit", parser.DefaultDepthLimit,
			"How deeply a document may nest before it's refused.\n"+
				"Below 1 takes the default.")
	)
	if code, ok := parse(cli, args, stderr, ProxyCommand); !ok {
		return cfg, code, false
	}

	cfg.File, cfg.CmdPrintVersion = *fFile, *fVersion
	if cfg.CmdPrintVersion {
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
	cfg.DepthLimit = *fDepthLimit
	return cfg, 0, true
}

// EnvPrefix is what the environment form of a proxy flag starts with, see [EnvName].
const EnvPrefix = "GQLHASH_PROXY_"

// EnvName is the environment variable that stands for the flag named flag.
//
// Only the proxy reads these. It's a long-running service configured by a deployment,
// while the hashing command is invoked per document:
// an environment variable that silently changed -hash there would change
// the hashes a pipeline produces without anything saying so.
func EnvName(flag string) string {
	name := strings.NewReplacer("-", "_", ".", "_").Replace(flag)
	return EnvPrefix + strings.ToUpper(name)
}

// ParseProxy reads the flags of the proxy command.
//
// run is false when the caller is done and must return exitCode.
// name is the command as invoked.
func ParseProxy(
	name string, args []string, stderr io.Writer,
) (cfg Proxy, exitCode int, run bool) {
	return ParseProxyFor("", false, name, args, stderr)
}

// ParseProxyFor is [ParseProxy] for a binary serving with a named underlay.
//
// underlay is reported in the startup log. http1Only refuses -upstream.http2:
// an implementation that can't speak h2 stops the run rather than accepting the
// flag and serving something other than what it asks for. An empty underlay is
// net/http, which has neither restriction.
func ParseProxyFor(
	underlay string, http1Only bool,
	name string, args []string, stderr io.Writer,
) (cfg Proxy, exitCode int, run bool) {
	if underlay == "" {
		underlay = "nethttp"
	}
	cli := flag.NewFlagSet(name, flag.ContinueOnError)
	cli.SetOutput(stderr)
	var (
		fHash = cli.String("hash", "sha2",
			"Hash function ("+SupportedProxyHashFunctions+").\n"+
				"Only collision-resistant functions are accepted here.")
		fIgnore = cli.String("ignore", "nothing",
			"What to leave out of the hash ("+SupportedIgnoreModes+")")
		fDepthLimit = cli.Int("depth-limit", parser.DefaultDepthLimit,
			"How deeply a document may nest before it's refused.\n"+
				"Nesting is what a document grows cheaply, so this bounds\n"+
				"what one costs. Below 1 takes the default.")
		fMaxBody = cli.Int64("server.max-body", 1<<20,
			"Largest request body to accept, in bytes")
		fAllowBatch = cli.Bool("allow-batch", false,
			"Accept batched requests, where every document must be allowed")
		fOpaqueErrors = cli.Bool("opaque-errors", false,
			"Answer every rejection with 403 and no detail")
		fTrustForwarded = cli.Bool("trust-forwarded", false,
			"Keep the X-Forwarded-* headers of the request and append to them.\n"+
				"Set this only behind a trusted load balancer: a client that\n"+
				"reaches the proxy directly can otherwise claim any address.")
		fAllowlist = cli.String("allowlist", "",
			"Directory holding the allowed documents as .graphql and .gql files")

		fControl = cli.String("control.listen", "127.0.0.1:9090",
			"Address to serve the control server on. It answers Prometheus\n"+
				"metrics on /metrics and rereads the allowlist on POST /reload.\n"+
				"Keep the address off any network a client of the API can reach,\n"+
				"which is what the loopback default does.\n"+
				"Set "+EnvName("control.token")+" to require it as a bearer\n"+
				"token on /reload. It has no flag on purpose: a process argument\n"+
				"is readable by anyone on the host. Metrics need no token.")

		fLogRequests = cli.Bool("log.requests", false,
			"Log every forwarded request at debug level")
		fLogLevel = cli.String("log.level", "info",
			"Log level (debug, info, warn, error)")
		fLogJSON = cli.Bool("log.json", true, "Log JSON instead of readable text")

		fUpstream = cli.String("upstream.url", "",
			"URL of the GraphQL API to forward to")
		fTimeout = cli.Duration("upstream.timeout", 30*time.Second,
			"Upstream request timeout")
		fMaxIdleConnsPerHost = cli.Int("upstream.max-idle-conns-per-host", 64,
			"Connections to keep open to the upstream between requests.\n"+
				"This is what caps connection reuse under load.")
		fMaxIdleConns = cli.Int("upstream.max-idle-conns", 256,
			"Ceiling over -upstream.max-idle-conns-per-host, 0 for none")
		fMaxConnLifetime = cli.Duration("upstream.max-conn-lifetime", 0,
			"Retire an upstream connection once it's this old, 0 to keep it.\n"+
				"Set it where the upstream is several backends behind one name:\n"+
				"they're balanced per connection, so a pool that never turns\n"+
				"over never reaches one that was added after it filled.")
		fHTTP2 = cli.Bool("upstream.http2", true,
			"Allow HTTP/2 to an https upstream, which multiplexes the requests\n"+
				"onto one connection instead of one connection each.\n"+
				"An http upstream is HTTP/1.1 either way, h2c is never used.")

		fListen            = cli.String("server.listen", ":8080", "Address to listen on")
		fReadHeaderTimeout = cli.Duration("server.read-header-timeout", 10*time.Second,
			"How long a client may take to send the request headers, which is\n"+
				"what keeps a connection from being held open by sending them\n"+
				"one byte at a time. 0 leaves it off.")
		fReadTimeout = cli.Duration("server.read-timeout", 30*time.Second,
			"How long a client may take to send the whole request. It has to fit\n"+
				"-server.max-body arriving over the slowest link you serve. 0 leaves it off.")
		fWriteTimeout = cli.Duration("server.write-timeout", 0,
			"How long answering a request may take. "+
				"Unset follows -upstream.timeout with\n"+
				"10s to spare, since a shorter one would cut off a response the\n"+
				"upstream is still allowed to be sending. 0 leaves it off.")
		fIdleTimeout = cli.Duration("server.idle-timeout", 120*time.Second,
			"How long an idle keep-alive connection is held. Behind a load\n"+
				"balancer, keep this above its own idle timeout, or it reuses a\n"+
				"connection the proxy is closing. 0 leaves it off.")
		fShutdown = cli.Duration("server.shutdown-timeout", 10*time.Second,
			"How long to wait for in-flight requests on shutdown")

		fVersion = cli.Bool("version", false, "Print the version to stdout and exit")
	)
	cli.Usage = func() {
		_, _ = fmt.Fprintf(cli.Output(), "Usage of %s:\n", name)
		cli.PrintDefaults()
		_, _ = fmt.Fprintf(cli.Output(),
			"\nEvery flag can be given through the environment instead, as %s\n"+
				"followed by its name with the dashes and dots as underscores,\n"+
				"such as %s=%s.\n"+
				"A flag given on the command line wins over the environment.\n",
			EnvPrefix, EnvName("server.max-body"), "4096")
	}
	// The environment is read before parsing, so the command line wins.
	if code, ok := applyEnv(cli, stderr); !ok {
		return cfg, code, false
	}
	if code, ok := parse(cli, args, stderr, ""); !ok {
		return cfg, code, false
	}

	cfg = Proxy{
		AllowlistDir:   *fAllowlist,
		AllowBatch:     *fAllowBatch,
		OpaqueErrors:   *fOpaqueErrors,
		TrustForwarded: *fTrustForwarded,
		Server: ProxyServer{
			Listen:            *fListen,
			Underlay:          underlay,
			MaxBody:           *fMaxBody,
			ShutdownTimeout:   *fShutdown,
			ReadHeaderTimeout: *fReadHeaderTimeout,
			ReadTimeout:       *fReadTimeout,
			WriteTimeout:      *fWriteTimeout,
			IdleTimeout:       *fIdleTimeout,
		},
		Upstream: ProxyUpstream{
			Timeout:             *fTimeout,
			MaxIdleConnsPerHost: *fMaxIdleConnsPerHost,
			MaxIdleConns:        *fMaxIdleConns,
			MaxConnLifetime:     *fMaxConnLifetime,
			HTTP2:               *fHTTP2,
		},
		Control: ProxyControl{Address: *fControl},
		Log: ProxyLog{
			Level: *fLogLevel, JSON: *fLogJSON, Requests: *fLogRequests,
		},
		CmdPrintVersion: *fVersion,
	}

	if cfg.CmdPrintVersion {
		// The caller prints the version, so nothing else has to be given.
		return cfg, 0, true
	}

	if *fUpstream == "" {
		_, _ = fmt.Fprintln(stderr, "-upstream.url is required")
		return cfg, 2, false
	}
	upstream, err := url.Parse(*fUpstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		_, _ = fmt.Fprintf(stderr, "-upstream.url %q is no absolute URL\n", *fUpstream)
		return cfg, 2, false
	}
	cfg.Upstream.URL = upstream

	// The token has no flag: a process argument is readable by anyone on the host,
	// while the environment of a process isn't.
	cfg.Control.Token = strings.TrimSpace(os.Getenv(EnvName("control.token")))

	if cfg.AllowlistDir == "" {
		_, _ = fmt.Fprintln(stderr, "-allowlist is required")
		return cfg, 2, false
	}
	// There is no way to run without the control server: the metrics and the
	// reload are how a deployment sees and steers what the proxy is doing.
	if cfg.Control.Address == "" {
		_, _ = fmt.Fprintln(stderr, "-control.listen must name an address")
		return cfg, 2, false
	}
	if cfg.HashFunc = ParseProxyHashFunction(*fHash); cfg.HashFunc == 0 {
		return cfg, unsupported(stderr, "hash function", *fHash,
			SupportedProxyHashFunctions), false
	}
	var ok bool
	if cfg.Ignore, ok = ParseIgnore(*fIgnore); !ok {
		return cfg, unsupported(stderr, "ignore mode", *fIgnore,
			SupportedIgnoreModes), false
	}
	cfg.DepthLimit = *fDepthLimit

	for _, t := range []struct {
		name  string
		value time.Duration
	}{
		{"server.read-header-timeout", cfg.Server.ReadHeaderTimeout},
		{"server.read-timeout", cfg.Server.ReadTimeout},
		{"server.write-timeout", cfg.Server.WriteTimeout},
		{"server.idle-timeout", cfg.Server.IdleTimeout},
	} {
		if t.value < 0 {
			_, _ = fmt.Fprintf(stderr, "-%s must be 0 or more\n", t.name)
			return cfg, 2, false
		}
	}
	// An unset write timeout follows the upstream timeout. Set explicitly,
	// it has to leave the upstream its full time,
	// or the proxy cuts off a response that is still arriving.
	if !isSet(cli, "server.write-timeout") {
		cfg.Server.WriteTimeout = cfg.Upstream.Timeout + 10*time.Second
	} else if cfg.Server.WriteTimeout != 0 && cfg.Server.WriteTimeout <= cfg.Upstream.Timeout {
		_, _ = fmt.Fprintf(stderr,
			"-server.write-timeout %v must be above -upstream.timeout %v, "+
				"or 0 for none\n",
			cfg.Server.WriteTimeout, cfg.Upstream.Timeout)
		return cfg, 2, false
	}
	// ReadTimeout covers the headers too, so a shorter one decides first and
	// leaves -server.read-header-timeout without effect.
	if cfg.Server.ReadTimeout != 0 && cfg.Server.ReadTimeout < cfg.Server.ReadHeaderTimeout {
		_, _ = fmt.Fprintf(stderr,
			"-server.read-timeout %v must be at least "+
				"-server.read-header-timeout %v, "+
				"or 0 for none\n", cfg.Server.ReadTimeout, cfg.Server.ReadHeaderTimeout)
		return cfg, 2, false
	}

	if http1Only {
		// Rather than accept the flag and serve something other than what it
		// asks for, the run stops here and says where h2 belongs instead.
		if isSet(cli, "upstream.http2") && cfg.Upstream.HTTP2 {
			_, _ = fmt.Fprintf(stderr,
				"-upstream.http2 can't be served by %s, which speaks "+
					"HTTP/1.1 only.\n"+
					"Terminate HTTP/2 ahead of this proxy, at the load balancer "+
					"or the ingress,\nand let it reach the upstream over HTTP/1.1. "+
					"Set -upstream.http2=false to run\nwith this command.\n",
				// The base of the invocation, so the message names the command
				// rather than wherever it was built or installed.
				filepath.Base(name))
			return cfg, 2, false
		}
		// Unset, the default of the flag is true and no h2 is on offer, so what
		// the run logs stays what the run does.
		cfg.Upstream.HTTP2 = false
	}

	if cfg.Upstream.MaxConnLifetime < 0 {
		_, _ = fmt.Fprintln(stderr,
			"-upstream.max-conn-lifetime must be 0 or more")
		return cfg, 2, false
	}

	if cfg.Upstream.MaxIdleConnsPerHost < 1 {
		_, _ = fmt.Fprintln(stderr,
			"-upstream.max-idle-conns-per-host must be 1 or more")
		return cfg, 2, false
	}
	// A total below the per-host limit caps it, which reads as the per-host
	// value having no effect.
	if cfg.Upstream.MaxIdleConns < 0 || (cfg.Upstream.MaxIdleConns > 0 &&
		cfg.Upstream.MaxIdleConns < cfg.Upstream.MaxIdleConnsPerHost) {
		_, _ = fmt.Fprintf(stderr, "-upstream.max-idle-conns must be 0 or at "+
			"least -upstream.max-idle-conns-per-host (%d)\n",
			cfg.Upstream.MaxIdleConnsPerHost)
		return cfg, 2, false
	}
	return cfg, 0, true
}

// isSet reports whether the flag named name was given, on the command line
// or through the environment, which [applyEnv] sets the same way.
func isSet(cli *flag.FlagSet, name string) bool {
	var set bool
	cli.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
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
		// Set on the set rather than on the value: the flag set records what
		// was set that way, which is what isSet reads. Setting the value alone
		// leaves the flag looking untouched, so a default derived from another
		// flag overwrites what the environment asked for,
		// and a rule that refuses a combination never runs.
		if err := cli.Set(f.Name, value); err != nil {
			_, _ = fmt.Fprintf(stderr, "%s=%q: %v\n", name, value, err)
			exitCode, ok = 2, false
		}
	})
	return exitCode, ok
}

// parse reads args and rejects what the flag package would let through.
//
// proxyCommand names the command that serves the proxy, for the one command
// that isn't it: reaching for the proxy by subcommand is a plausible guess from
// the hasher and no guess at all from the proxy, which is already there.
// It's empty where there's nothing to point at.
func parse(
	cli *flag.FlagSet, args []string, stderr io.Writer, proxyCommand string,
) (int, bool) {
	if err := cli.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	// Every command takes its input from stdin or a flag,
	// so there is nothing for a positional argument to mean. Without this the
	// flag package ignores it and a mistyped command silently hashes stdin.
	if cli.NArg() > 0 {
		if proxyCommand != "" && cli.Arg(0) == "proxy" {
			_, _ = fmt.Fprintf(stderr, "the proxy is the %s command\n", proxyCommand)
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
