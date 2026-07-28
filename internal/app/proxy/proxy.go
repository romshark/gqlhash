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

// counters of what the proxy decided.
type counters struct {
	allowed   atomic.Uint64
	rejected  atomic.Uint64
	malformed atomic.Uint64
	upstream  atomic.Uint64
}

// decisions is what a proxy did, read at one moment. It's a struct and not four
// returns so that a caller names what it takes: the four are the same type,
// and nothing catches a transposition at the call or a reorder of the counters.
type decisions struct {
	allowed   uint64
	rejected  uint64
	malformed uint64
	upstream  uint64
}

// snapshot returns what the proxy decided.
func (c *counters) snapshot() decisions {
	return decisions{
		allowed:   c.allowed.Load(),
		rejected:  c.rejected.Load(),
		malformed: c.malformed.Load(),
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

	options        gqlhash.Options
	maxBody        int64
	allowBatch     bool
	opaqueErrors   bool
	logRequests    bool
	trustForwarded bool

	// debug says whether the logger keeps a debug event.
	// The level is set once at startup, so it's read once instead of per event.
	debug bool

	// metrics are always kept. The control server is what exposes them, and a
	// run always has one, so a request costs the clock read and the observation
	// either way and the hot path has nothing to decide.
	metrics *metrics

	newHash func() hash.Hash
	states  sync.Pool
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
		allowBatch: config.allowBatch, opaqueErrors: config.opaqueErrors,
		trustForwarded: config.trustForwarded,
		debug:          log.GetLevel() <= zerolog.DebugLevel,
		newHash:        newHash,
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
		Rewrite: func(r *httputil.ProxyRequest) {
			// The upstream URL is the GraphQL endpoint,
			// so it replaces the path instead of prefixing it. SetURL would join the
			// two and turn a request to /graphql into /graphql/graphql.
			r.Out.URL.Scheme = upstream.Scheme
			r.Out.URL.Host = upstream.Host
			r.Out.URL.Path, r.Out.URL.RawPath = upstream.Path, upstream.RawPath
			// An empty Host makes the request carry the host of the upstream URL.
			r.Out.Host = ""
			setForwarded(r, p.trustForwarded)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
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
		p.counters.malformed.Add(1)
		p.reject(w, http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
		p.metrics.Observe(decisionMalformed, start)
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
	}
	p.upstream.ServeHTTP(w, r)

	// The duration includes the upstream answer,
	// so a dashboard can tell the proxy apart from the API behind it.
	p.metrics.Observe(decisionAllowed, start)
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
}

var errTooLarge = errors.New("request body too large")

// check reports whether every document of the request is on the allowlist.
//
// It allocates nothing: the body lands in the buffer of st, the document is a
// subslice of that buffer, and the allowlist is looked up by that subslice.
func (p *proxy) check(st *state, r *http.Request) (allowed bool, err error) {
	var value []byte

	if r.Method == http.MethodGet {
		value, st.scratch, err = extractQueryParam(st.scratch, r.URL.RawQuery)
		if err != nil {
			return false, err
		}
		return p.allow(st, value), nil
	}

	if err = p.readBody(st, r); err != nil {
		return false, err
	}

	if isGraphQLContentType(r.Header.Get("Content-Type")) {
		// The body is the document.
		return p.allow(st, st.body), nil
	}

	docs, err := extractJSON(st.spans[:0], st.body, p.allowBatch)
	// The spans of st are kept whatever happened, so the room extractJSON grew
	// for a batch is there for the next request. docs holds the same array and
	// keeps its length, which is what the loop below reads.
	st.spans = docs[:0]
	if err != nil {
		return false, err
	}
	for _, s := range docs {
		value, st.scratch, err = unescapeJSON(st.scratch, st.body[s.start:s.end])
		if err != nil {
			return false, err
		}
		if !p.allow(st, value) {
			return false, nil
		}
	}
	return true, nil
}

// allow reports whether document is on the allowlist.
// A document that doesn't parse isn't.
func (p *proxy) allow(st *state, document []byte) bool {
	if p.allowlist.Len() == 0 {
		return false
	}

	st.hash.Reset()
	if e := st.parser.Parse(st.hash, p.options, document); e.IsErr() {
		return false
	}
	key := st.hash.Sum(st.sum[:0])
	st.sum = key
	return p.allowlist.Allowed(key)
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
	if p.opaqueErrors {
		code, message, extension = http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED"
	}
	writeError(w, code, message, extension)
}

// writeError answers with the GraphQL error shape, which is what a client
// library expects in a body.
//
// The envelope is written as it is rather than marshalled, which is what keeps
// a rejection free of allocations, so the parts that come from a variable are
// escaped on the way out. Nothing today puts a quote in a message,
// and the client parsing this body is what pays if anything ever does.
func writeError(w http.ResponseWriter, code int, message, extension string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `{"errors":[{"message":"`)
	writeJSONString(w, message)
	// The code is a constant of this package and never comes from a request,
	// so it goes out as it is. Only the message can carry anything.
	_, _ = io.WriteString(w, `","extensions":{"code":"`)
	_, _ = io.WriteString(w, extension)
	_, _ = io.WriteString(w, `"}}]}`)
}

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
