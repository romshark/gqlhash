package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// errDraining ends a stream because the proxy is shutting down.
var errDraining = errors.New("the proxy is draining")

// drainContext turns the drain channel into a [context.Context],
// which is what [context.AfterFunc] takes. Nothing reads its value or its error.
type drainContext struct{ done <-chan struct{} }

func (d drainContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (d drainContext) Done() <-chan struct{}       { return d.done }
func (d drainContext) Value(any) any               { return nil }
func (d drainContext) Err() error {
	select {
	case <-d.done:
		return context.Canceled
	default:
		return nil
	}
}

// streamWriter releases what bounds an ordinary exchange, once the answer turns
// out to be an event stream.
//
// A subscription is answered for as long as the client listens,
// so the deadlines that keep a slow API from holding a connection are the wrong tool
// for it: -upstream.timeout would cut the stream and -server.write-timeout
// would cut the client. Both are dropped here, and only here — everything that
// isn't a stream is bounded exactly as it was.
//
// It's a field of the pooled per-request state rather than a value of its own,
// so an answer relayed through it allocates nothing.
type streamWriter struct {
	http.ResponseWriter

	proxy *proxy
	start time.Time

	// asked is whether the client named text/event-stream in Accept. Both have
	// to hold: an API that answers a stream to a client that never asked for
	// one is not a subscription, and the fasthttp underlay has to choose a
	// client before there is an answer to look at, so it can only go on this.
	// Deciding on the answer alone made the two disagree — one streamed such an
	// answer and the other refused it — for the client that sends `*/*`,
	// which is most non-browser clients.
	asked bool

	// deadline bounds the forward, nil where -upstream.timeout is off.
	deadline *time.Timer

	// cancel ends the forward, which is what closes a stream when the drain starts.
	// stopDraining unregisters that watch once the request is over.
	cancel       context.CancelCauseFunc
	stopDraining func() bool

	// streamed says the answer was an event stream, which is what tells
	// [proxy.ServeHTTP] the request has been timed already.
	streamed bool

	// decided is set once the status has been written, so the answer is read
	// once however many times a handler writes.
	decided bool

	// failed is set where the error handler ran, which is where an upstream
	// failure is counted. It can't run once a status is on the wire,
	// and that is the only case [proxy.ServeHTTP] counts for itself — reading the
	// context's cause instead would count twice whenever the deadline fired
	// between the handler and the check.
	failed bool
}

// reset points the writer at one request's answer.
func (w *streamWriter) reset(
	rw http.ResponseWriter, p *proxy, start time.Time, deadline *time.Timer,
	asked bool, cancel context.CancelCauseFunc,
) {
	*w = streamWriter{
		ResponseWriter: rw, proxy: p, start: start,
		deadline: deadline, asked: asked, cancel: cancel,
	}
}

func (w *streamWriter) WriteHeader(code int) {
	// An informational answer is not the answer. ReverseProxy relays a 1xx from
	// the API by writing it here, so deciding on the first WriteHeader would
	// read a 103's headers as the answer's and leave a stream bounded —
	// which a client can arrange against an API that sends early hints.
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if !w.decided {
		w.decided = true
		if w.asked && IsEventStream(w.Header().Get("Content-Type")) {
			w.streamed = true
			if w.deadline != nil {
				// -upstream.timeout bounds an exchange, and a stream is none:
				// the API answers until the subscription ends.
				w.deadline.Stop()
			}
			// -server.write-timeout bounds writing one answer, so it would cut
			// the stream at whatever it's set to. Cleared for this answer alone:
			// net/http sets the deadline again for the next request on the connection.
			_ = http.NewResponseController(w.ResponseWriter).
				SetWriteDeadline(time.Time{})
			// A drain waits for what's in flight, and this never ends on its own,
			// so it ends when the drain starts instead.
			// Registered only here: an ordinary exchange is short and is waited for.
			if w.cancel != nil {
				w.stopDraining = context.AfterFunc(
					drainContext{w.proxy.draining},
					func() { w.cancel(errDraining) })
			}
			// Timed to its headers rather than to the end of the stream, so a
			// subscription that runs for an hour doesn't land in the latency
			// histogram as an hour. What the histogram answers is how quickly
			// the proxy answers; how long a client listens is the client's.
			w.proxy.metrics.Observe(decisionAllowed, w.start)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap gives [http.ResponseController] the writer underneath,
// so flushing — which is what makes an event stream arrive event by event —
// reaches the connection instead of stopping here.
func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
