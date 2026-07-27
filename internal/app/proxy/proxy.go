package proxy

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/romshark/gqlhash/v2/parser"
)

// Counters of what the proxy decided.
type Counters struct {
	Allowed   atomic.Uint64
	Rejected  atomic.Uint64
	Malformed atomic.Uint64
	Upstream  atomic.Uint64
}

// Proxy checks the document of a request against an allowlist and forwards it
// upstream or rejects it.
type Proxy struct {
	store    *Store
	upstream *httputil.ReverseProxy
	log      zerolog.Logger
	counters Counters

	options        gqlhash.Options
	exact          bool
	maxBody        int64
	allowBatch     bool
	opaqueErrors   bool
	logRequests    bool
	trustForwarded bool

	// debug says whether the logger keeps a debug event. The level is set once at
	// startup, so it's read once instead of per event.
	debug bool

	// metrics is nil unless -metrics is set. Timing a request costs a clock read
	// and an observation, so the hot path checks this first.
	metrics *Metrics

	newHash func() hash.Hash
	states  sync.Pool
}

// state is everything one request needs. It's pooled, so a request that fits
// allocates nothing on the checking path.
type state struct {
	// body holds the request body. The document is a subslice of it.
	body []byte

	// scratch takes the document only when it needs unescaping or decoding.
	scratch []byte

	// canon takes the canonical form under -exact.
	canon appender

	sum []byte

	spans  []span
	hash   hash.Hash
	parser *parser.Parser[[]byte]
}

// ProxyConfig configures a [Proxy]. Its zero value hashes whole documents,
// rejects batches, reports why it rejected and trusts no forwarding header.
type ProxyConfig struct {
	// Options is what the canonical form leaves out.
	Options gqlhash.Options

	// Exact compares canonical forms instead of hashes, which cannot collide.
	Exact bool

	// AllowBatch accepts a JSON array of requests. Every document must be allowed.
	AllowBatch bool

	// OpaqueErrors answers every rejection with 403 and no detail.
	OpaqueErrors bool

	// LogRequests logs a forwarded request at debug level.
	LogRequests bool

	// TrustForwarded keeps the X-Forwarded-* headers of the incoming request and
	// appends to them, instead of replacing them with the direct peer.
	//
	// Requirement: only set this where a trusted load balancer is in front. A
	// client that reaches the proxy directly can claim any address.
	TrustForwarded bool

	// MaxBody is the largest request body to accept, in bytes.
	MaxBody int64
}

// NewProxy returns a proxy forwarding to upstream.
func NewProxy(
	store *Store,
	upstream *url.URL,
	newHash func() hash.Hash,
	config ProxyConfig,
	transport http.RoundTripper,
	log zerolog.Logger,
) *Proxy {
	p := &Proxy{
		store:   store,
		log:     log,
		options: config.Options, exact: config.Exact, maxBody: config.MaxBody,
		allowBatch: config.AllowBatch, opaqueErrors: config.OpaqueErrors,
		trustForwarded: config.TrustForwarded,
		debug:          log.GetLevel() <= zerolog.DebugLevel,
		newHash:        newHash,
	}
	p.logRequests = config.LogRequests && p.debug
	p.states.New = func() any {
		return &state{
			body:    make([]byte, 0, 8192),
			scratch: make([]byte, 0, 4096),
			canon:   appender{buf: make([]byte, 0, 4096)},
			sum:     make([]byte, 0, 64),
			spans:   make([]span, 0, 8),
			hash:    newHash(),
			parser:  parser.NewParser[[]byte](0, 0),
		}
	}
	p.upstream = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// The upstream URL is the GraphQL endpoint, so it replaces the path
			// instead of prefixing it. SetURL would join the two and turn a
			// request to /graphql into /graphql/graphql.
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
				// The client hung up, so nothing failed upstream and there is
				// nobody left to answer. Without this any client that gives up
				// fills the log at error level.
				if p.debug {
					p.log.Debug().Err(err).Msg("the client left before the answer")
				}
				return
			}
			p.counters.Upstream.Add(1)
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

// SetMetrics turns metrics on. Call it before the proxy serves.
func (p *Proxy) SetMetrics(m *Metrics) { p.metrics = m }

// CountersSnapshot returns the decisions the proxy made.
func (p *Proxy) CountersSnapshot() (allowed, rejected, malformed, upstream uint64) {
	return p.counters.Allowed.Load(), p.counters.Rejected.Load(),
		p.counters.Malformed.Load(), p.counters.Upstream.Load()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var start time.Time
	if p.metrics != nil {
		start = time.Now()
	}

	st := p.states.Get().(*state)
	defer p.states.Put(st)

	allowed, err := p.check(st, r)
	switch {
	case errors.Is(err, errTooLarge):
		p.counters.Malformed.Add(1)
		p.reject(w, http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
		if p.metrics != nil {
			p.metrics.Observe(decisionMalformed, start)
		}
		return
	case err != nil:
		p.counters.Malformed.Add(1)
		if p.debug {
			p.log.Debug().Err(err).Msg("rejecting a malformed request")
		}
		p.reject(w, http.StatusBadRequest, err.Error(), "BAD_REQUEST")
		if p.metrics != nil {
			p.metrics.Observe(decisionMalformed, start)
		}
		return
	case !allowed:
		p.counters.Rejected.Add(1)
		// Why debug and behind a condition: a rejection is the path a flood
		// takes, so one event each is log volume the caller controls.
		// [Counters] carry the totals.
		if p.debug {
			p.log.Debug().
				Str("remote", r.RemoteAddr).
				Str("method", r.Method).
				Msg("the document is not on the allowlist")
		}
		p.reject(w, http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
		if p.metrics != nil {
			p.metrics.Observe(decisionRejected, start)
		}
		return
	}

	p.counters.Allowed.Add(1)
	if p.logRequests {
		p.log.Debug().Str("remote", r.RemoteAddr).Msg("forwarding")
	}

	// The body was read to find the document, so the same bytes go upstream.
	if r.Method != http.MethodGet {
		r.Body = io.NopCloser(bytes.NewReader(st.body))
		r.ContentLength = int64(len(st.body))
	}
	p.upstream.ServeHTTP(w, r)

	// The duration includes the upstream answer, so a dashboard can tell the
	// proxy apart from the API behind it.
	if p.metrics != nil {
		p.metrics.Observe(decisionAllowed, start)
	}
}

var errTooLarge = errors.New("request body too large")

// check reports whether every document of the request is on the allowlist.
//
// It allocates nothing: the body lands in the buffer of st, the document is a
// subslice of that buffer, and the allowlist is looked up by that subslice.
func (p *Proxy) check(st *state, r *http.Request) (allowed bool, err error) {
	docs := st.spans[:0]

	if r.Method == http.MethodGet {
		value, scratch, err := extractQueryParam(st.scratch, r.URL.RawQuery)
		st.scratch = scratch
		if err != nil {
			return false, err
		}
		return p.allow(st, value), nil
	}

	if err := p.readBody(st, r); err != nil {
		return false, err
	}

	if isGraphQLContentType(r.Header.Get("Content-Type")) {
		// The body is the document.
		return p.allow(st, st.body), nil
	}

	docs, err = extractJSON(docs, st.body, p.allowBatch)
	st.spans = docs[:0]
	if err != nil {
		return false, err
	}
	for _, s := range docs {
		raw := st.body[s.start:s.end]
		value, scratch, err := unescapeJSON(st.scratch, raw)
		st.scratch = scratch
		if err != nil {
			return false, err
		}
		if !p.allow(st, value) {
			return false, nil
		}
	}
	return true, nil
}

// allow reports whether document is on the allowlist. A document that doesn't
// parse isn't.
func (p *Proxy) allow(st *state, document []byte) bool {
	list := p.store.Load()
	if list.Len() == 0 {
		return false
	}

	var key []byte
	if p.exact {
		st.canon.buf = st.canon.buf[:0]
		if e := st.parser.Parse(&st.canon, p.options, document); e.IsErr() {
			return false
		}
		key = st.canon.buf
	} else {
		st.hash.Reset()
		if e := st.parser.Parse(st.hash, p.options, document); e.IsErr() {
			return false
		}
		st.sum = st.hash.Sum(st.sum[:0])
		key = st.sum
	}
	return list.Lookup(key) != nil
}

func (p *Proxy) readBody(st *state, r *http.Request) error {
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
			return ErrMalformedJSON
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
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// reject answers with a GraphQL error body. Under -opaque-errors every rejection
// is a 403 without detail, so a caller learns nothing about why.
func (p *Proxy) reject(w http.ResponseWriter, code int, message, extension string) {
	if p.opaqueErrors {
		code, message, extension = http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED"
	}
	writeError(w, code, message, extension)
}

// writeError answers with the GraphQL error shape, which is what a client
// library expects in a body.
func writeError(w http.ResponseWriter, code int, message, extension string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"errors":[{"message":"`))
	_, _ = io.WriteString(w, message)
	_, _ = w.Write([]byte(`","extensions":{"code":"`))
	_, _ = io.WriteString(w, extension)
	_, _ = w.Write([]byte(`"}}]}`))
}

// setForwarded fills the forwarding headers of the outbound request.
//
// Without trust they report the direct peer, because a client that reaches the
// proxy directly can claim any address. With trust the peer is appended to the
// chain of the load balancer and its host and protocol are kept, so the upstream
// API sees the original client.
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
