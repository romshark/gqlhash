package proxy

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// errDraining ends a stream because the proxy is shutting down.
var errDraining = errors.New("the proxy is draining")

// drainContext adapts the drain channel to what [context.AfterFunc] takes.
// Nothing reads its value or its error.
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

// streamWriter releases the ordinary exchange bounds once the answer turns out
// to be an event stream.
//
// A subscription is answered for as long as the client listens,
// so the deadlines that keep a slow API from holding a connection are the wrong tool:
// -upstream.timeout would cut the stream, -server.write-timeout the client.
// Both are dropped here and only here.
//
// A field of the pooled per-request state, so relaying allocates nothing.
type streamWriter struct {
	http.ResponseWriter

	proxy *proxy
	start time.Time

	// asked is whether the client named text/event-stream in Accept.
	// Both the ask and the answer have to hold: a stream nobody asked for is no
	// subscription, and the fasthttp underlay picks a client before there is an
	// answer to look at, so it can only go on this. Deciding on the answer alone
	// makes the two disagree for the `*/*` client, which is most non-browsers.
	asked bool

	// deadline bounds the forward, nil where -upstream.timeout is off.
	deadline *time.Timer

	// cancel ends the forward, which closes a stream when the drain starts.
	// stopDraining unregisters that watch once the request is over.
	cancel       context.CancelCauseFunc
	stopDraining func() bool

	// streamed tells [proxy.ServeHTTP] the request has been timed already.
	streamed bool

	// decided is set once the status is written, so the answer is read once
	// however many times a handler writes.
	decided bool

	// failed is set where the error handler ran and counted the upstream failure.
	// It can't run once a status is on the wire, which is the only
	// case [proxy.ServeHTTP] counts for itself: reading the context cause
	// instead would count twice when the deadline fires between the two.
	failed bool
}

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
	// An informational answer is not the answer. ReverseProxy relays a 1xx by
	// writing it here, so deciding on the first WriteHeader would read a 103's
	// headers as the answer's and leave a stream bounded — arrangeable by a
	// client against an API that sends early hints.
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
			// the stream. Cleared for this answer alone: net/http sets the
			// deadline again for the next request on the connection.
			_ = http.NewResponseController(w.ResponseWriter).
				SetWriteDeadline(time.Time{})
			// This never ends on its own, so it ends when the drain starts.
			// Registered only here: an ordinary exchange is short and is waited for.
			if w.cancel != nil {
				w.stopDraining = context.AfterFunc(
					drainContext{w.proxy.draining},
					func() { w.cancel(errDraining) })
			}
			// Timed at its headers, so an hour-long subscription doesn't land
			// in the histogram as an hour. The histogram answers how quickly
			// the proxy answers; how long a client listens is the client's.
			w.proxy.metrics.Observe(decisionAllowed, w.start)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap gives [http.ResponseController] the writer underneath,
// so a flush — what makes a stream arrive event by event — reaches the connection.
func (w *streamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
