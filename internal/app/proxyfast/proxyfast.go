// Package proxyfast serves gqlhash-proxy with [fasthttp] instead of net/http,
// on both sides: it takes the traffic and carries a forwarded request upstream.
//
// It's a package of its own so that only the command naming it links fasthttp,
// which keeps the default proxy free of an HTTP implementation it never serves
// with. The decision isn't here: every request goes through [proxy.Core], which
// is the same code the net/http underlay reaches.
//
// What this underlay can't do, and what the command documents: HTTP/1.1 only,
// on both sides, so HTTP/2 belongs in front of it. It has no cancellation
// signal tied to client disconnect either — [fasthttp.RequestCtx.Done] closes
// on server shutdown alone — so a client that hangs up doesn't stop the
// upstream work its request started. The forward is still bounded by
// -upstream.timeout, which [fasthttp.HostClient.DoTimeout] applies below.
// A protocol upgrade isn't forwarded; the proxy reads and
// hashes the whole body of every request, so an upgrade never reached the
// upstream intact under either underlay.
//
// [fasthttp]: https://github.com/valyala/fasthttp
package proxyfast

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"
	"github.com/valyala/fasthttp"

	"github.com/romshark/gqlhash/v2/internal/app/config"
	"github.com/romshark/gqlhash/v2/internal/app/proxy"
)

// Underlay is what the command hands [proxy.RunWith].
var Underlay = proxy.Underlay{
	Name:      "fasthttp",
	HTTP1Only: true,
	New:       New,
}

// server is the data-plane server on fasthttp, forwarding with its client.
type server struct {
	server *fasthttp.Server
	core   *proxy.Core
	client *fasthttp.HostClient

	// upstream is where a forwarded request goes,
	// in the parts a fasthttp request is built from.
	scheme, host, path []byte
	timeout            time.Duration
	log                zerolog.Logger
}

// New builds the underlay over core.
func New(
	core *proxy.Core, cfg config.Proxy, upstream *url.URL, log zerolog.Logger,
) proxy.DataPlaneServer {
	path := upstream.EscapedPath()
	if path == "" {
		path = "/"
	}
	s := &server{
		core:    core,
		scheme:  []byte(upstream.Scheme),
		host:    []byte(upstream.Host),
		path:    []byte(path),
		timeout: cfg.Upstream.Timeout,
		log:     log,
	}
	s.client = &fasthttp.HostClient{
		Addr:                upstream.Host,
		IsTLS:               upstream.Scheme == "https",
		MaxConns:            cfg.Upstream.MaxIdleConnsPerHost,
		MaxIdleConnDuration: 90 * time.Second,
		// Where net/http needs the idle connections closed on an interval,
		// fasthttp retires one by its own age, after the request using it is
		// done. Zero leaves the connection for as long as the upstream will.
		MaxConnDuration: cfg.Upstream.MaxConnLifetime,
		ReadTimeout:     cfg.Upstream.Timeout,
		WriteTimeout:    cfg.Upstream.Timeout,
		Dial: func(addr string) (net.Conn, error) {
			return fasthttp.DialTimeout(addr, 5*time.Second)
		},
	}
	s.server = &fasthttp.Server{
		Handler:      s.handle,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		// One over the limit, so a body that's too large still reaches the
		// handler and is answered with the same envelope the net/http underlay
		// gives. Anything past that fasthttp refuses itself,
		// which lands in ErrorHandler below and is answered the same way.
		MaxRequestBodySize:    int(min(cfg.Server.MaxBody+1, math.MaxInt32)),
		ErrorHandler:          s.handleReadError,
		Logger:                &logger{log: log},
		NoDefaultServerHeader: true,
		CloseOnShutdown:       true,
	}
	return s
}

func (s *server) Serve(listener net.Listener) error {
	return s.server.Serve(listener)
}

func (s *server) Shutdown(ctx context.Context) error {
	return s.server.ShutdownWithContext(ctx)
}

// handle answers one request. It's the fasthttp counterpart of the net/http
// handler, and the decision in the middle is the same call.
func (s *server) handle(ctx *fasthttp.RequestCtx) {
	start := time.Now()

	req := proxy.Request{IsGET: ctx.IsGet()}
	if req.IsGET {
		req.RawQuery = string(ctx.URI().QueryString())
	} else {
		// The body is read and owned by ctx, so the decision runs over those
		// bytes rather than a copy of them.
		req.Body = ctx.PostBody()
		req.BodyIsDocument = proxy.IsGraphQLContentType(
			string(ctx.Request.Header.ContentType()))
	}

	verdict, answer := s.core.Decide(req)
	if verdict != proxy.VerdictAllowed {
		if s.core.Debug() && verdict == proxy.VerdictRejected {
			// Why debug: a rejection is the path a flood takes,
			// so one event each is log volume the caller controls.
			s.log.Debug().
				Str("remote", ctx.RemoteAddr().String()).
				Str("method", string(ctx.Method())).
				Msg("the document is not on the allowlist")
		}
		s.answer(ctx, answer)
		s.core.Observe(verdict, start)
		return
	}

	if s.core.LogRequests() {
		s.log.Debug().Str("remote", ctx.RemoteAddr().String()).Msg("forwarding")
	}
	s.forward(ctx, req.Body, req.IsGET)
	// The duration includes the upstream answer,
	// so a dashboard can tell the proxy apart from the API behind it.
	s.core.Observe(proxy.VerdictAllowed, start)
}

// handleReadError answers a request fasthttp couldn't read. The handler never
// runs for one, so the count is raised through the core to keep the totals
// whole. The duration isn't recorded: nothing was decided, so nothing was timed.
func (s *server) handleReadError(ctx *fasthttp.RequestCtx, err error) {
	if s.core.Debug() {
		s.log.Debug().Err(err).Msg("rejecting a request that couldn't be read")
	}
	s.answer(ctx, s.core.ReadError(errors.Is(err, fasthttp.ErrBodyTooLarge)))
}

// answer writes what the core decided.
func (s *server) answer(ctx *fasthttp.RequestCtx, a proxy.Answer) {
	ctx.SetStatusCode(a.Code)
	ctx.SetContentType(s.core.ContentType())
	proxy.WriteErrorBody(ctx, a.Message, a.Extension)
}

// forward carries the request upstream and answers with what comes back.
func (s *server) forward(ctx *fasthttp.RequestCtx, body []byte, isGET bool) {
	req := fasthttp.AcquireRequest()
	res := fasthttp.AcquireResponse()
	defer func() {
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(res)
	}()

	ctx.Request.Header.CopyTo(&req.Header)
	removeHopByHop(&req.Header)
	s.setForwarded(ctx, req)

	// The upstream URL is the GraphQL endpoint, so its path replaces the one of
	// the request rather than being joined with it. The query is kept:
	// a GET carries the document in it.
	uri := req.URI()
	uri.SetSchemeBytes(s.scheme)
	uri.SetHostBytes(s.host)
	uri.SetPathBytes(s.path)
	uri.SetQueryStringBytes(ctx.URI().QueryString())
	req.Header.SetMethodBytes(ctx.Method())
	// The request carries the host of the upstream, as it would without a proxy.
	req.Header.SetHostBytes(s.host)
	if !isGET {
		// The body was read to find the document, so the same bytes go upstream.
		// SetBodyRaw keeps them rather than copying: they belong to ctx,
		// which outlives this call.
		req.SetBodyRaw(body)
	}

	if err := s.client.DoTimeout(req, res, s.timeout); err != nil {
		s.core.CountUpstreamError()
		code := http.StatusBadGateway
		if errors.Is(err, fasthttp.ErrTimeout) ||
			errors.Is(err, context.DeadlineExceeded) {
			code = http.StatusGatewayTimeout
		}
		s.log.Error().Err(err).Msg("forwarding upstream")
		s.answer(ctx, proxy.Answer{
			Code: code, Message: "upstream unavailable",
			Extension: "UPSTREAM_UNAVAILABLE",
		})
		return
	}

	res.Header.CopyTo(&ctx.Response.Header)
	removeHopByHop(&ctx.Response.Header)
	ctx.SetStatusCode(res.StatusCode())
	_, _ = ctx.Write(res.Body())
}

// header is what the two helpers below need of a fasthttp header,
// which the request and the response carry as separate types.
type header interface {
	Del(key string)
	Peek(key string) []byte
	Set(key, value string)
}

// hopByHopHeaders belong to one connection rather than to the message,
// so they stop here. The list is the one net/http/httputil carries,
// so both underlays forward the same headers.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// removeHopByHop drops the hop-by-hop headers, and the ones Connection names
// with them: a client naming a header there is asking for it not to be forwarded,
// and honouring that is what keeps a header from being smuggled past
// something in front of this proxy.
func removeHopByHop(h header) {
	if named := h.Peek("Connection"); len(named) > 0 {
		for token := range splitTokens(string(named)) {
			if token != "" {
				h.Del(token)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

// splitTokens yields the comma-separated tokens of v, trimmed.
func splitTokens(v string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for len(v) > 0 {
			var token string
			if i := indexByte(v, ','); i >= 0 {
				token, v = v[:i], v[i+1:]
			} else {
				token, v = v, ""
			}
			if !yield(trimOWS(token)) {
				return
			}
		}
	}
}

// setForwarded tells the upstream who the request came from.
//
// It follows the net/http underlay exactly,
// including what [net/http/httputil.ReverseProxy] does before its hook runs:
// the forwarding headers a client sent are dropped from the outbound request first,
// so that without -trust-forwarded a client can't put anything of its own in them.
func (s *server) setForwarded(ctx *fasthttp.RequestCtx, req *fasthttp.Request) {
	// What the client claimed, read before it's dropped.
	var chain, host, proto string
	if s.core.TrustForwarded() {
		chain = string(ctx.Request.Header.Peek("X-Forwarded-For"))
		host = string(ctx.Request.Header.Peek("X-Forwarded-Host"))
		proto = string(ctx.Request.Header.Peek("X-Forwarded-Proto"))
	}
	// An RFC 7239 header of the client would reach the upstream unchecked.
	req.Header.Del("Forwarded")
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")

	clientIP := ""
	if addr, ok := ctx.RemoteAddr().(*net.TCPAddr); ok && addr.IP != nil {
		clientIP = addr.IP.String()
	} else if h, _, err := net.SplitHostPort(ctx.RemoteAddr().String()); err == nil {
		clientIP = h
	}
	if clientIP != "" {
		if chain != "" {
			clientIP = chain + ", " + clientIP
		}
		req.Header.Set("X-Forwarded-For", clientIP)
	}

	if host == "" {
		host = string(ctx.Host())
	}
	req.Header.Set("X-Forwarded-Host", host)

	if proto == "" {
		proto = "http"
		if ctx.IsTLS() {
			proto = "https"
		}
	}
	req.Header.Set("X-Forwarded-Proto", proto)
}

// logger hands what the fasthttp server reports to zerolog, at debug level:
// a malformed request is the caller's doing and a flood of them is the caller's
// to make, which is where rejections sit too.
type logger struct{ log zerolog.Logger }

func (l *logger) Printf(format string, args ...any) {
	l.log.Debug().Msgf(format, args...)
}

// indexByte is strings.IndexByte,
// kept beside trimOWS so the two token helpers read together.
func indexByte(s string, c byte) int {
	for i := range len(s) {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// trimOWS drops the optional whitespace HTTP allows around a token.
func trimOWS(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
