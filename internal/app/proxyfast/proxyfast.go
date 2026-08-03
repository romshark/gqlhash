// Package proxyfast serves gqlhash-proxy with [fasthttp] instead of net/http,
// on both sides: it takes the traffic and carries a forwarded request upstream.
//
// It's a package of its own so that only the command naming it links fasthttp,
// which keeps the default proxy free of an HTTP implementation it never serves
// with. The decision isn't here: every request goes through [proxy.Core], which
// is the same code the net/http server reaches.
//
// EXPERIMENTAL, AND NOT FOR PRODUCTION USE. It exists for benchmarking,
// and to prove the acceptance suite holds two implementations to the same rules rather
// than describing one. Its parser is younger and far less exercised than net/http's,
// and a request trailer reaches the API as an ordinary header:
// fasthttp merges trailer fields into the header set before a handler runs,
// declared or not. See GQLHASH_PROXY_FHTTP.md.
//
// What it can't do, and what the command documents: HTTP/1.1 only on both sides,
// so HTTP/2 belongs in front of it. No cancellation on client disconnect either —
// [fasthttp.RequestCtx.Done] closes on server shutdown alone —
// so a client that hangs up doesn't stop the upstream work it started.
// The forward is still bounded by -upstream.timeout through
// [fasthttp.HostClient.DoTimeout], except for an event stream,
// which is bounded only at its headers; see newHostClient and [server.within].
// A protocol upgrade isn't forwarded: the offer goes with the other hop-by-hop
// headers in removeHopByHop, and an upstream answering 101 anyway gets a 502.
//
// [fasthttp]: https://github.com/valyala/fasthttp
package proxyfast

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/valyala/fasthttp"

	"github.com/romshark/gqlhash/v2/internal/app/config"
	"github.com/romshark/gqlhash/v2/internal/app/proxy"
)

// ServerImpl is what the command hands [proxy.RunWith].
var ServerImpl = proxy.ServerImpl{
	Name:      "fasthttp",
	HTTP1Only: true,
	New:       New,
}

// server is the data-plane server on fasthttp, forwarding with its client.
type server struct {
	server *fasthttp.Server
	core   *proxy.Core
	client *fasthttp.HostClient

	// streamClient carries a forward relayed as it arrives. A client of its own
	// because the bound belongs to the connection: fasthttp's one read deadline
	// covers the body too, so the ordinary client would cut a stream at
	// -upstream.timeout. This one has none; [server.within] bounds the headers.
	streamClient *fasthttp.HostClient

	// live are the streams being written right now. A drain waits for what's in
	// flight and a subscription never finishes, so the shutdown ends these first.
	// fasthttp writes a body stream after the handler returns,
	// so nothing else knows they exist.
	live sync.Map

	// upstream is where a forwarded request goes,
	// in the parts a fasthttp request is built from.
	scheme, host, path []byte
	timeout            time.Duration
	log                zerolog.Logger

	// serveTLS is whether -server.tls.cert was given. Kept here rather than read
	// off the fasthttp server, which fills its own TLSConfig in on the way up.
	serveTLS bool
}

// continue100 is the one expectation the proxy meets: it reads every body,
// so it answers the continuation itself and forwards no Expect header.
var continue100 = []byte("100-continue")

// readBufferSize takes a request line and its headers. Sized for a GET
// carrying an allowlisted document; a connection costs it while it's open.
const readBufferSize = 64 << 10

func New(
	core *proxy.Core, cfg config.Proxy, upstream *url.URL, log zerolog.Logger,
) proxy.DataPlaneServer {
	path := upstream.EscapedPath()
	if path == "" {
		path = "/"
	}
	s := &server{
		core:     core,
		scheme:   []byte(upstream.Scheme),
		host:     []byte(upstream.Host),
		path:     []byte(path),
		timeout:  upstreamTimeout(cfg.Upstream.Timeout),
		log:      log,
		serveTLS: cfg.Server.TLSCert != nil,
	}
	s.client = newHostClient(cfg, upstream, false)
	// The same upstream reached the same way, without a read deadline and
	// answering into a body stream. A separate client costs a pool of its own,
	// drawn from only by a request asking for a stream.
	s.streamClient = newHostClient(cfg, upstream, true)
	s.server = &fasthttp.Server{
		Handler:      s.handle,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		// -server.write-timeout bounds writing one answer, so it would cut an
		// event stream. fasthttp takes a per-request write timeout only where
		// it's positive, so "unbounded" is spelled as a bound no process outlives.
		// The client's ask decides, since this runs before there's an answer to look at;
		// one that asked for a stream and gets an ordinary answer writes it and is done.
		HeaderReceived: func(h *fasthttp.RequestHeader) fasthttp.RequestConfig {
			if proxy.AcceptsEventStream(string(h.Peek("Accept"))) {
				return fasthttp.RequestConfig{WriteTimeout: streamWriteTimeout}
			}
			return fasthttp.RequestConfig{}
		},
		// One over the limit, so a body that's too large still reaches the
		// handler and gets the same envelope net/http gives.
		// Past that fasthttp refuses it into ErrorHandler, answered the same way.
		MaxRequestBodySize: int(min(cfg.Server.MaxBody+1, math.MaxInt32)),
		// fasthttp defaults this to 4KiB, past which a GET carrying its document
		// in the query string is refused, where net/http grows its own.
		// A buffer per connection rather than a limit, so it's sized for what a GET
		// carries and not for -server.max-body.
		// GQLHASH_PROXY_FHTTP.md names the ceiling it leaves.
		ReadBufferSize: readBufferSize,
		ErrorHandler:   s.handleReadError,
		// fasthttp answers text/plain where a handler set none.
		// The client reads the API's answer, headers included, so none stays none.
		NoDefaultContentType:  true,
		Logger:                &logger{log: log},
		NoDefaultServerHeader: true,
		CloseOnShutdown:       true,
		TLSConfig:             config.TLSServerConfig(cfg.Server.TLSCert),
	}
	return s
}

// noBound is how a timeout that's off is spelled where fasthttp takes a duration
// and reads 0 as no time at all rather than no limit: a bound no process outlives.
const noBound = 100 * 365 * 24 * time.Hour

// upstreamTimeout is -upstream.timeout as this implementation carries it.
//
// The flag is off at 0, which net/http serves as a forward with no bound of its own.
// fasthttp has no such value: DoTimeout with 0 is a deadline already past,
// so every forward is a 504, and MaxConnWaitTimeout at 0 waits for no connection at all.
// Both take [noBound] instead, so the flag means the same thing under either command.
func upstreamTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return noBound
	}
	return d
}

// newHostClient builds the client a forward is carried with. With stream it
// reads the answer into a body stream and puts no deadline on the connection,
// which lets a subscription outlive -upstream.timeout.
func newHostClient(
	cfg config.Proxy, upstream *url.URL, stream bool,
) *fasthttp.HostClient {
	// -upstream.timeout bounds an exchange and a stream is none,
	// so a streaming client carrying one would lose the subscription at the timeout:
	// fasthttp's read deadline covers the body too. [server.do] bounds the headers.
	readTimeout := cfg.Upstream.Timeout
	if stream {
		readTimeout = 0
	}
	return &fasthttp.HostClient{
		Addr:  upstream.Host,
		IsTLS: upstream.Scheme == "https",
		// The same trust the net/http server uses,
		// so -upstream.tls.ca means one thing whichever binary is serving.
		TLSConfig: config.TLSClientConfig(cfg.Upstream.TLSCA),
		// fasthttp has no separate limit for connections opened, so MaxConns is
		// both and a request past it fails with ErrNoFreeConns rather than
		// being redialed the way net/http does. MaxConnWaitTimeout keeps
		// -upstream.max-idle-conns-per-host a pool size and not a concurrency limit:
		// the surplus waits, bounded by -upstream.timeout.
		MaxConns:            cfg.Upstream.MaxIdleConnsPerHost,
		MaxConnWaitTimeout:  upstreamTimeout(cfg.Upstream.Timeout),
		MaxIdleConnDuration: 90 * time.Second,
		// A pooled connection may have been closed by the upstream since,
		// which a restart does to all of them. The request never reached it,
		// so it goes again on a fresh one rather than turning a rolling deploy into a
		// burst of 502s. Once, and only where the connection failed:
		// a timeout means the API may have the request already.
		RetryIfErr: retryDeadConn,
		// Where net/http needs the idle connections closed on an interval,
		// fasthttp retires one by its own age once the request using it is done.
		// Zero keeps it for as long as the upstream will.
		MaxConnDuration:    cfg.Upstream.MaxConnLifetime,
		ReadTimeout:        readTimeout,
		WriteTimeout:       cfg.Upstream.Timeout,
		StreamResponseBody: stream,
		Dial: func(addr string) (net.Conn, error) {
			return fasthttp.DialTimeout(addr, 5*time.Second)
		},
	}
}

func (s *server) Serve(listener net.Listener) error {
	if s.serveTLS {
		// The certificate is in TLSConfig already, so this takes no file names.
		// fasthttp wraps the listener; there's no h2 to negotiate either way.
		return s.server.ServeTLS(listener, "", "")
	}
	return s.server.Serve(listener)
}

func (s *server) Shutdown(ctx context.Context) error {
	// The streams end first, so the drain has only exchanges left to wait for.
	// Closing the reader fails the copy fasthttp is mid-way through, ending the
	// write and letting the connection close.
	s.live.Range(func(key, _ any) bool {
		key.(*upstreamStream).endForShutdown()
		return true
	})
	return s.server.ShutdownWithContext(ctx)
}

// handle answers one request, the fasthttp counterpart of the net/http handler.
// The decision in the middle is the same call.
func (s *server) handle(ctx *fasthttp.RequestCtx) {
	start := time.Now()

	// An expectation this doesn't understand is refused rather than forwarded,
	// as RFC 9110 requires of any recipient. net/http refuses one inside its own server;
	// fasthttp knows only 100-continue and would carry the rest upstream.
	// The check is here so the two answer alike.
	//
	// What counts still differs: net/http refuses before a handler exists,
	// so nothing is counted there, while this is a refusal the proxy made.
	// That holds for every request an HTTP implementation turns away on its own,
	// and the acceptance suite pins only what both keep — the status,
	// and that nothing is forwarded.
	if expect := ctx.Request.Header.Peek("Expect"); len(expect) > 0 &&
		!bytes.EqualFold(expect, continue100) {
		verdict := s.core.ExpectationFailed()
		ctx.SetStatusCode(fasthttp.StatusExpectationFailed)
		s.core.Observe(verdict, start)
		return
	}

	req := proxy.Request{
		IsGET: ctx.IsGet(),
		// Read whatever the method: a request whose document is its body and
		// that names a query parameter too is refused, not forwarded.
		RawQuery: string(ctx.URI().QueryString()),
	}
	if req.IsGET {
		// fasthttp reads the body whatever the method, so a body on a GET is
		// there to be seen. The decision refuses it: the document is in the
		// query string and a body is a second place it could be.
		req.HasBody = len(ctx.PostBody()) > 0
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
			// Debug: a rejection is the path a flood takes,
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
	// Includes the upstream answer,
	// so a dashboard can tell the proxy apart from the API behind it.
	s.core.Observe(proxy.VerdictAllowed, start)
}

// retryDeadConn reports whether a failed forward goes again. Only the first attempt,
// and only where the connection failed: the upstream never saw those.
func retryDeadConn(_ *fasthttp.Request, attempts int, err error) (
	resetTimeout, retry bool,
) {
	if attempts > 1 {
		return false, false
	}
	return false, errors.Is(err, fasthttp.ErrConnectionClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

// handleReadError answers a request fasthttp couldn't read. The handler never
// runs for one, so the core raises the count to keep the totals whole,
// and the answer is timed like any other that produced a status.
func (s *server) handleReadError(ctx *fasthttp.RequestCtx, err error) {
	start := time.Now()
	if s.core.Debug() {
		s.log.Debug().Err(err).Msg("rejecting a request that couldn't be read")
	}
	verdict, answer := s.core.ReadError(errors.Is(err, fasthttp.ErrBodyTooLarge))
	s.answer(ctx, answer)
	s.core.Observe(verdict, start)
}

func (s *server) answer(ctx *fasthttp.RequestCtx, a proxy.Answer) {
	ctx.SetStatusCode(a.Code)
	ctx.SetContentType(s.core.ContentType())
	proxy.WriteErrorBody(ctx, a.Message, a.Extension)
}

// streamWriteTimeout stands in for -server.write-timeout while an event stream
// is being written. See the HeaderReceived hook in New.
const streamWriteTimeout = noBound

// upstreamStream is the API's answer, read by the server as it writes it to the client.
//
// It owns the request and the answer it was read from: fasthttp writes a body
// stream after the handler returns, so releasing them there would close the
// stream out from under it. Closing this releases them, and fasthttp closes a
// body stream whether it finished writing or the client left.
type upstreamStream struct {
	reader io.Reader
	req    *fasthttp.Request
	res    *fasthttp.Response
	server *server

	// read is whether the stream reached its end.
	// One that didn't is a stream the API is still writing into, see Close.
	read bool
}

// endForShutdown closes the reader under the copy that's writing it,
// which is the only way to end a stream fasthttp is already writing.
func (s *upstreamStream) endForShutdown() {
	if c, ok := s.reader.(fasthttp.ReadCloserWithError); ok {
		_ = c.CloseWithError(errDraining)
	}
}

func (s *upstreamStream) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if err != nil {
		// EOF or a failure: nothing more to come either way, and only the first
		// leaves the connection reusable — fasthttp's call once it's told.
		s.read = errors.Is(err, io.EOF)
	}
	return n, err
}

// Close releases what the answer was read from,
// returning the connection to the pool — but only where the stream ended.
//
// Where it didn't, the client left mid-subscription and the API is still
// writing events into that connection. Pooling it hands the next subscription a
// socket with somebody else's events queued on it, read as a status line:
// one abandoned stream fails the next two forwards, and a browser tab closing is
// exactly this case. Releasing the response closes the stream with no error,
// which fasthttp reads as a clean end, so the failure is handed over explicitly.
func (s *upstreamStream) Close() error {
	s.server.live.Delete(s)
	if !s.read {
		if c, ok := s.reader.(fasthttp.ReadCloserWithError); ok {
			_ = c.CloseWithError(errClientLeftMidStream)
		}
	}
	fasthttp.ReleaseRequest(s.req)
	fasthttp.ReleaseResponse(s.res)
	return nil
}

// errClientLeftMidStream retires an upstream connection whose stream was
// abandoned rather than returning it to the pool.
var errClientLeftMidStream = errors.New("the client left mid-stream")

// errDraining ends a stream because the proxy is shutting down.
var errDraining = errors.New("the proxy is draining")

// maxBufferedAnswer bounds an answer read into memory on the streaming path,
// where a client asked for a stream and the API answered something else.
// That's the one place this implementation reads a body the streaming client gave it,
// and that client ignores MaxResponseBodySize: without a bound, an API that never
// stops sending is memory exhaustion one request long. The net/http server
// relays as it arrives rather than holding, so it needs none.
const maxBufferedAnswer = 64 << 20

var errAnswerTooLarge = errors.New("the upstream answer is too large to buffer")

// forward carries the request upstream and answers with what comes back.
//
// The named result is for the deferred release, not the caller: an answer relayed as
// a stream is written after this returns and owns what it was read from.
// This returns once the headers are in, so what [server.handle] observes
// for a stream is already the time to its headers.
func (s *server) forward(
	ctx *fasthttp.RequestCtx, body []byte, isGET bool,
) (streamed bool) {
	req := fasthttp.AcquireRequest()
	res := fasthttp.AcquireResponse()
	// Released here unless the answer is a stream, which outlives this call.
	defer func() {
		if !streamed {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(res)
		}
	}()

	// The client's ask decides how the forward is carried, since the choice
	// comes before there's an answer to look at.
	// The API's answer decides how it's relayed, further down.
	wantsStream := proxy.AcceptsEventStream(
		string(ctx.Request.Header.Peek("Accept")))

	ctx.Request.Header.CopyTo(&req.Header)
	removeHopByHop(&req.Header)
	for _, name := range requestOnlyHeaders {
		req.Header.Del(name)
	}
	s.setForwarded(ctx, req)

	// The upstream URL is the GraphQL endpoint, so its path replaces the
	// request's rather than being joined with it.
	// The query is kept: a GET carries the document in it.
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
		if wantsStream {
			// Copied, because a streaming forward can outlive this handler:
			// one whose headers never arrive is abandoned rather than stopped,
			// and the goroutine carrying it may still retry on a fresh connection.
			// SetBodyRaw would leave that retry pointing at ctx's buffer,
			// since recycled into another request — a document this proxy never hashed,
			// sent under the decision made about another.
			req.SetBody(body)
		} else {
			// SetBodyRaw keeps the bytes rather than copying them: nothing here
			// outlives ctx, which owns them.
			req.SetBodyRaw(body)
		}
	}

	// One deadline for the whole forward, so the parts of a streaming one that
	// aren't the stream are bounded together rather than each afresh.
	deadline := time.Now().Add(s.timeout)
	err := s.do(ctx, req, res, wantsStream, deadline)
	if errors.Is(err, errAbandoned) {
		// The headers never came and the forward was left running,
		// so req and res belong to it now.
		return true
	}
	if err != nil {
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
		return false
	}

	if res.StatusCode() == fasthttp.StatusSwitchingProtocols {
		// removeHopByHop dropped the offer above, so a 101 answers a question
		// nobody asked. Relaying it would hand the client a channel to the API.
		s.core.CountUpstreamError()
		s.log.Error().Msg("the upstream switched protocols; nothing offered an upgrade")
		s.answer(ctx, proxy.Answer{
			Code: http.StatusBadGateway, Message: "upstream unavailable",
			Extension: "UPSTREAM_UNAVAILABLE",
		})
		return false
	}

	// An answer read into a body stream that isn't a stream after all:
	// the client asked for one and the API answered something else, which GraphQL
	// over SSE allows for a request it refuses. Read whole, as the ordinary path does,
	// so a truncated answer can't reach a client as a complete one.
	if wantsStream && !proxy.IsEventStream(string(res.Header.ContentType())) {
		var payload []byte
		err := s.within(ctx, req, res, deadline,
			"the upstream answer didn't complete",
			func() (err error) {
				// A bodyless answer — a 204, a 304 — leaves no stream to read.
				// Reading it anyway dereferences nil in the goroutine below,
				// where no recover reaches it: one allowed request against an
				// API that answers 204 takes the process down.
				stream := res.BodyStream()
				if stream == nil {
					return nil
				}
				// Bounded, because this reads into memory and the streaming
				// client ignores MaxResponseBodySize. An API answering
				// application/json and then never stopping drives one request
				// from 13 MB to 15 GB resident in eight seconds:
				// the deadline answers the client but abandons the read, which runs on.
				limited := io.LimitReader(stream, maxBufferedAnswer+1)
				payload, err = io.ReadAll(limited)
				if err == nil && int64(len(payload)) > maxBufferedAnswer {
					return errAnswerTooLarge
				}
				return err
			})
		if errors.Is(err, errAbandoned) {
			return true
		}
		if err != nil {
			s.core.CountUpstreamError()
			s.log.Error().Err(err).Msg("reading the upstream answer")
			s.answer(ctx, proxy.Answer{
				Code: http.StatusBadGateway, Message: "upstream unavailable",
				Extension: "UPSTREAM_UNAVAILABLE",
			})
			return false
		}
		res.SetBodyRaw(payload)
	}

	res.Header.CopyTo(&ctx.Response.Header)
	// CopyTo carries the setting with the headers, so the server's own is set again:
	// an answer that named no type reaches the client with none.
	ctx.Response.Header.SetNoDefaultContentType(true)
	removeHopByHop(&ctx.Response.Header)
	ctx.SetStatusCode(res.StatusCode())

	if wantsStream && proxy.IsEventStream(string(res.Header.ContentType())) {
		// Relayed as it arrives: fasthttp writes a body stream chunk by chunk,
		// flushing each, so an event reaches the client when the API sent it
		// and not when the answer ends — which, for a subscription, is never.
		stream := res.BodyStream()
		if stream == nil {
			// An event stream carrying no body: the headers are the whole answer,
			// so there's nothing to relay and nothing to own.
			return false
		}
		relayed := &upstreamStream{
			reader: stream, req: req, res: res, server: s,
		}
		s.live.Store(relayed, struct{}{})
		ctx.SetBodyStream(relayed, -1)
		return true
	}
	_, _ = ctx.Write(res.Body())
	return false
}

// errAbandoned is a forward that outlived -upstream.timeout and was left
// running: nothing interrupts a fasthttp client mid-request, so the request and
// the answer belong to that goroutine until it ends.
// The caller lets them go without releasing them.
var errAbandoned = errors.New("the forward was abandoned")

// do carries the forward, with or without a read deadline on the connection.
//
// The ordinary client bounds the whole exchange, which is what
// -upstream.timeout means. The streaming one has no deadline at all,
// since fasthttp's covers the body and a stream has no length to bound;
// [server.within] bounds the parts that aren't the stream against the same deadline,
// so naming text/event-stream can't buy a client a wider bound than any other gets.
func (s *server) do(
	ctx *fasthttp.RequestCtx, req *fasthttp.Request, res *fasthttp.Response,
	wantsStream bool, deadline time.Time,
) error {
	if !wantsStream {
		return s.client.DoTimeout(req, res, s.timeout)
	}
	res.StreamBody = true
	return s.within(ctx, req, res, deadline,
		"the upstream sent no answer headers",
		func() error { return s.streamClient.Do(req, res) })
}

// within runs work under the exchange deadline.
//
// Work that doesn't finish in time is abandoned rather than stopped, since
// nothing interrupts a fasthttp client mid-request: the client keeps req and res,
// this answers the 504 without them, and the release happens when the goroutine ends.
// An API that takes a connection and says nothing then costs
// one goroutine until it lets go, not a forward per request that reached it.
func (s *server) within(
	ctx *fasthttp.RequestCtx, req *fasthttp.Request, res *fasthttp.Response,
	deadline time.Time, what string, work func() error,
) error {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	done := make(chan error, 1)
	go func() { done <- work() }()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		// Abandoned, not stopped — nothing interrupts a fasthttp client mid-request.
		// A read of the answer's body can be stopped:
		// closing the stream fails it now instead of leaving it running
		// against an API that never stops sending.
		if stream, ok := res.BodyStream().(fasthttp.ReadCloserWithError); ok {
			_ = stream.CloseWithError(errAbandoned)
		}
		go func() {
			<-done
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(res)
		}()
		s.core.CountUpstreamError()
		s.log.Error().Dur("timeout", s.timeout).Msg(what)
		s.answer(ctx, proxy.Answer{
			Code: http.StatusGatewayTimeout, Message: "upstream unavailable",
			Extension: "UPSTREAM_UNAVAILABLE",
		})
		return errAbandoned
	}
}

// header is what the helpers below need of a fasthttp header,
// which the request and the response carry as separate types.
type header interface {
	Del(key string)
	Peek(key string) []byte
	Set(key, value string)
}

// hopByHopHeaders belong to one connection rather than to the message,
// so they stop here. The same list net/http/httputil carries,
// so both implementations forward the same headers.
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

// requestOnlyHeaders stop at the proxy too, but belong to the request alone,
// so they're kept out of the list above rather than deleted from every answer.
var requestOnlyHeaders = []string{
	// The expectation was the proxy's to meet and it met it:
	// the body is read and hashed, and a 100 sent if one was asked for.
	// Forwarding it makes the API run the handshake again for a body already in flight.
	"Expect",
}

// removeHopByHop drops the hop-by-hop headers and the ones Connection names with them:
// naming a header there asks for it not to be forwarded,
// and honouring that keeps one from being smuggled past whatever is in front.
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
// It follows the net/http server exactly, including what
// [net/http/httputil.ReverseProxy] does before its hook runs:
// the client's forwarding headers are dropped from the outbound request first,
// so without -trust-forwarded a client can't put anything of its own in them.
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
// a malformed request is the caller's doing and a flood of them the caller's to make,
// where rejections sit too.
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
