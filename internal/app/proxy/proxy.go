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

// upstreamCopyBuffer is what one answer is copied through. A bigger answer takes
// more turns around the loop, cheaper than 32KiB per request in flight.
const upstreamCopyBuffer = 8 * 1024

// bufferPool keeps [httputil.ReverseProxy] from allocating a copy buffer per request.
type bufferPool struct{ pool sync.Pool }

func (b *bufferPool) Get() []byte  { return b.pool.Get().([]byte) }
func (b *bufferPool) Put(p []byte) { b.pool.Put(p) } //nolint:staticcheck // the interface takes a slice

// counters of what the proxy decided, one per cache line. Packed, two cores
// raising different counters bounce one line between them: ~10.7ns against
// ~8.6ns per raise. A flood of one decision contends whatever the layout.
type counters struct {
	allowed   paddedCounter
	rejected  paddedCounter
	malformed paddedCounter
	tooLarge  paddedCounter
	ambiguous paddedCounter
	tooDeep   paddedCounter
	upstream  paddedCounter
}

// paddedCounter is a counter on a cache line of its own.
type paddedCounter struct {
	atomic.Uint64
	_ [56]byte
}

// decisions is what a proxy did, read at one moment. A struct rather than seven
// returns of one type, where nothing catches a transposition at the call.
type decisions struct {
	allowed   uint64
	rejected  uint64
	malformed uint64
	tooLarge  uint64
	ambiguous uint64
	tooDeep   uint64
	upstream  uint64
}

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

	// debug is the level, read once at startup instead of per event.
	debug bool

	// metrics are always kept: every run has a control server exposing them,
	// so the hot path pays the clock read either way and has nothing to decide.
	metrics *metrics

	newHash   func() hash.Hash
	states    sync.Pool
	drainOnce sync.Once

	// draining is closed when the shutdown starts. Streams watch it and end;
	// everything else is waited for. A drain waits for what's in flight and a
	// subscription never ends on its own, so without this a proxy carrying one
	// sits out the whole -server.shutdown-timeout and exits 1.
	draining chan struct{}
}

// Draining is closed when the shutdown starts, for an underlay in another
// package to end its streams on. See [proxy.draining].
func (c *Core) Draining() <-chan struct{} { return c.p.draining }

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
	// Without it a burst of -server.max-body-sized requests leaves the pool
	// holding a megabyte per concurrent request for the life of the process.
	// Same policy as parser.maxRetainedBufferSize.
	maxRetainedBuffer = 64 << 10

	// maxRetainedSpans is the same bound for a batch:
	// a megabyte of tiny documents is tens of thousands of spans.
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

	// writer relays the answer, releasing the exchange bounds where that answer
	// turns out to be an event stream. Inline, so forwarding allocates nothing.
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

	// TrustForwarded appends to the X-Forwarded-* headers of the incoming
	// request instead of replacing them with the direct peer.
	//
	// Requirement: only behind a trusted load balancer.
	// A client reaching the proxy directly can claim any address.
	trustForwarded bool

	// MaxBody is the largest request body to accept, in bytes.
	maxBody int64

	// upstreamTimeout is -upstream.timeout, which bounds a forward whole.
	upstreamTimeout time.Duration
}

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
	// Built here rather than handed in: the metrics read this proxy's counters,
	// so a caller would need each of the two before the other.
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
		// Without a pool ReverseProxy allocates 32KiB per answer,
		// most of what forwarding costs when the answers are small.
		BufferPool: &bufferPool{pool: sync.Pool{
			New: func() any { return make([]byte, upstreamCopyBuffer) },
		}},
		Rewrite: func(r *httputil.ProxyRequest) {
			// The upstream URL is the GraphQL endpoint, so its path replaces the
			// request's. SetURL joins them: /graphql becomes /graphql/graphql.
			r.Out.URL.Scheme = upstream.Scheme
			r.Out.URL.Host = upstream.Host
			r.Out.URL.Path, r.Out.URL.RawPath = upstream.Path, upstream.RawPath
			// The client's query string, verbatim. ReverseProxy has already
			// dropped what net/url can't parse, which for a `;`-separated query
			// is all of it — an allowed GET reaching the API with no document.
			// Safe to restore: the reading rule splits on `;` too, so a document
			// named twice either way is refused rather than forwarded.
			r.Out.URL.RawQuery = r.In.URL.RawQuery
			// An empty Host makes the request carry the upstream URL's.
			r.Out.Host = ""
			// A protocol upgrade stops here. ReverseProxy strips these with the
			// other hop-by-hop headers and puts them back for an upgrade,
			// which is one allowlisted document buying a tunnel: the API answers 101
			// and everything written afterwards bypasses the allowlist.
			// This runs after that, so the 101 then has nothing to match.
			r.Out.Header.Del("Upgrade")
			r.Out.Header.Del("Connection")
			// The expectation was the proxy's to meet and it met it: the body is
			// read and hashed, and a 100 sent if one was asked for. Forwarding it
			// makes the API run the handshake again for a body already in flight,
			// and ReverseProxy relays the answer's 1xx — two continuations.
			r.Out.Header.Del("Expect")
			setForwarded(r, p.trustForwarded)
		},
		ModifyResponse: func(res *http.Response) error {
			// Nothing offered an upgrade, so a 101 answers a question nobody asked.
			// Relaying it would splice the two connections together.
			if res.StatusCode == http.StatusSwitchingProtocols {
				return errUpstreamSwitchedProtocols
			}
			return nil
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// Reached, so the upstream failure is counted here and not by
			// [proxy.ServeHTTP]. Never both: the deadline can fire between them,
			// leaving ServeHTTP to read a cause this already answered for.
			if sw, ok := w.(*streamWriter); ok {
				sw.failed = true
			}
			// The proxy's own deadline: a failure of the upstream rather than
			// of the client waiting for it.
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
		// Debug and behind a condition: a rejection is the path a flood takes,
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
		// A GET's body was read only to see whether it had one,
		// and one that had is refused before this. Nothing to send on,
		// and the framing it was declared under goes with it,
		// as under the fasthttp underlay.
		r.Body, r.ContentLength, r.TransferEncoding = http.NoBody, 0, nil
	}
	// -upstream.timeout bounds the exchange, not the wait for its first byte:
	// an API that answers headers and then stops is what the flag exists for.
	var deadline *time.Timer
	var cancelForward context.CancelCauseFunc
	if p.upstreamTimeout > 0 {
		// A cause, so the error handler can tell this deadline from a client
		// that went away: both cancel the request, only one is an upstream failure.
		// A timer rather than [context.WithTimeoutCause] because
		// [streamWriter] releases it once the answer names itself a stream.
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
	// Deferred, because an answer this can't finish doesn't return through here:
	// ReverseProxy abandons a copy that fails mid-body by panicking with
	// [http.ErrAbortHandler], which net/http recovers.
	// An upstream that sends headers and then stops takes that path.
	defer func() {
		if st.writer.stopDraining != nil {
			st.writer.stopDraining()
		}
		// Counted here rather than in the error handler, which no longer runs
		// once a status is on the wire. The cause is set exactly once,
		// and a client that hung up sets a different one.
		if !st.writer.failed &&
			errors.Is(context.Cause(r.Context()), errUpstreamTimeout) {
			p.counters.upstream.Add(1)
		}
		// A stream was timed at its headers: its length is the client's business
		// rather than the proxy's latency.
		if !st.writer.streamed {
			// Includes the upstream answer, so a dashboard can tell the proxy
			// apart from the API behind it.
			p.metrics.Observe(decisionAllowed, start)
		}
	}()
	p.upstream.ServeHTTP(&st.writer, r)
}

// release drops what one oversized request grew, so the pool doesn't hold it
// for the life of the process. What the common request grew is kept:
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
	// Drops the finished request's connection, which the pool has no business
	// holding until the state is taken again.
	st.writer = streamWriter{}
}

var errTooLarge = errors.New("request body too large")

// errTooDeep is a document nesting past the depth limit,
// which is a rejection with a reason of its own.
var errTooDeep = errors.New("operation not allowed")

// isAmbiguous reports whether err is a request naming its document more than once,
// answered like a malformed one and counted apart from it.
func isAmbiguous(err error) bool {
	return errors.Is(err, errQueryCollision) ||
		errors.Is(err, errDuplicateQuery) ||
		errors.Is(err, errBodyOnGET) ||
		errors.Is(err, errQueryBesideBody)
}

// errUpstreamTimeout is the context cause of a forward past -upstream.timeout.
var errUpstreamTimeout = errors.New("upstream timeout")

// errUpstreamSwitchedProtocols is an API answering 101 to a forward that
// offered no upgrade. It reaches the client as an upstream failure, not a tunnel.
var errUpstreamSwitchedProtocols = errors.New(
	"the upstream switched protocols; nothing offered an upgrade")

// check reports whether every document of the request is on the allowlist.
//
// It allocates nothing: the body lands in the buffer of st, the document is a
// subslice of that buffer, and the allowlist is looked up by that subslice.
func (p *proxy) check(st *state, r *http.Request) (allowed bool, err error) {
	if r.Method == http.MethodGet {
		// A body is bytes, not a framing: `Content-Length: 0` and an empty
		// chunked body both name no document. A length of 0 is net/http for
		// "no bytes under either framing", so only an unknown or non-zero one
		// is read — which is also what applies -server.max-body to a GET.
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
// carries rather than a request, so every underlay reaches the same answer.
//
// req.Body is read and not kept, so an underlay can pass its own buffers.
// Allocates nothing: the document is a subslice of that body,
// and the allowlist is looked up by that subslice.
func (p *proxy) decide(st *state, req Request) (allowed bool, err error) {
	var value []byte

	if req.IsGET {
		// A GET carries its document in the query string, so a body on one is a
		// second place it could be, and which an API reads is the API's
		// business. Same question a duplicate query member asks, same answer:
		// the request names the document once.
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
	// place it could be: an API reading one for a POST runs what this never
	// hashed. Same question a GET carrying a body asks, same answer.
	if hasQueryParam(req.RawQuery) {
		return false, errQueryBesideBody
	}

	if req.BodyIsDocument {
		return p.allow(st, req.Body)
	}

	docs, err := extractJSON(st.spans[:0], req.Body, p.allowBatch)
	// Kept whatever happened, so the room extractJSON grew for a batch is there
	// for the next request. docs shares the array and keeps its length.
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
		// Nesting past the limit is told apart from not being on the list:
		// a flood of the first is an attack on what hashing costs,
		// where the second is usually an allowlist out of date.
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
			// Calling it JSON is wrong for an application/graphql body anyway.
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

// reject answers with a GraphQL error body. Under -opaque-errors that's a 403
// without detail, so a caller learns nothing about why.
func (p *proxy) reject(w http.ResponseWriter, code int, message, extension string) {
	code, message, extension = p.rejection(code, message, extension)
	writeError(w, code, message, extension)
}

// rejection applies -opaque-errors. The one copy of that rule:
// every underlay asks here, so none can leak a detail this is meant to withhold.
func (p *proxy) rejection(
	code int, message, extension string,
) (int, string, string) {
	if p.opaqueErrors {
		return http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED"
	}
	return code, message, extension
}

// writeError answers with the GraphQL error shape a client library expects.
//
// The envelope is written rather than marshalled, which keeps a rejection free
// of allocations, so the variable parts are escaped on the way out.
func writeError(w http.ResponseWriter, code int, message, extension string) {
	// Header.Set would build a one-element slice per rejection,
	// the path a flood takes. The shared one is only ever read:
	// Add on a full slice appends into a new one, Del drops the entry.
	// The key must be canonical to be assigned like this.
	w.Header()["Content-Type"] = contentTypeJSON
	w.WriteHeader(code)
	writeErrorBody(w, message, extension)
}

// writeErrorBody writes the envelope alone. The status and the content type are
// the underlay's to set; the shape is the same under all of them.
func writeErrorBody(w io.Writer, message, extension string) {
	_, _ = io.WriteString(w, `{"errors":[{"message":"`)
	writeJSONString(w, message)
	// Unescaped: the code is a constant of this package and never comes from a
	// request. Only the message can carry anything.
	_, _ = io.WriteString(w, `","extensions":{"code":"`)
	_, _ = io.WriteString(w, extension)
	_, _ = io.WriteString(w, `"}}]}`)
}

// contentTypeJSON is shared, so writing an error answer allocates no slice.
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

// writeJSONString writes s as JSON string contents, without the quotes.
// The run between two escapes is written whole, so a string needing none costs one write.
//
// Bytes above 0x7F go out as they are, valid JSON where s is valid UTF-8.
// Every message here is a Go error or a constant of this package, so it is.
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
// Without trust they report the direct peer, since a client reaching the proxy
// directly can claim any address. With trust the peer is appended to the load
// balancer's chain and its host and protocol are kept,
// so the API sees the original client.
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
