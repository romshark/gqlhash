package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
	"github.com/romshark/gqlhash/v2/parser"
)

// upstreamCopyBuffer is what one answer is copied through. A GraphQL answer that
// needs more than this takes more than one turn around the loop, which is
// cheaper than holding 32KiB per request in flight.
const upstreamCopyBuffer = 8 * 1024

// bufferPool gives [httputil.ReverseProxy] the buffers it copies answers through,
// so that forwarding doesn't allocate one per request.
type bufferPool struct{ pool sync.Pool }

func (b *bufferPool) Get() []byte  { return b.pool.Get().([]byte) }
func (b *bufferPool) Put(p []byte) { b.pool.Put(p) } //nolint:staticcheck // the interface takes a slice

// counters of what the proxy decided, one per cache line: packed,
// two cores raising different counters bounce one line between them,
// ~10.7ns against ~8.6ns per raise.
// A flood of one decision contends whatever the layout.
type counters struct {
	allowed   paddedCounter
	rejected  paddedCounter
	malformed paddedCounter
	tooLarge  paddedCounter
	ambiguous paddedCounter
	tooDeep   paddedCounter
	upstream  paddedCounter
}

// paddedCounter is a counter on a cache line of its own:
// 56 bytes beside the 8 of the counter.
type paddedCounter struct {
	atomic.Uint64
	_ [56]byte
}

// decisions is what a proxy did, read at one moment. It's a struct and not four
// returns so that a caller names what it takes: the four are the same type,
// and nothing catches a transposition at the call or a reorder of the counters.
type decisions struct {
	allowed   uint64
	rejected  uint64
	malformed uint64
	tooLarge  uint64
	ambiguous uint64
	tooDeep   uint64
	upstream  uint64
}

// snapshot returns what the proxy decided.
func (c *counters) snapshot() decisions {
	return decisions{
		allowed:   c.allowed.Load(),
		rejected:  c.rejected.Load(),
		malformed: c.malformed.Load(),
		tooLarge:  c.tooLarge.Load(),
		ambiguous: c.ambiguous.Load(),
		tooDeep:   c.tooDeep.Load(),
		upstream:  c.upstream.Load(),
	}
}

// proxy checks the document of a request against an allowlist and
// forwards it upstream or rejects it.
type proxy struct {
	allowlist *allowlist.Allowlist
	upstream  *httputil.ReverseProxy
	log       zerolog.Logger
	counters  counters

	options gqlhash.Options
	maxBody int64

	// upstreamTimeout bounds a forward whole, see -upstream.timeout.
	upstreamTimeout time.Duration
	allowBatch      bool
	opaqueErrors    bool
	logRequests     bool
	trustForwarded  bool

	// debug says whether the logger keeps a debug event.
	// The level is set once at startup, so it's read once instead of per event.
	debug bool

	// metrics are always kept. The control server is what exposes them, and a
	// run always has one, so a request costs the clock read and the observation
	// either way and the hot path has nothing to decide.
	metrics *metrics

	newHash   func() hash.Hash
	states    sync.Pool
	drainOnce sync.Once

	// draining is closed when the shutdown starts. A drain waits for the
	// requests in flight, and a subscription is in flight by definition —
	// it has no natural end to wait for — so a proxy carrying one sat out the
	// whole -server.shutdown-timeout and then exited 1, on every deploy.
	// Streams watch this and end; everything else is waited for as before.
	draining chan struct{}
}

// Draining is closed when the shutdown starts, for an underlay in another
// package to end its streams on. See [proxy.draining].
func (c *Core) Draining() <-chan struct{} { return c.p.draining }

// StartDraining closes it, once however many times it's called.
func (p *proxy) StartDraining() {
	p.drainOnce.Do(func() { close(p.draining) })
}

// The buffers a pooled state starts with, sized for the request the proxy mostly
// sees: a document of a few hundred bytes carrying a handful of them.
const (
	defaultBodyBuffer    = 8192
	defaultScratchBuffer = 4096

	// defaultSumBuffer holds the widest digest -hash offers, which is sha3-512.
	defaultSumBuffer = 64

	// defaultSpans is one per document of a batch, and one is the common case.
	defaultSpans = 8

	// maxRetainedBuffer is the largest buffer a state carries back into the pool.
	//
	// -server.max-body allows a megabyte by default, so a burst of requests that
	// size would otherwise leave the pool holding one per concurrent request for
	// the life of the process. Releasing above this costs an allocation on the
	// rare large request and keeps the common one free.
	//
	// The parser holds the same policy for the same reason,
	// see parser.maxRetainedBufferSize.
	maxRetainedBuffer = 64 << 10

	// maxRetainedSpans bounds the same for a batch: a megabyte of tiny documents
	// is tens of thousands of them, and the pool shouldn't keep the room for it.
	maxRetainedSpans = 1024
)

// state is everything one request needs. It's pooled, so a request that fits
// allocates nothing on the checking path.
type state struct {
	// body holds the request body. The document is a subslice of it.
	body []byte

	// scratch takes the document only when it needs unescaping or decoding.
	scratch []byte

	sum []byte

	spans  []span
	hash   hash.Hash
	parser *parser.Parser[[]byte]

	// writer relays the answer, releasing what bounds an exchange where the
	// answer turns out to be an event stream.
	// It's here so that forwarding costs no allocation of its own.
	writer streamWriter
}

// proxyConfig configures a [proxy]. Its zero value leaves nothing out of the hash,
// rejects batches, reports why it rejected and trusts no forwarding header.
type proxyConfig struct {
	// Options is what the canonical form leaves out.
	options gqlhash.Options

	// AllowBatch accepts a JSON array of requests. Every document must be allowed.
	allowBatch bool

	// OpaqueErrors answers every rejection with 403 and no detail.
	opaqueErrors bool

	// LogRequests logs a forwarded request at debug level.
	logRequests bool

	// TrustForwarded keeps the X-Forwarded-* headers of the incoming request and
	// appends to them, instead of replacing them with the direct peer.
	//
	// Requirement: only set this where a trusted load balancer is in front.
	// A client that reaches the proxy directly can claim any address.
	trustForwarded bool

	// MaxBody is the largest request body to accept, in bytes.
	maxBody int64

	// upstreamTimeout is -upstream.timeout, which bounds a forward whole.
	upstreamTimeout time.Duration
}

// newProxy returns a proxy forwarding to upstream.
func newProxy(
	allowlist *allowlist.Allowlist,
	upstream *url.URL,
	newHash func() hash.Hash,
	config proxyConfig,
	transport http.RoundTripper,
	log zerolog.Logger,
) *proxy {
	p := &proxy{
		allowlist: allowlist,
		log:       log,
		options:   config.options, maxBody: config.maxBody,
		upstreamTimeout: config.upstreamTimeout,
		allowBatch:      config.allowBatch, opaqueErrors: config.opaqueErrors,
		trustForwarded: config.trustForwarded,
		debug:          log.GetLevel() <= zerolog.DebugLevel,
		newHash:        newHash,
		draining:       make(chan struct{}),
	}
	// The metrics read the counters of this proxy, which is why they're built
	// here rather than handed in: a caller would need the proxy to build them
	// and the proxy to be built with them.
	p.metrics = newMetrics(&p.counters, allowlist)
	p.logRequests = config.logRequests && p.debug
	p.states.New = func() any {
		return &state{
			body:    make([]byte, 0, defaultBodyBuffer),
			scratch: make([]byte, 0, defaultScratchBuffer),
			sum:     make([]byte, 0, defaultSumBuffer),
			spans:   make([]span, 0, defaultSpans),
			hash:    newHash(),
			parser:  parser.NewParser[[]byte](0),
		}
	}
	p.upstream = &httputil.ReverseProxy{
		// Without a pool ReverseProxy allocates 32KiB to copy every answer,
		// which is most of what forwarding costs when the answers are small.
		BufferPool: &bufferPool{pool: sync.Pool{
			New: func() any { return make([]byte, upstreamCopyBuffer) },
		}},
		Rewrite: func(r *httputil.ProxyRequest) {
			// The upstream URL is the GraphQL endpoint,
			// so it replaces the path instead of prefixing it. SetURL would join the
			// two and turn a request to /graphql into /graphql/graphql.
			r.Out.URL.Scheme = upstream.Scheme
			r.Out.URL.Host = upstream.Host
			r.Out.URL.Path, r.Out.URL.RawPath = upstream.Path, upstream.RawPath
			// The query string of the client, verbatim. ReverseProxy drops the
			// parameters net/url can't parse before this runs, which for a
			// `;`-separated one is all of them: an allowed GET reached the API
			// carrying no document at all. Restoring it is safe because the
			// reading rule already splits on `;` as well as `&`, so a document
			// named twice either way is refused rather than forwarded.
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			// An empty Host makes the request carry the host of the upstream URL.
			r.Out.Host = ""
			// A protocol upgrade stops here. ReverseProxy strips these with the
			// other hop-by-hop headers and then puts them back for an upgrade,
			// which is how one allowlisted document used to buy a tunnel:
			// the API answered 101 and everything the client wrote afterwards
			// reached it without passing the allowlist again. This runs after
			// that, so what it drops stays dropped, and the answer's 101 then
			// has nothing to match.
			r.Out.Header.Del("Upgrade")
			r.Out.Header.Del("Connection")
			// The expectation was the proxy's to meet and it met it:
			// the body has been read and hashed, and a 100 was sent if one was asked for.
			// Forwarding it makes the API run the handshake again for a body
			// already in flight, and ReverseProxy relays the answer's 1xx as
			// well, so a client that asked for one continuation received two.
			r.Out.Header.Del("Expect")
			setForwarded(r, p.trustForwarded)
		},
		ModifyResponse: func(res *http.Response) error {
			// Nothing offered an upgrade, so a 101 is an API answering a
			// question nobody asked. Relaying it would splice the two
			// connections together on the strength of that answer alone.
			if res.StatusCode == http.StatusSwitchingProtocols {
				return errUpstreamSwitchedProtocols
			}
			return nil
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// Reached, so whatever [proxy.ServeHTTP] would have counted for
			// itself is counted here instead. The two must not both do it:
			// the deadline can fire between them, and then the cause ServeHTTP
			// reads is set where this had already answered for another reason.
			if sw, ok := w.(*streamWriter); ok {
				sw.failed = true
			}
			// The proxy's own deadline, which is a failure of the upstream
			// rather than of the client waiting for it.
			if errors.Is(context.Cause(r.Context()), errUpstreamTimeout) {
				p.counters.upstream.Add(1)
				p.log.Error().Err(err).Msg("forwarding upstream")
				writeError(w, http.StatusGatewayTimeout,
					"upstream unavailable", "UPSTREAM_UNAVAILABLE")
				return
			}
			if r.Context().Err() != nil {
				// The client hung up,
				// so nothing failed upstream and there is nobody left to answer.
				// Without this any client that gives up fills the log at error level.
				if p.debug {
					p.log.Debug().Err(err).Msg("the client left before the answer")
				}
				return
			}
			p.counters.upstream.Add(1)
			code := http.StatusBadGateway
			if errors.Is(err, context.DeadlineExceeded) {
				code = http.StatusGatewayTimeout
			}
			p.log.Error().Err(err).Msg("forwarding upstream")
			writeError(w, code, "upstream unavailable", "UPSTREAM_UNAVAILABLE")
		},
	}
	return p
}

// snapshot returns the decisions the proxy made.
func (p *proxy) snapshot() decisions { return p.counters.snapshot() }

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	st := p.states.Get().(*state)
	defer func() {
		st.release()
		p.states.Put(st)
	}()

	allowed, err := p.check(st, r)
	switch {
	case errors.Is(err, errTooLarge):
		p.counters.tooLarge.Add(1)
		p.reject(w, http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
		p.metrics.Observe(decisionTooLarge, start)
		return
	case errors.Is(err, errTooDeep):
		p.counters.tooDeep.Add(1)
		p.reject(w, http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
		p.metrics.Observe(decisionTooDeep, start)
		return
	case isAmbiguous(err):
		p.counters.ambiguous.Add(1)
		if p.debug {
			p.log.Debug().Err(err).Msg("rejecting an ambiguous request")
		}
		p.reject(w, http.StatusBadRequest, err.Error(), "BAD_REQUEST")
		p.metrics.Observe(decisionAmbiguous, start)
		return
	case err != nil:
		p.counters.malformed.Add(1)
		if p.debug {
			p.log.Debug().Err(err).Msg("rejecting a malformed request")
		}
		p.reject(w, http.StatusBadRequest, err.Error(), "BAD_REQUEST")
		p.metrics.Observe(decisionMalformed, start)
		return
	case !allowed:
		p.counters.rejected.Add(1)
		// Why debug and behind a condition: a rejection is the path a flood takes,
		// so one event each is log volume the caller controls.
		// [counters] carry the totals.
		if p.debug {
			p.log.Debug().
				Str("remote", r.RemoteAddr).
				Str("method", r.Method).
				Msg("the document is not on the allowlist")
		}
		p.reject(w, http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
		p.metrics.Observe(decisionRejected, start)
		return
	}

	p.counters.allowed.Add(1)
	if p.logRequests {
		p.log.Debug().Str("remote", r.RemoteAddr).Msg("forwarding")
	}

	// The body was read to find the document, so the same bytes go upstream.
	if r.Method != http.MethodGet {
		r.Body = io.NopCloser(bytes.NewReader(st.body))
		r.ContentLength = int64(len(st.body))
	} else {
		// A GET's body was read only to see whether it had one, and one that
		// had is refused before this. So there is nothing to send on,
		// and the framing it was declared under goes with it:
		// the API receives a GET carrying no body,
		// as it does under the fasthttp underlay.
		r.Body, r.ContentLength, r.TransferEncoding = http.NoBody, 0, nil
	}
	// -upstream.timeout bounds the exchange, not the wait for its first byte:
	// an API that answers headers and then stops is the case the flag exists
	// for. The deadline is released once the answer is written.
	var deadline *time.Timer
	var cancelForward context.CancelCauseFunc
	if p.upstreamTimeout > 0 {
		// A cause, so the error handler can tell this deadline from a client
		// that went away: both cancel the request, and only one of them is a
		// failure of the upstream.
		//
		// A timer rather than [context.WithTimeoutCause] because a deadline
		// that can't be called off can't be released for an event stream,
		// which [streamWriter] does once the answer names itself one.
		ctx, cancel := context.WithCancelCause(r.Context())
		defer cancel(nil)
		cancelForward = cancel
		deadline = time.AfterFunc(p.upstreamTimeout, func() {
			cancel(errUpstreamTimeout)
		})
		defer deadline.Stop()
		r = r.WithContext(ctx)
	}
	st.writer.reset(w, p, start, deadline,
		AcceptsEventStream(r.Header.Get("Accept")), cancelForward)
	// In a defer, because an answer this can't finish doesn't return through
	// here: ReverseProxy abandons a copy that fails mid-body by panicking with
	// [http.ErrAbortHandler], which net/http recovers. An upstream that sent its
	// headers and then stopped took that path, so the request it belongs to was
	// counted and never timed, and the deadline that ended it never counted.
	defer func() {
		if st.writer.stopDraining != nil {
			st.writer.stopDraining()
		}
		// The deadline is counted here rather than in the error handler,
		// which no longer runs once a status is on the wire — ReverseProxy can't take
		// one back. The cause is set exactly once, so this counts once,
		// and a client that hung up sets a different one and is no upstream failure.
		// Only where the error handler never ran, which is what an answer
		// already on the wire means: ReverseProxy can't take a status back,
		// so it abandons the copy instead, and nothing else would count the
		// deadline that ended it. A client that hung up sets another cause and
		// is no upstream failure.
		if !st.writer.failed &&
			errors.Is(context.Cause(r.Context()), errUpstreamTimeout) {
			p.counters.upstream.Add(1)
		}
		// A stream was timed when its headers were written,
		// since its length is the client's business rather than the proxy's latency.
		if !st.writer.streamed {
			// The duration includes the upstream answer,
			// so a dashboard can tell the proxy apart from the API behind it.
			p.metrics.Observe(decisionAllowed, start)
		}
	}()
	p.upstream.ServeHTTP(&st.writer, r)
}

// release drops what one oversized request grew, so the pool doesn't hold it for
// the life of the process. What the common request grew is kept:
// releasing that would cost an allocation per request to save nothing.
func (st *state) release() {
	if cap(st.body) > maxRetainedBuffer {
		st.body = make([]byte, 0, defaultBodyBuffer)
	}
	if cap(st.scratch) > maxRetainedBuffer {
		st.scratch = make([]byte, 0, defaultScratchBuffer)
	}
	if cap(st.spans) > maxRetainedSpans {
		st.spans = make([]span, 0, defaultSpans)
	}
	// The connection of the request that just ended, which the pool has no
	// business keeping alive until the state is taken again.
	st.writer = streamWriter{}
}

var errTooLarge = errors.New("request body too large")

// errTooDeep is a document nesting past the depth limit,
// which is a rejection with a reason of its own.
var errTooDeep = errors.New("operation not allowed")

// isAmbiguous reports whether err is a request naming its document more than once,
// which is answered like a malformed one and counted apart from it.
func isAmbiguous(err error) bool {
	return errors.Is(err, errQueryCollision) ||
		errors.Is(err, errDuplicateQuery) ||
		errors.Is(err, errBodyOnGET) ||
		errors.Is(err, errQueryBesideBody)
}

// errUpstreamTimeout is what cancels a forward that outlived -upstream.timeout,
// as the cause of its context.
var errUpstreamTimeout = errors.New("upstream timeout")

// errUpstreamSwitchedProtocols is an API answering 101 to a forward that
// offered no upgrade, which reaches the client as an upstream failure rather
// than as a tunnel.
var errUpstreamSwitchedProtocols = errors.New(
	"the upstream switched protocols; nothing offered an upgrade")

// check reports whether every document of the request is on the allowlist.
//
// It allocates nothing: the body lands in the buffer of st, the document is a
// subslice of that buffer, and the allowlist is looked up by that subslice.
func (p *proxy) check(st *state, r *http.Request) (allowed bool, err error) {
	if r.Method == http.MethodGet {
		// A body is bytes, not a framing: `Content-Length: 0` and an empty
		// chunked body both name no document, so neither is a second place one
		// could be. A length of 0 is net/http for "no bytes under either framing",
		// so only an unknown or non-zero length is read — and reading
		// it is also what applies -server.max-body to a GET, which makes an
		// oversized body too_large before it is anything else.
		st.body = st.body[:0]
		if r.ContentLength != 0 {
			if err = p.readBody(st, r); err != nil {
				return false, err
			}
		}
		return p.decide(st, Request{
			IsGET: true, RawQuery: r.URL.RawQuery, HasBody: len(st.body) > 0,
		})
	}
	if err = p.readBody(st, r); err != nil {
		return false, err
	}
	return p.decide(st, Request{
		RawQuery:       r.URL.RawQuery,
		BodyIsDocument: isGraphQLContentType(r.Header.Get("Content-Type")),
		Body:           st.body,
	})
}

// decide is the whole decision and the only copy of it. It takes what a request
// carries rather than a request, so that every underlay reaches the same answer:
// one reads the body itself, another is handed the bytes it already has.
//
// req.Body is read and not kept, so an underlay owning buffers of its own can pass them.
// It allocates nothing: the document is a subslice of that body and
// the allowlist is looked up by that subslice.
func (p *proxy) decide(st *state, req Request) (allowed bool, err error) {
	var value []byte

	if req.IsGET {
		// A GET carries its document in the query string, so a body on one is a
		// second place it could be, and which of them an API reads is the API's
		// business. It's the question a duplicate query member asks,
		// and it's answered the same way: the request names the document once.
		if req.HasBody {
			return false, errBodyOnGET
		}
		value, st.scratch, err = extractQueryParam(st.scratch, req.RawQuery)
		if err != nil {
			return false, err
		}
		return p.allow(st, value)
	}

	// The document is the body from here on, so a query parameter is a second
	// place it could be: an API reading one for a POST would run what this never hashed.
	// It's the question a GET carrying a body asks, answered the same way.
	if hasQueryParam(req.RawQuery) {
		return false, errQueryBesideBody
	}

	if req.BodyIsDocument {
		return p.allow(st, req.Body)
	}

	docs, err := extractJSON(st.spans[:0], req.Body, p.allowBatch)
	// The spans of st are kept whatever happened, so the room extractJSON grew
	// for a batch is there for the next request. docs holds the same array and
	// keeps its length, which is what the loop below reads.
	st.spans = docs[:0]
	if err != nil {
		return false, err
	}
	for _, s := range docs {
		value, st.scratch, err = unescapeJSON(st.scratch, req.Body[s.start:s.end])
		if err != nil {
			return false, err
		}
		if allowed, err := p.allow(st, value); err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}

// allow reports whether document is on the allowlist.
// A document that doesn't parse isn't.
func (p *proxy) allow(st *state, document []byte) (allowed bool, err error) {
	if p.allowlist.Len() == 0 {
		return false, nil
	}

	st.hash.Reset()
	if e := st.parser.Parse(st.hash, p.options, document); e.IsErr() {
		// A document nesting past the limit is told apart from one that isn't
		// on the list: a flood of them is an attack on what hashing costs,
		// where a document nobody allowed is usually an allowlist out of date.
		if errors.Is(e.Err, parser.ErrTooDeep) {
			return false, errTooDeep
		}
		return false, nil
	}
	key := st.hash.Sum(st.sum[:0])
	st.sum = key
	return p.allowlist.Allowed(key), nil
}

func (p *proxy) readBody(st *state, r *http.Request) error {
	st.body = st.body[:0]
	if r.ContentLength > p.maxBody {
		return errTooLarge
	}
	if r.ContentLength > 0 {
		// The length is known, so one read fills one buffer.
		if int64(cap(st.body)) < r.ContentLength {
			st.body = make([]byte, 0, r.ContentLength)
		}
		st.body = st.body[:r.ContentLength]
		if _, err := io.ReadFull(r.Body, st.body); err != nil {
			// A body that doesn't arrive is no malformed document: the client
			// went away, a timeout cut it off, or the connection failed.
			// Saying which is what lets the log tell them apart, and calling it JSON
			// would be wrong anyway for an application/graphql body.
			return fmt.Errorf("reading the request body: %w", err)
		}
		return nil
	}

	// No length, so read until EOF, one buffer growth at a time.
	limit := p.maxBody + 1
	for {
		if len(st.body) == cap(st.body) {
			st.body = append(st.body, 0)[:len(st.body)]
		}
		n, err := r.Body.Read(st.body[len(st.body):cap(st.body)])
		st.body = st.body[:len(st.body)+n]
		if int64(len(st.body)) >= limit {
			return errTooLarge
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("reading the request body: %w", err)
		}
	}
}

// reject answers with a GraphQL error body. Under -opaque-errors every rejection is
// a 403 without detail, so a caller learns nothing about why.
func (p *proxy) reject(w http.ResponseWriter, code int, message, extension string) {
	code, message, extension = p.rejection(code, message, extension)
	writeError(w, code, message, extension)
}

// rejection is what a rejection says after -opaque-errors has had its say.
// It's the one copy of that rule: an underlay answering a rejection asks here first,
// so none of them can leak a detail this is meant to withhold.
func (p *proxy) rejection(
	code int, message, extension string,
) (int, string, string) {
	if p.opaqueErrors {
		return http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED"
	}
	return code, message, extension
}

// writeError answers with the GraphQL error shape, which is what a client
// library expects in a body.
//
// The envelope is written as it is rather than marshalled, which is what keeps
// a rejection free of allocations, so the parts that come from a variable are
// escaped on the way out. Nothing today puts a quote in a message,
// and the client parsing this body is what pays if anything ever does.
func writeError(w http.ResponseWriter, code int, message, extension string) {
	// Header.Set would build a one-element slice per rejection, which is the
	// path a flood takes. The shared one is only ever read: Add on a full slice
	// appends into a new one, and Del drops the entry rather than the value.
	// The key has to be in canonical form to be assigned like this.
	w.Header()["Content-Type"] = contentTypeJSON
	w.WriteHeader(code)
	writeErrorBody(w, message, extension)
}

// writeErrorBody writes the envelope alone. The status and the content type are
// the underlay's to set, the shape of the answer is the same under all of them.
func writeErrorBody(w io.Writer, message, extension string) {
	_, _ = io.WriteString(w, `{"errors":[{"message":"`)
	writeJSONString(w, message)
	// The code is a constant of this package and never comes from a request,
	// so it goes out as it is. Only the message can carry anything.
	_, _ = io.WriteString(w, `","extensions":{"code":"`)
	_, _ = io.WriteString(w, extension)
	_, _ = io.WriteString(w, `"}}]}`)
}

// contentTypeJSON is the header value every error answer carries,
// shared so that writing one doesn't allocate the slice that holds it.
var contentTypeJSON = []string{"application/json; charset=utf-8"}

// jsonEscape is the escape for every byte a JSON string can't carry as it is.
// A quote and a backslash are handled apart, being the only two above 0x1F.
var jsonEscape = func() (table [0x20]string) {
	const hex = "0123456789abcdef"
	for i := range table {
		table[i] = `\u00` + string([]byte{hex[i>>4], hex[i&0xf]})
	}
	table['\b'], table['\f'] = `\b`, `\f`
	table['\n'], table['\r'], table['\t'] = `\n`, `\r`, `\t`
	return table
}()

// writeJSONString writes s as the contents of a JSON string, without the quotes
// around it. The run between two escapes is written whole, so a string needing
// no escape costs one write and nothing else.
//
// Bytes above 0x7F go out as they are, which is a JSON string where s is valid
// UTF-8. Every message here is a Go error or a constant of this package, so it is.
func writeJSONString(w io.Writer, s string) {
	start := 0
	for i := range len(s) {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		if start < i {
			_, _ = io.WriteString(w, s[start:i])
		}
		switch c {
		case '"':
			_, _ = io.WriteString(w, `\"`)
		case '\\':
			_, _ = io.WriteString(w, `\\`)
		default:
			_, _ = io.WriteString(w, jsonEscape[c])
		}
		start = i + 1
	}
	if start < len(s) {
		_, _ = io.WriteString(w, s[start:])
	}
}

// setForwarded fills the forwarding headers of the outbound request.
//
// Without trust they report the direct peer, because a client that reaches
// the proxy directly can claim any address. With trust the peer is appended
// to the chain of the load balancer and its host and protocol are kept,
// so the upstream API sees the original client.
// Reference:
//
//   - https://datatracker.ietf.org/doc/html/rfc7239
func setForwarded(r *httputil.ProxyRequest, trust bool) {
	if !trust {
		r.SetXForwarded()
		// An RFC 7239 header of the client would reach upstream unchecked.
		r.Out.Header.Del("Forwarded")
		return
	}

	chain := r.In.Header.Get("X-Forwarded-For")
	host := r.In.Header.Get("X-Forwarded-Host")
	proto := r.In.Header.Get("X-Forwarded-Proto")

	r.SetXForwarded()

	if chain != "" {
		r.Out.Header.Set("X-Forwarded-For",
			chain+", "+r.Out.Header.Get("X-Forwarded-For"))
	}
	if host != "" {
		r.Out.Header.Set("X-Forwarded-Host", host)
	}
	if proto != "" {
		r.Out.Header.Set("X-Forwarded-Proto", proto)
	}
}

func isGraphQLContentType(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/graphql")
}
