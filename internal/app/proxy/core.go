package proxy

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// Core is the decision a proxy makes, without the HTTP machinery that carries it.
// The types here name no HTTP implementation, so what a request is answered
// with can't depend on which one served it.
//
// The net/http server lives in this package and uses the unexported form
// directly. Core exists for the ones that don't, see
// [github.com/romshark/gqlhash/v2/internal/app/proxyfast].
type Core struct{ p *proxy }

func (p *proxy) Core() *Core { return &Core{p: p} }

// Method is the request method, as much of it as the decision needs:
// where the document is read from, and whether the request is served at all.
type Method uint8

const (
	// MethodOther is every method that is neither GET nor POST, and the zero
	// value so that a Request built without one is refused rather than served.
	//
	// GraphQL over HTTP defines those two. Anything else carrying an allowed
	// document would reach the API as a shape no entry of the allowlist describes,
	// and what an API makes of a DELETE holding a query is the API's business.
	MethodOther Method = iota
	MethodGET
	MethodPOST
)

// AllowedMethods is the Allow header of the 405 that answers [MethodOther],
// which RFC 9110 requires of one.
const AllowedMethods = "GET, POST"

// Request is what the decision needs of a request, which is less than an HTTP
// implementation carries. Each fills it from whatever types it holds.
type Request struct {
	// Method decides where the document is read from, and that a request is
	// refused where it's [MethodOther].
	Method Method

	// RawQuery is the query string, unparsed. Filled whatever the method:
	// a GET reads its document from it,
	// every other method is refused for naming a query parameter at all.
	RawQuery string

	// BodyIsDocument says the body is the document itself rather than a JSON
	// request carrying one. The implementation decides it with [IsGraphQLContentType].
	BodyIsDocument bool

	// Body is the whole request body, already read. Read and not kept,
	// so an implementation owning the bytes can pass its own.
	Body []byte

	// HasBody is asked only of a GET: one carrying a body names its document twice.
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
	// so nothing was read of it. Apart from malformed, see decisionTooLarge.
	VerdictTooLarge
	// VerdictAmbiguous answers alone: the request names its document more than once,
	// see decisionAmbiguous.
	VerdictAmbiguous
	// VerdictTooDeep answers alone: the document nests past the depth limit,
	// see decisionTooDeep.
	VerdictTooDeep
	// VerdictBatchTooLarge answers alone: the batch carries more documents than
	// -server.max-batch allows, see decisionBatchTooLarge.
	VerdictBatchTooLarge
	// VerdictMethodNotAllowed answers alone: the method is neither GET nor POST,
	// see [MethodOther] and decisionMethodNotAllowed.
	VerdictMethodNotAllowed
)

// Answer is what to write when a request isn't forwarded.
// It has already been through -opaque-errors, so an implementation writes it as it is.
//
// A 405 carries the Allow header of [AllowedMethods], which every implementation
// sets from the status: RFC 9110 requires it of a 405, and under -opaque-errors
// the status is a 403 that names nothing.
type Answer struct {
	Code               int
	Message, Extension string
}

// Decide reports what to do with req, counts it, and returns the answer to
// write where it isn't forwarded. Nothing of req is retained.
func (c *Core) Decide(req Request) (Verdict, Answer) {
	p := c.p
	st := p.states.Get().(*state)
	defer func() {
		st.release()
		p.states.Put(st)
	}()

	if req.Method != MethodGET && int64(len(req.Body)) > p.maxBody {
		p.counters.tooLarge.Add(1)
		return VerdictTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
	}

	allowed, err := p.decide(st, req)
	switch {
	case errors.Is(err, errMethodNotAllowed):
		p.counters.methodBad.Add(1)
		// The Allow header belongs to the answer and is the implementation's to set,
		// since it goes out only where the status stays a 405, see [Answer].
		return VerdictMethodNotAllowed, c.answer(http.StatusMethodNotAllowed,
			err.Error(), "METHOD_NOT_ALLOWED")
	case errors.Is(err, errTooLarge):
		p.counters.tooLarge.Add(1)
		return VerdictTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			"request body too large", "REQUEST_TOO_LARGE")
	case errors.Is(err, errTooDeep):
		p.counters.tooDeep.Add(1)
		return VerdictTooDeep, c.answer(http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
	case errors.Is(err, errBatchTooLarge):
		p.counters.batchBig.Add(1)
		return VerdictBatchTooLarge, c.answer(http.StatusRequestEntityTooLarge,
			err.Error(), "BATCH_TOO_LARGE")
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

// answer applies -opaque-errors, the one rule every implementation asks for rather
// than carrying a copy of.
func (c *Core) answer(code int, message, extension string) Answer {
	code, message, extension = c.p.rejection(code, message, extension)
	return Answer{Code: code, Message: message, Extension: extension}
}

// ReadError is the answer for a request an implementation couldn't read at all.
// One refused for its size counts as too_large, the rest as malformed.
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

// ExpectationFailed counts a request naming an expectation the proxy can't
// meet, which RFC 9110 requires any recipient to refuse rather than forward.
//
// No [Answer]: the status is 417 with an empty body,
// matching what net/http's own server writes,
// so a client sees the same answer whichever implementation carried the request.
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
	case VerdictBatchTooLarge:
		c.p.metrics.Observe(decisionBatchTooLarge, start)
	case VerdictMethodNotAllowed:
		c.p.metrics.Observe(decisionMethodNotAllowed, start)
	default:
		c.p.metrics.Observe(decisionMalformed, start)
	}
}

// CountUpstreamError records a request that was allowed and that the upstream
// didn't answer.
func (c *Core) CountUpstreamError() { c.p.counters.upstream.Add(1) }

// The settings an implementation needs,
// read rather than copied so one source stays the source.
func (c *Core) Debug() bool          { return c.p.debug }
func (c *Core) LogRequests() bool    { return c.p.logRequests }
func (c *Core) TrustForwarded() bool { return c.p.trustForwarded }

// ContentType is the media type an [Answer] is written with,
// given the Accept header of the request it answers, see [AcceptsGraphQLResponseJSON].
// It takes one so that an implementation can't answer a client in a media type
// the other wouldn't.
func (c *Core) ContentType(accept string) string { return errorContentType(accept)[0] }

// IsGraphQLContentType reports whether a body of this content type is the
// document itself. It's exported for the implementations that fill [Request].
func IsGraphQLContentType(ct string) bool { return isGraphQLContentType(ct) }

// MergeQuery is the query string a forwarded request carries, given the query of
// -upstream.url and the client's, see [mergeQuery].
// Exported so every implementation builds the same one.
func MergeQuery(upstream, client string) string { return mergeQuery(upstream, client) }

// WriteErrorBody writes the GraphQL error envelope, without the status or the
// headers, which are the implementation's to set.
func WriteErrorBody(w io.Writer, message, extension string) {
	writeErrorBody(w, message, extension)
}
