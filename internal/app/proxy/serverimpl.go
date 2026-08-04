package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/app/config"
)

// dataPlaneServer takes requests from clients, decides on them and forwards
// what's allowed. Which HTTP implementation depends on the command a run was
// started as, see [ServerImpl].
//
// Its counterpart, the control server on -control.listen, is net/http under
// every command and sits on no hot path, so it's behind no interface at all.
//
// Neither is the decision: every implementation reaches [proxy.decide],
// so what a request is answered with doesn't depend on which carried it.
type dataPlaneServer interface {
	// Serve takes the traffic on listener until Shutdown: nil where it was shut down,
	// an error where it failed. net/http and fasthttp differ here and this hides it.
	Serve(listener net.Listener) error

	// Shutdown stops taking requests and waits for the ones in flight,
	// bounded by the deadline of ctx.
	Shutdown(ctx context.Context) error
}

// DataPlaneServer is [dataPlaneServer] under a name another package can implement.
type DataPlaneServer = dataPlaneServer

// ServerImpl is the HTTP implementation serving the traffic. The zero value is
// net/http, which is what gqlhash-proxy runs and what a deployment should.
// gqlhash-proxy-fhttp passes fasthttp, see [github.com/romshark/gqlhash/v2/internal/app/proxyfast].
//
// Given rather than chosen by a flag, since the choice is what a binary is
// built out of: only the command naming an implementation links it.
type ServerImpl struct {
	// Name goes in the startup log, so an operator can see which binary is running.
	Name string

	// HTTP1Only refuses -upstream.http2 rather than accepting the flag and
	// serving something else, see [config.ParseProxyFor].
	HTTP1Only bool

	// New builds the server. core is the decision it drives, upstream is where
	// a forwarded request goes.
	New func(
		core *Core, cfg config.Proxy, upstream *url.URL, log zerolog.Logger,
	) DataPlaneServer
}

// netHTTPServer is the net/http implementation, the default. [http.Server.Serve]
// answers an ordinary shutdown with [http.ErrServerClosed] where fasthttp answers nil,
// so that difference is flattened here rather than at every call.
type netHTTPServer struct{ server *http.Server }

func (s netHTTPServer) Serve(listener net.Listener) error {
	serve := s.server.Serve
	if s.server.TLSConfig != nil {
		// ServeTLS and not a TLS listener: it's what negotiates h2 over ALPN,
		// which a wrapped listener leaves at HTTP/1.1.
		// The certificate is in TLSConfig already, so it takes no file names.
		serve = func(l net.Listener) error { return s.server.ServeTLS(l, "", "") }
	}
	err := serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s netHTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
