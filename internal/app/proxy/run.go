// Package proxy serves an allowlist of GraphQL documents in front of a GraphQL
// API. It forwards a request whose document is on the list and rejects every
// other request.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
	"github.com/romshark/gqlhash/v2/internal/app/config"
)

// Run serves the proxy until it's interrupted, ctx is done, or a server fails.
//
// name and version are what -version reports. args[0] is the name of the command
// as invoked, as in [os.Args].
func Run(
	ctx context.Context,
	name, version string,
	args []string,
	stdout, stderr io.Writer,
) (exitCode int) {
	return RunWith(ctx, name, version, args, stdout, stderr, Underlay{})
}

// RunWith is [Run] on the given underlay. A zero [Underlay] serves with
// net/http, which is what [Run] passes.
func RunWith(
	ctx context.Context,
	name, version string,
	args []string,
	stdout, stderr io.Writer,
	underlay Underlay,
) (exitCode int) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal starts the shutdown and restores the default handling, so
	// a second one exits at once instead of waiting for the shutdown timeout.
	go func() {
		<-ctx.Done()
		stop()
	}()

	cfg, code, run := config.ParseProxyFor(underlay.Name, underlay.HTTP1Only,
		args[0], args, stderr)
	if !run {
		return code
	}
	if cfg.CmdPrintVersion {
		_, _ = fmt.Fprintf(stdout, "%s v%s\n", name, version)
		return 0
	}

	log, ok := newLogger(cfg, stderr)
	if !ok {
		return 2
	}
	c, err := build(cfg, log, underlay)
	if err != nil {
		log.Error().Err(err).Msg("loading the allowlist")
		return 1
	}
	return c.serve(ctx, cfg, log)
}

// newLogger builds the logger from the flags. The level is the one thing config
// leaves as a string, because it owns no logging dependency.
func newLogger(
	cfg config.Proxy, stderr io.Writer,
) (log zerolog.Logger, ok bool) {
	level, err := zerolog.ParseLevel(cfg.Log.Level)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "unsupported log level %q\n", cfg.Log.Level)
		return log, false
	}
	out := stderr
	if !cfg.Log.JSON {
		out = zerolog.ConsoleWriter{Out: stderr, TimeFormat: time.RFC3339}
	}
	return zerolog.New(out).Level(level).With().Timestamp().Logger(), true
}

// components are what a run assembles from a config.
type components struct {
	allowlist *allowlist.Allowlist
	proxy     *proxy

	// recycle retires idle upstream connections on an interval, or is nil where
	// -upstream.max-conn-lifetime is off. See [components.recycleConns].
	recycle func()
	// recycleEvery is how often it runs.
	recycleEvery time.Duration

	// dataPlane serves the requests the proxy exists for and control answers
	// /metrics, /status and /reload, each on a listener of its own. A run has
	// both: the control server has no off switch, see [config.ProxyControl].
	//
	// dataPlane is whichever HTTP implementation the command carries, see
	// [Underlay]. control is net/http under every one of them.
	dataPlane dataPlaneServer
	control   *http.Server
}

// build assembles the components and loads the allowlist.
func build(cfg config.Proxy, log zerolog.Logger, underlay Underlay) (*components, error) {
	options := gqlhash.Options{Ignore: cfg.Ignore, DepthLimit: cfg.DepthLimit}
	// The function is checked once here rather than per request: a proxy that
	// can't hash serves nothing, so it's a start failure and not a nil that
	// turns up at the first request.
	if _, ok := config.NewHasher(cfg.HashFunc); !ok {
		return nil, fmt.Errorf("unsupported hash function: %s",
			config.HashName(cfg.HashFunc))
	}
	newHash := func() hash.Hash {
		h, _ := config.NewHasher(cfg.HashFunc)
		return h
	}

	list := allowlist.New(newHash, options)
	result, err := list.Reload(cfg.AllowlistDir)
	if err != nil {
		return nil, err
	}
	logReload(log, cfg.AllowlistDir, result)

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          cfg.Upstream.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.Upstream.MaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Upstream.Timeout,
		ForceAttemptHTTP2:     cfg.Upstream.HTTP2,
		// The proxy asks for no encoding of its own. Without this net/http adds
		// Accept-Encoding: gzip to a request that carried none and gunzips the
		// answer, so an API compresses what nobody asked for and the client
		// reads an answer in another envelope than the API sent.
		// What the client asks for is forwarded, and what the API answers with arrives.
		DisableCompression: true,
	}
	p := newProxy(list, cfg.Upstream.URL, newHash, proxyConfig{
		options:         options,
		upstreamTimeout: cfg.Upstream.Timeout,
		allowBatch:      cfg.AllowBatch,
		opaqueErrors:    cfg.OpaqueErrors,
		logRequests:     cfg.Log.Requests,
		trustForwarded:  cfg.TrustForwarded,
		maxBody:         cfg.Server.MaxBody,
	}, transport, log)

	// The metrics and the control endpoints share an address of their own, so
	// neither is exposed on the data-plane port.
	controlMux := http.NewServeMux()
	controlMux.Handle("/metrics", p.metrics.Handler(log))
	(&control{
		allowlist: list, dir: cfg.AllowlistDir, proxy: p,
		token: cfg.Control.Token, log: log,
	}).routes(controlMux)
	controlServer := &http.Server{
		Addr:              cfg.Control.Address,
		Handler:           controlMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The underlay is used here and nowhere else: past this point a run holds a
	// [dataPlaneServer] and doesn't ask which one it got.
	var server dataPlaneServer
	var recycle func()
	if underlay.New != nil {
		// An underlay of its own owns its client, so retiring its connections is
		// its own to do. fasthttp takes the lifetime as a setting.
		server = underlay.New(p.Core(), cfg, cfg.Upstream.URL, log)
	} else {
		if cfg.Upstream.MaxConnLifetime > 0 {
			// http.Transport has no lifetime of its own, and a connection can't
			// be closed from under a request that's using it. Closing the idle
			// ones on an interval is the safe form of the same thing:
			// what's in flight is left alone, and what isn't gets dialled again,
			// which resolves the name afresh.
			recycle = transport.CloseIdleConnections
		}
		mux := http.NewServeMux()
		mux.Handle("/", p)
		server = netHTTPServer{server: &http.Server{
			Addr:              cfg.Server.Listen,
			Handler:           mux,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			ReadTimeout:       cfg.Server.ReadTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
		}}
	}
	return &components{
		allowlist: list, proxy: p,
		recycle: recycle, recycleEvery: cfg.Upstream.MaxConnLifetime,
		dataPlane: server, control: controlServer,
	}, nil
}

// serve runs until ctx is done or a server fails.
func (c *components) serve(
	ctx context.Context, cfg config.Proxy, log zerolog.Logger,
) (exitCode int) {
	// The listener is opened before serving, so a bind failure is reported as
	// one and the address that's logged is the one in use.
	// With -server.listen :0 that address is the only way to learn the port.
	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		log.Error().Err(err).Str("listen", cfg.Server.Listen).Msg("listening")
		return 1
	}
	return c.serveOn(ctx, listener, cfg, log)
}

// serveOn serves the data plane on listener and runs until ctx is done or a
// server fails. It's separate from [components.serve] so a test can hand it a
// listener of its own, which is the only way to fail the data-plane server
// while it runs.
func (c *components) serveOn(
	ctx context.Context, listener net.Listener,
	cfg config.Proxy, log zerolog.Logger,
) (exitCode int) {
	list, server := c.allowlist, c.dataPlane

	errServe := make(chan error, 1)
	go func() { errServe <- server.Serve(listener) }()
	go c.recycleConns(ctx)

	// controlAddress is the one in use, which is the only way to learn the port
	// behind an address ending in :0.
	controlListener, err := net.Listen("tcp", c.control.Addr)
	if err != nil {
		log.Error().Err(err).Str("address", c.control.Addr).
			Msg("listening for the control server")
		// Nothing is in flight this early, so the shutdown returns at once.
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(stopCtx)
		return 1
	}
	controlAddress := controlListener.Addr().String()
	go func() {
		if err := c.control.Serve(controlListener); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("serving the control server")
		}
	}()
	log.Info().Str("address", controlAddress).Msg("serving /metrics and /reload")
	listening := log.Info().
		Str("address", listener.Addr().String()).
		Str("upstream", cfg.Upstream.URL.String()).
		Str("allowlist", cfg.AllowlistDir).
		Int("documents", list.Len()).
		Str("hash", config.HashName(cfg.HashFunc)).
		Str("ignore", config.IgnoreName(cfg.Ignore)).
		Int("depth_limit", cfg.DepthLimit).
		Bool("trust_forwarded", cfg.TrustForwarded).
		// The environment can set any of these, so the effective values are
		// logged where a deployment can see them.
		Int("upstream_max_idle_conns_per_host", cfg.Upstream.MaxIdleConnsPerHost).
		Bool("upstream_http2", cfg.Upstream.HTTP2).
		Str("underlay", cfg.Server.Underlay).
		Str("control", controlAddress)
	listening.Msg("listening")

	// Both servers are shut down on every way out of here,
	// so a failure of one doesn't leave the other holding a listener.
	var failed bool
	select {
	case err := <-errServe:
		// An ordinary shutdown is reported as nil whichever underlay served,
		// see [dataPlaneServer].
		if err != nil {
			log.Error().Err(err).Msg("serving")
			failed = true
		}
	case <-ctx.Done():
		log.Info().Msg("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := c.control.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutting down the control server")
		failed = true
	}
	// Shutdown waits for the requests in flight, bounded by -server.shutdown-timeout.
	// Past the timeout it returns and whatever is still running is abandoned.
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutting down")
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}

// recycleConns retires idle upstream connections every
// -upstream.max-conn-lifetime, and returns at once where that's off.
//
// A pool that never turns over pins itself to the backends it first reached:
// a name standing for several of them is balanced per connection, so one added
// later takes no traffic until something lets go. This is that something.
func (c *components) recycleConns(ctx context.Context) {
	if c.recycle == nil || c.recycleEvery <= 0 {
		return
	}
	ticker := time.NewTicker(c.recycleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.recycle()
		}
	}
}

// logReload reports what a load of the allowlist did. The allowlist logs nothing
// itself: what deserves an event, and at which level, is decided here.
//
// Every file left out is an error, since a document that was meant to be served
// and isn't is a deployment mistake, and the summary is one line whatever the outcome,
// so a reload always leaves a trace.
func logReload(log zerolog.Logger, dir string, r allowlist.Result) {
	if r.SchemaErr != nil {
		log.Error().Err(r.SchemaErr).
			Msg("reading the schema, the documents are checked against none")
	}
	for _, err := range r.Skipped {
		log.Error().Err(err).Msg("skipping a document")
	}
	if len(r.Files) == 0 {
		// Serving an empty allowlist rejects every request,
		// which is loud in the counters but silent otherwise.
		// This is the one line that says why.
		log.Error().Str("dir", dir).Int("skipped", len(r.Skipped)).
			Msg("no documents on the allowlist, every request is rejected")
	}
	log.Info().
		Int("documents", len(r.Files)).
		Int("added", r.Added).
		Int("removed", r.Removed).
		Int("skipped", len(r.Skipped)).
		Str("dir", dir).
		Msg("allowlist loaded")
}
