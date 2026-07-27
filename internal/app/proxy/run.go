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
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal starts the shutdown and restores the default handling, so
	// a second one exits at once instead of waiting for the shutdown timeout.
	go func() {
		<-ctx.Done()
		stop()
	}()

	cfg, code, run := config.ParseProxy(args[0], args, stderr)
	if !run {
		return code
	}
	if cfg.Version {
		_, _ = fmt.Fprintf(stdout, "%s v%s\n", name, version)
		return 0
	}

	log, ok := newLogger(cfg, stderr)
	if !ok {
		return 2
	}
	c, err := build(cfg, log)
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
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "unsupported log level %q\n", cfg.LogLevel)
		return log, false
	}
	out := stderr
	if !cfg.LogJSON {
		out = zerolog.ConsoleWriter{Out: stderr, TimeFormat: time.RFC3339}
	}
	return zerolog.New(out).Level(level).With().Timestamp().Logger(), true
}

// components are what a run assembles from a config.
type components struct {
	Store  *Store
	Loader *Loader
	Proxy  *Proxy

	// Server takes the traffic, Metrics is nil unless -metrics is set.
	Server  *http.Server
	Metrics *http.Server
}

// build assembles the components and loads the allowlist.
func build(cfg config.Proxy, log zerolog.Logger) (*components, error) {
	options := gqlhash.Options{Ignore: cfg.Ignore}
	newHash := func() hash.Hash { return config.NewHasher(cfg.Hash) }

	store := new(Store)
	loader := NewLoader(store, cfg.Allowlist, cfg.Exact, newHash, options, log)
	if err := loader.Load(); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
		ForceAttemptHTTP2:     cfg.HTTP2,
	}
	proxy := NewProxy(store, cfg.Upstream, newHash, ProxyConfig{
		Options:        options,
		Exact:          cfg.Exact,
		AllowBatch:     cfg.AllowBatch,
		OpaqueErrors:   cfg.OpaqueErrors,
		LogRequests:    cfg.LogRequests,
		TrustForwarded: cfg.TrustForwarded,
		MaxBody:        cfg.MaxBody,
	}, transport, log)

	// Metrics live on an address of their own, so a scrape target isn't exposed
	// on the port that serves traffic.
	var metricsServer *http.Server
	if cfg.Metrics != "" {
		metrics := NewMetrics(proxy, store)
		proxy.SetMetrics(metrics)
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", metrics.Handler(log))
		metricsServer = &http.Server{
			Addr:              cfg.Metrics,
			Handler:           metricsMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", proxy)
	if cfg.Status != "" {
		mux.HandleFunc(cfg.Status, func(w http.ResponseWriter, r *http.Request) {
			writeStatus(w, store, proxy)
		})
	}

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      cfg.Timeout + 10*time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return &components{
		Store: store, Loader: loader, Proxy: proxy,
		Server: server, Metrics: metricsServer,
	}, nil
}

// serve runs until ctx is done or a server fails.
func (c *components) serve(
	ctx context.Context, cfg config.Proxy, log zerolog.Logger,
) (exitCode int) {
	store, loader := c.Store, c.Loader
	server, metricsServer := c.Server, c.Metrics

	done := make(chan struct{})
	if cfg.Watch {
		go loader.Watch(cfg.WatchInterval, done)
	}

	// The listener is opened before serving, so a bind failure is reported as
	// one and the address that's logged is the one in use. With -listen :0 that
	// address is the only way to learn the port.
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error().Err(err).Str("listen", cfg.Listen).Msg("listening")
		close(done)
		return 1
	}

	errServe := make(chan error, 1)
	go func() { errServe <- server.Serve(listener) }()

	if metricsServer != nil {
		metricsListener, err := net.Listen("tcp", metricsServer.Addr)
		if err != nil {
			log.Error().Err(err).Str("metrics", cfg.Metrics).
				Msg("listening for metrics")
			_ = server.Close()
			close(done)
			return 1
		}
		go func() {
			if err := metricsServer.Serve(metricsListener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				log.Error().Err(err).Msg("serving metrics")
			}
		}()
		log.Info().
			Str("address", metricsListener.Addr().String()).
			Msg("serving metrics on /metrics")
	}
	log.Info().
		Str("address", listener.Addr().String()).
		Str("upstream", cfg.Upstream.String()).
		Str("allowlist", cfg.Allowlist).
		Int("documents", store.Load().Len()).
		Bool("exact", cfg.Exact).
		Str("hash", config.HashName(cfg.Hash)).
		Str("ignore", config.IgnoreName(cfg.Ignore)).
		Bool("watch", cfg.Watch).
		Bool("trust_forwarded", cfg.TrustForwarded).
		Bool("metrics", cfg.Metrics != "").
		// The environment can set any of these, so the effective values are
		// logged where a deployment can see them.
		Int("upstream_max_idle_conns_per_host", cfg.MaxIdleConnsPerHost).
		Bool("upstream_http2", cfg.HTTP2).
		Msg("listening")

	// Both servers are shut down on every way out of here, so a failure of one
	// doesn't leave the other holding a listener.
	var failed bool
	select {
	case err := <-errServe:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("serving")
			failed = true
		}
	case <-ctx.Done():
		log.Info().Msg("shutting down")
	}
	close(done)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Shutdown)
	defer cancel()
	if metricsServer != nil {
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("shutting down the metrics server")
			failed = true
		}
	}
	// Shutdown waits for the requests in flight, bounded by -shutdown-timeout.
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

// writeStatus answers with the state of the allowlist and the decisions made.
func writeStatus(w http.ResponseWriter, store *Store, proxy *Proxy) {
	list := store.Load()
	allowed, rejected, malformed, upstream := proxy.CountersSnapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = fmt.Fprintf(w,
		`{"documents":%d,"loaded_at":%q,"allowed":%d,"rejected":%d,`+
			`"malformed":%d,"upstream_errors":%d}`,
		list.Len(), list.loadedAt.Format(time.RFC3339), allowed, rejected,
		malformed, upstream)
}
