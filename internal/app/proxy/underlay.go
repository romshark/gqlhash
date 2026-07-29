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

// dataPlaneServer serves the requests the proxy exists for: it takes them from
// clients, decides on them and forwards what's allowed. Which HTTP
// implementation it is depends on the command a run was started as, see
// [Underlay].
//
// It's the counterpart of the control server on -control.listen, which answers
// /metrics, /status and /reload. That one is net/http under every command and
// sits on no hot path, so it isn't behind an interface at all.
//
// The decision isn't behind this one either. Every implementation reaches
// [proxy.decide], so what a request is answered with doesn't depend on which
// carried it.
type dataPlaneServer interface {
	// Serve takes the traffic on listener until Shutdown. It returns nil where
	// it was shut down and an error where it failed, which the underlays report
	// differently and this hides.
	Serve(listener net.Listener) error

	// Shutdown stops taking requests and waits for the ones in flight, bounded
	// by the deadline of ctx.
	Shutdown(ctx context.Context) error
}

// DataPlaneServer is [dataPlaneServer] under a name an underlay in another
// package can implement.
type DataPlaneServer = dataPlaneServer

// Underlay is an HTTP implementation a command can serve with. A command that
// passes none serves with net/http.
//
// It's given rather than chosen by a flag because the choice is what a binary
// is built out of: only the command that names an underlay links it, so the
// default proxy carries no HTTP implementation it doesn't serve with.
type Underlay struct {
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

// netHTTPServer is the net/http underlay, the default.
//
// [http.Server.Serve] answers an ordinary shutdown with [http.ErrServerClosed]
// where fasthttp answers with nil, so that difference is flattened here rather
// than at every call.
type netHTTPServer struct{ server *http.Server }

func (s netHTTPServer) Serve(listener net.Listener) error {
	err := s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s netHTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
