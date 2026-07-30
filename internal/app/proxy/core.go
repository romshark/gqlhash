package proxy

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Core is the decision a proxy makes, without the HTTP machinery that carries
// it. An underlay in another package drives the proxy through this: the types
// here name no HTTP implementation, so what a request is answered with can't
// depend on which one served it.
//
// The net/http underlay lives in this package and uses the unexported form
// directly. Core exists for the ones that don't, see
// [github.com/romshark/gqlhash/v2/internal/app/proxyfast].
type Core struct{ p *proxy }

// Core returns the decision of p for an underlay outside this package.
func (p *proxy) Core() *Core { return &Core{p: p} }

// Request is what the decision needs of a request, which is less than an HTTP
// implementation carries. An underlay fills it from whatever types it holds.
type Request struct {
	// IsGET selects the query string over the body as the source of the document.
	IsGET bool

	// RawQuery is the query string, unparsed. A GET reads its document from it;
	// every other method is refused for naming a query parameter at all,
	// so an underlay fills this whatever the method.
	RawQuery string

	// BodyIsDocument is what the content type said: the body is the document
	// itself rather than a JSON request carrying one. The underlay decides it
	// with [IsGraphQLContentType] so that neither has to convert what it holds.
	BodyIsDocument bool

	// Body is the whole request body, already read. It's read and not kept, so
	// an underlay owning the bytes can pass its own.
	Body []byte

	// HasBody is whether a body came with the request at all,
	// which only a GET is asked about: one carrying a body names its document twice.
	HasBody bool
}

// Verdict is what the proxy decided.
type Verdict uint8

const (
	// VerdictAllowed forwards the request upstream.
	VerdictAllowed Verdict = iota
	// VerdictRejected answers alone: the document isn't on the allowlist.
	VerdictRejected
	// VerdictMalformed answers alone: the request carried no document to look up.
	VerdictMalformed
	// VerdictTooLarge answers alone: the request is past -server.max-body,
	// so nothing was read of it. It's a verdict of its own because it says
	// something else than a malformed request does, see decisionTooLarge.
	VerdictTooLarge
	// VerdictAmbiguous answers alone:
	// the request names its document more than once, see decisionAmbiguous.
	VerdictAmbiguous
	// VerdictTooDeep answers alone: the document nests past the depth limit,
	// see decisionTooDeep.
	VerdictTooDeep
)

// Answer is what to write when a request isn't forwarded.
// It has already been through -opaque-errors, so an underlay writes it as it is.
type Answer struct {
	Code               int
	Message, Extension string
}

// Decide reports what to do with req, counts it, and returns the answer to
// write where it isn't forwarded.
//
// The buffers the decision needs are taken from a pool and returned before it
// returns, so nothing of req is retained.
func (c *Core) Decide(req Request) (Verdict, Answer) {
	p := c.p
	st := p.states.Get().(*state)
	defer func() {
		st.release()
		p.states.Put(st)
	}()

	if !req.IsGET && int64(len(req.Body)) > p.maxBody {
		p.counters.tooLarge.Add(1)
		return VerdictTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
	}

	allowed, err := p.decide(st, req)
	switch {
	case errors.Is(err, errTooLarge):
		p.counters.tooLarge.Add(1)
		return VerdictTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
	case errors.Is(err, errTooDeep):
		p.counters.tooDeep.Add(1)
		return VerdictTooDeep, c.answer(http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
	case isAmbiguous(err):
		p.counters.ambiguous.Add(1)
		if p.debug {
			p.log.Debug().Err(err).Msg("rejecting an ambiguous request")
		}
		return VerdictAmbiguous, c.answer(http.StatusBadRequest,
			err.Error(), "BAD_REQUEST")
	case err != nil:
		p.counters.malformed.Add(1)
		if p.debug {
			p.log.Debug().Err(err).Msg("rejecting a malformed request")
		}
		return VerdictMalformed, c.answer(http.StatusBadRequest,
			err.Error(), "BAD_REQUEST")
	case !allowed:
		p.counters.rejected.Add(1)
		return VerdictRejected, c.answer(http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
	}
	p.counters.allowed.Add(1)
	return VerdictAllowed, Answer{Code: http.StatusOK}
}

// answer applies -opaque-errors, which is the one rule every underlay has to
// ask for rather than carry a copy of.
func (c *Core) answer(code int, message, extension string) Answer {
	code, message, extension = c.p.rejection(code, message, extension)
	return Answer{Code: code, Message: message, Extension: extension}
}

// ReadError is the answer for a request an underlay couldn't read at all,
// and the verdict to time it as: one refused for its size counts as that,
// the rest as malformed, the same as a body the decision couldn't parse.
func (c *Core) ReadError(tooLarge bool) (Verdict, Answer) {
	if tooLarge {
		c.p.counters.tooLarge.Add(1)
		return VerdictTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
	}
	c.p.counters.malformed.Add(1)
	return VerdictMalformed, c.answer(http.StatusBadRequest,
		"malformed request", "BAD_REQUEST")
}

// ExpectationFailed counts a request naming an expectation the proxy can't meet,
// which RFC 9110 requires any recipient to refuse rather than forward.
//
// It answers a verdict and no [Answer]: the status is 417 and the body empty,
// which is what net/http's own server writes for one, and matching it is the
// point — an underlay whose server doesn't know the header refuses it here
// instead, so a client sees the same answer whichever carried the request.
func (c *Core) ExpectationFailed() Verdict {
	c.p.counters.malformed.Add(1)
	return VerdictMalformed
}

// Observe records how long a request took. start is when it arrived.
func (c *Core) Observe(v Verdict, start time.Time) {
	switch v {
	case VerdictAllowed:
		c.p.metrics.Observe(decisionAllowed, start)
	case VerdictRejected:
		c.p.metrics.Observe(decisionRejected, start)
	case VerdictTooLarge:
		c.p.metrics.Observe(decisionTooLarge, start)
	case VerdictAmbiguous:
		c.p.metrics.Observe(decisionAmbiguous, start)
	case VerdictTooDeep:
		c.p.metrics.Observe(decisionTooDeep, start)
	default:
		c.p.metrics.Observe(decisionMalformed, start)
	}
}

// CountUpstreamError records a request that was allowed and that the upstream
// didn't answer.
func (c *Core) CountUpstreamError() { c.p.counters.upstream.Add(1) }

// The settings an underlay needs, read rather than copied so that one source
// stays the source.
func (c *Core) MaxBody() int64       { return c.p.maxBody }
func (c *Core) Debug() bool          { return c.p.debug }
func (c *Core) LogRequests() bool    { return c.p.logRequests }
func (c *Core) TrustForwarded() bool { return c.p.trustForwarded }
func (c *Core) Log() zerolog.Logger  { return c.p.log }
func (c *Core) ContentType() string  { return contentTypeJSON[0] }
func (c *Core) Write(w io.Writer, a Answer) {
	WriteErrorBody(w, a.Message, a.Extension)
}

// IsGraphQLContentType reports whether a body of this content type is the
// document itself. It's exported for the underlays that fill [Request].
func IsGraphQLContentType(ct string) bool { return isGraphQLContentType(ct) }

// WriteErrorBody writes the GraphQL error envelope, without the status or the
// headers, which are the underlay's to set.
func WriteErrorBody(w io.Writer, message, extension string) {
	writeErrorBody(w, message, extension)
}
