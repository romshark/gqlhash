package acceptance

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// GraphQL over SSE is the subscription transport this proxy can protect: every
// operation is an ordinary request carrying an ordinary document, so the
// allowlist applies to a subscription exactly as it applies to a query.
// What these tests pin is that the answer then reaches the client as a
// stream — event by event, for as long as the API sends events — under both commands.
//
// See cmd/gqlhash-proxy/README.md for what a deployment has to do.

// eventAPI is an API that answers a subscription the way graphql-sse does in
// distinct connections mode: 200, text/event-stream, and one `next` event per
// tick until it has sent them all.
type eventAPI struct {
	url string

	// events is how many to send, spaced by interval.
	events   int
	interval time.Duration

	mu       sync.Mutex
	requests []recorded
}

func newEventAPI(t *testing.T, events int, interval time.Duration) *eventAPI {
	t.Helper()
	api := &eventAPI{events: events, interval: interval}
	mux := http.NewServeMux()
	mux.Handle("/graphql", api)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	api.url = "http://" + listener.Addr().String() + "/graphql"
	return api
}

func (a *eventAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	a.mu.Lock()
	a.requests = append(a.requests, recorded{
		method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
		host: r.Host, body: body, header: r.Header.Clone(),
	})
	a.mu.Unlock()

	// A header an implementation must relay untouched, beside the media type.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
	for i := range a.events {
		_, _ = fmt.Fprintf(w,
			"event: next\ndata: {\"data\":{\"tick\":%d}}\n\n", i)
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(a.interval):
		}
	}
	_, _ = fmt.Fprint(w, "event: complete\ndata: \n\n")
	flusher.Flush()
}

func (a *eventAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *eventAPI) last(t *testing.T) recorded {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) == 0 {
		t.Fatal("the upstream saw no request")
	}
	return a.requests[len(a.requests)-1]
}

func readAll(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	return string(b), err
}

// event is one SSE event as it reached the client, with when it arrived.
type event struct {
	name string
	data string
	at   time.Duration
}

// next is the events carrying results,
// which is every one but the `complete` that ends the stream.
func next(events []event) []event {
	out := make([]event, 0, len(events))
	for _, e := range events {
		if e.name == "next" {
			out = append(out, e)
		}
	}
	return out
}

// readEvents reads an SSE answer off conn until the stream ends or deadline
// passes, answering the status line, the headers and every event with the time
// it arrived. It reads the wire itself: whether an event arrives when the API
// sent it or only once the answer ends is the whole question,
// and a buffering client would hide it.
func readEvents(
	t *testing.T, conn net.Conn, within time.Duration,
) (status string, header http.Header, events []event) {
	t.Helper()
	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)

	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	status = strings.TrimRight(line, "\r\n")

	header = http.Header{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok {
			header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}

	// The body is chunked or unbounded, and either way what matters is the
	// event lines and when each one landed. An event is its name followed by its data,
	// so the name is carried to the data line that completes it.
	var name string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return status, header, events
		}
		line = strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			name = rest
			continue
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			events = append(events, event{
				name: name, data: data, at: time.Since(start),
			})
			if name == "complete" {
				// The stream ended. Reading on would only wait out the
				// deadline on a connection the client may keep.
				return status, header, events
			}
		}
	}
}

// TestServerSentEvents covers a subscription answered as an event stream.
//
// Three things have to hold for GraphQL over SSE to work through the proxy at all,
// and none of them held before: the answer has to reach the client event by event
// rather than at the end, it has to outlive -upstream.timeout,
// which bounds an exchange and which a subscription is not, and the media type and
// the headers beside it have to arrive as the API sent them.
func TestServerSentEvents(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// Ten events over a second, against an exchange bound of 300ms:
		// a stream that outlives the bound several times over,
		// so an implementation that applies the bound to it can't pass by being slow.
		api := newEventAPI(t, 10, 100*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir,
			"-upstream.timeout", "300ms")

		body := docAllowed
		for _, tc := range []struct {
			name    string
			request string
		}{
			{
				name: "a POST asking for an event stream",
				request: fmt.Sprintf("POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\n"+
					"Accept: text/event-stream\r\n"+
					"Content-Length: %d\r\n\r\n%s", len(body), body),
			},
			{
				// EventSource can only send a GET,
				// so this is the shape a browser's own client produces.
				name: "a GET asking for an event stream",
				request: "GET /graphql?query=" + url.QueryEscape(allowedText) +
					" HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\n\r\n",
			},
			{
				// A client that names the media type among others,
				// which is what a q-value ordering produces.
				name: "an Accept naming the media type among others",
				request: fmt.Sprintf("POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\n"+
					"Accept: application/json, text/event-stream;q=0.9\r\n"+
					"Content-Length: %d\r\n\r\n%s", len(body), body),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if _, err := conn.Write([]byte(tc.request)); err != nil {
					t.Fatal(err)
				}

				status, header, events := readEvents(t, conn, 10*time.Second)
				if !strings.HasPrefix(status, "HTTP/1.1 200") {
					t.Fatalf("expected 200; received %q: %s", status, s.log)
				}
				if got := header.Get("Content-Type"); !strings.HasPrefix(
					got, "text/event-stream") {
					t.Errorf("expected the media type relayed; received %q", got)
				}
				if got := header.Get("Cache-Control"); got != "no-cache" {
					t.Errorf("expected the headers beside it relayed;"+
						" Cache-Control was %q", got)
				}
				// The API decides to stream by reading Accept, so a proxy that
				// dropped it would be asking for an ordinary answer and
				// answering a subscription that never arrives.
				if got := api.last(t).header.Get("Accept"); !strings.Contains(
					got, "text/event-stream") {
					t.Errorf("expected the client's Accept forwarded;"+
						" the API received %q", got)
				}

				// Every result, so the stream wasn't cut by the exchange bound,
				// and the `complete` that ends it.
				results := next(events)
				if len(results) != 10 {
					t.Fatalf("expected 10 next events; received %d: %v",
						len(results), events)
				}
				if len(events) == 0 || events[len(events)-1].name != "complete" {
					t.Errorf("expected the stream ended by a complete event: %v",
						events)
				}
				// Event by event: the last one is sent about 900ms after the first,
				// so an answer that arrived whole has them landing together.
				// Half the spread is far outside any scheduling noise
				// and far inside a buffered answer's zero.
				spread := results[len(results)-1].at - results[0].at
				if spread < 450*time.Millisecond {
					t.Errorf("expected the events spread over the stream;"+
						" the ten arrived within %s: %v", spread, events)
				}
				// And the first one long before the API had finished sending,
				// which is what a client waiting on a subscription needs.
				if results[0].at > 250*time.Millisecond {
					t.Errorf("expected the first event relayed at once;"+
						" it arrived after %s", results[0].at)
				}
			})
		}

		// One request each, and every one counted as an ordinary forward.
		if got := api.count(); got != 3 {
			t.Errorf("expected 3 requests forwarded; received %d", got)
		}
		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="allowed"}`); got != 3 {
			t.Errorf("expected 3 counted as allowed; received %v", got)
		}
		// Timed to its headers rather than to the end of the stream:
		// three subscriptions of about a second each,
		// and none of them a second in the histogram.
		if got := metricValue(t, exposition,
			`gqlhash_proxy_request_duration_seconds_sum{decision="allowed"}`,
		); got > 1 {
			t.Errorf("expected a stream timed to its headers;"+
				" the three summed to %vs", got)
		}
		if got := metricValue(t, exposition,
			`gqlhash_proxy_request_duration_seconds_count{decision="allowed"}`,
		); got != 3 {
			t.Errorf("expected 3 timed; received %v", got)
		}
		if got := metricValue(t, exposition,
			"gqlhash_proxy_upstream_errors_total"); got != 0 {
			t.Errorf("expected no upstream error; received %v", got)
		}
	})
}

// TestServerSentEventsChecksTheDocument covers the point of the whole exercise:
// asking for a stream is not a way past the allowlist. A subscription carrying
// a document that isn't on it is refused like any other request,
// and the API never sees it.
func TestServerSentEventsChecksTheDocument(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newEventAPI(t, 3, 10*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir)

		for _, tc := range []struct {
			name    string
			body    string
			status  int
			carrier string
		}{
			{
				"a document not on the list", docRejected,
				http.StatusForbidden, "a POST body",
			},
			{
				"no document at all", `{}`,
				http.StatusBadRequest, "a POST body",
			},
			{
				"a body that is no request", `not json`,
				http.StatusBadRequest, "a POST body",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodPost,
					"http://"+s.address+"/graphql", strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "text/event-stream")
				code, answer := send(t, req)
				if code != tc.status {
					t.Errorf("expected %d; received %d: %s",
						tc.status, code, answer)
				}
			})
		}

		if got := api.count(); got != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d requests", got)
		}

		// And the allowed one still goes through on the same server,
		// so the refusals above aren't the proxy refusing every stream.
		req, err := http.NewRequest(http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if code, answer := send(t, req); code != http.StatusOK {
			t.Errorf("expected the allowed subscription served; received %d: %s",
				code, answer)
		}
		if got := api.count(); got != 1 {
			t.Errorf("expected one request forwarded; the API saw %d", got)
		}
	})
}

// TestAskingForAStreamStillBoundsAnOrdinaryAnswer covers the other side of the
// rule. What escapes -upstream.timeout is an answer that is a stream,
// not a request that asked for one: an API answering JSON to a client that named
// text/event-stream is an ordinary exchange and is bounded like one.
//
// Without this a client could ask for a stream, receive an answer that never
// completes and hold the forward open for as long as it liked.
func TestAskingForAStreamStillBoundsAnOrdinaryAnswer(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		// An API that answers JSON headers and then stops,
		// which is the case -upstream.timeout exists for.
		url := rawUpstream(t, func(conn net.Conn) {
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: 4096\r\n\r\n{\"data\":"))
			time.Sleep(10 * time.Second)
		})
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "500ms")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn, "POST /graphql HTTP/1.1\r\nHost: x\r\n"+
			"Content-Type: application/json\r\nAccept: text/event-stream\r\n"+
			"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}

		// Whatever it answers, it stops waiting. What must not happen is the
		// forward outliving the bound because the client named a media type:
		// the API here sends headers and then nothing for ten seconds.
		start := time.Now()
		buf := make([]byte, 4096)
		_, err = conn.Read(buf)
		took := time.Since(start)
		if err != nil && took > 3*time.Second {
			t.Fatalf("expected an answer within the bound; read failed after %s: %v",
				took, err)
		}
		if took > 3*time.Second {
			t.Errorf("expected the exchange bounded; the answer took %s: %s",
				took, s.log)
		}
	})
}

// TestStreamAskedForAndNotGiven covers a client naming text/event-stream whose
// request the API answers with something else.
//
// The bodyless case is the one that matters: reading a body stream that isn't
// there dereferences nil inside a goroutine no recover reaches,
// so one allowed request against an API answering 204 takes the process down.
// The client controls Accept and the API supplies the status, so that's a remote kill.
//
// Every case asserts the answer and that the proxy is still serving afterwards,
// which is the part a crash fails.
func TestStreamAskedForAndNotGiven(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		for _, tc := range []struct {
			name   string
			answer string
			status int
		}{
			{
				name: "a bodyless 204",
				answer: "HTTP/1.1 204 No Content\r\n" +
					"Connection: close\r\n\r\n",
				status: http.StatusNoContent,
			},
			{
				name: "a 304 with no body",
				answer: "HTTP/1.1 304 Not Modified\r\n" +
					"Connection: close\r\n\r\n",
				status: http.StatusNotModified,
			},
			{
				name: "an ordinary JSON answer",
				answer: "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n" +
					"Content-Length: 31\r\nConnection: close\r\n\r\n" +
					upstreamAnswer,
				status: http.StatusOK,
			},
			{
				// The shape graphql-sse allows for a request the API refuses.
				name: "a 400 carrying a GraphQL error",
				answer: "HTTP/1.1 400 Bad Request\r\n" +
					"Content-Type: application/json\r\n" +
					"Content-Length: 26\r\nConnection: close\r\n\r\n" +
					`{"errors":[{"message":""}]}`[:26],
				status: http.StatusBadRequest,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				answer := tc.answer
				url := rawUpstream(t, func(conn net.Conn) {
					_, _ = conn.Write([]byte(answer))
				})
				s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
					"-upstream.timeout", "2s")

				req, err := http.NewRequest(http.MethodPost,
					"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "text/event-stream")
				code, _ := send(t, req)
				if code != tc.status {
					t.Errorf("expected %d; received %d: %s", tc.status, code, s.log)
				}

				// Still serving: a panic in a goroutine takes the process with
				// it, and the answer above would have arrived either way.
				if !s.running() {
					t.Fatalf("the proxy died answering it: %s", s.log)
				}
				code, _ = control(t, s, http.MethodGet, "/status", "")
				if code != http.StatusOK {
					t.Errorf("expected it still answering; received %d: %s",
						code, s.log)
				}
			})
		}
	})
}

// TestAbandonedStreamLeavesThePoolClean covers the normal end of a
// subscription: the client goes away while the API is still sending events.
//
// The connection that stream was read over is one the API is still writing into,
// so returning it to the pool hands the next subscription a socket with
// somebody else's events queued on it, read as a status line: one abandoned
// stream fails the next two forwards. A browser tab closing is exactly this
// case, so a deployment meets it constantly.
func TestAbandonedStreamLeavesThePoolClean(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// Long enough that every subscription below is abandoned mid-flight,
		// and no longer: each iteration waits out whatever the API still has to
		// send, so a longer stream is test runtime rather than more assertion.
		api := newEventAPI(t, 20, 20*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir)

		open := func(t *testing.T) {
			t.Helper()
			conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintf(conn,
				"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\nAccept: text/event-stream\r\n"+
					"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
			); err != nil {
				t.Fatal(err)
			}
			// Read the head and a couple of events, then walk away.
			buf := make([]byte, 4096)
			if _, err := conn.Read(buf); err != nil {
				t.Fatalf("expected the stream to start; received %v", err)
			}
			time.Sleep(80 * time.Millisecond)
			_ = conn.Close()
		}

		// Several abandoned subscriptions, each followed by one that has to
		// work: a poisoned connection shows as the next forward failing.
		for i := range 6 {
			open(t)
			time.Sleep(50 * time.Millisecond)
			if code, answer := post(t, s, docAllowed); code != http.StatusOK {
				t.Fatalf("forward %d after an abandoned stream: expected 200;"+
					" received %d: %s", i, code, answer)
			}
		}

		if got := metricValue(t, scrape(t, s),
			"gqlhash_proxy_upstream_errors_total"); got != 0 {
			t.Errorf("expected no upstream error from abandoned streams;"+
				" received %v", got)
		}
	})
}

// TestStreamTriggerIsTheClientsAccept covers who decides that an answer is
// relayed as a stream rather than bounded like an exchange.
//
// The client asks and the API agrees. Only the request's Accept is available to
// both implementations: fasthttp picks a client before there is an answer to look at,
// so deciding on the answer's Content-Type alone would split the two for an API
// that streams to a client that never asked.
//
// A stream nobody asked for is an ordinary exchange, bounded by
// -upstream.timeout, and since it never completes within one the client sees
// the truncated-answer rule: not a complete 200.
func TestStreamTriggerIsTheClientsAccept(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// A stream far longer than the exchange bound below.
		api := newEventAPI(t, 100, 20*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir,
			"-upstream.timeout", "300ms")

		for _, tc := range []struct {
			name   string
			accept string
			stream bool
		}{
			{"naming the media type", "text/event-stream", true},
			{
				"naming it among others",
				"application/json, text/event-stream;q=0.9", true,
			},
			// A wildcard is not an ask: every browser sends one, and reading it
			// as one would put every request on the streaming path.
			{"a wildcard", "*/*", false},
			{"another type entirely", "application/json", false},
			{"no Accept at all", "", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(time.Now().Add(6 * time.Second)); err != nil {
					t.Fatal(err)
				}
				accept := ""
				if tc.accept != "" {
					accept = "Accept: " + tc.accept + "\r\n"
				}
				if _, err := fmt.Fprintf(conn,
					"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
						"Content-Type: application/json\r\n%s"+
						"Content-Length: %d\r\n\r\n%s",
					accept, len(docAllowed), docAllowed); err != nil {
					t.Fatal(err)
				}

				// When the last byte arrived, not when the connection went:
				// a bounded answer may still leave the connection open for
				// keep-alive, which says nothing about the bound.
				start := time.Now()
				if err := conn.SetReadDeadline(
					time.Now().Add(1200 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 4096)
				var read int
				var last time.Duration
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						read, last = read+n, time.Since(start)
					}
					if err != nil {
						break
					}
				}

				if tc.stream {
					// It was still arriving well past the exchange bound.
					if last < 700*time.Millisecond {
						t.Errorf("expected a stream to outlive -upstream.timeout;"+
							" the last of %d bytes arrived after %s", read, last)
					}
					return
				}
				// Not a stream, so the bound applies. Whether the client gets a
				// refusal or a connection that breaks is the implementation's,
				// the same as any answer cut off mid-body; what must not happen is
				// the bound being escaped by an answer nobody asked for.
				if last > 700*time.Millisecond {
					t.Errorf("expected the exchange bounded;"+
						" bytes were still arriving after %s", last)
				}
			})
		}
	})
}

// TestShutdownWithALiveStream covers a subscription open when the signal
// arrives.
//
// A drain waits for what's in flight and a subscription has no natural end,
// so without this a proxy carrying one sits out the whole
// -server.shutdown-timeout and exits 1 — every deploy a ten-second stop
// reported as a failure, or a SIGKILL under a shorter grace period.
//
// A stream is closed rather than waited for:
// an exchange is something to finish, a subscription something to end.
func TestShutdownWithALiveStream(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newEventAPI(t, 500, 20*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir,
			"-server.shutdown-timeout", "5s")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
				"Content-Type: application/json\r\nAccept: text/event-stream\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}
		// The stream is running before the signal.
		buf := make([]byte, 4096)
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("expected the stream to start; received %v", err)
		}

		start := time.Now()
		s.interrupt()
		code := s.wait(t)
		took := time.Since(start)

		if code != 0 {
			t.Errorf("expected a clean stop with a stream open; received %d: %s",
				code, s.log)
		}
		// Well inside the 5s it was given: waiting for a subscription means
		// waiting for the whole timeout, every time.
		if took > 3*time.Second {
			t.Errorf("expected the stream closed rather than waited for;"+
				" the stop took %s", took)
		}
	})
}

// TestStreamAfterAnEarlyHint covers an API that sends a 1xx before its answer.
//
// An informational answer is not the answer, and a proxy that decided on the
// first status it wrote would read a 103's headers as the stream's and bound
// the subscription — which a client can arrange against any API that sends early hints.
func TestStreamAfterAnEarlyHint(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		url := rawUpstream(t, func(conn net.Conn) {
			// An early hint, then the stream itself.
			_, _ = conn.Write([]byte("HTTP/1.1 103 Early Hints\r\n" +
				"Link: </style.css>; rel=preload\r\n\r\n"))
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n" +
				"Content-Type: text/event-stream\r\n\r\n"))
			for i := range 40 {
				if _, err := fmt.Fprintf(conn,
					"event: next\ndata: {\"tick\":%d}\n\n", i); err != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		})
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "400ms")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
				"Content-Type: application/json\r\nAccept: text/event-stream\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		var last time.Duration
		var got strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				last = time.Since(start)
				got.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}

		// The rule both keep: a hint is not the answer. Either the stream ran
		// past the exchange bound — the hint didn't decide it — or the client
		// was left with no final answer at all, which is what an implementation whose
		// client can't read past a 1xx can offer. What must not happen is the
		// hint being served as a complete answer.
		answer := got.String()
		if strings.Contains(answer, "HTTP/1.1 103") &&
			strings.Count(answer, "HTTP/1.1 ") == 1 {
			// Only the hint arrived, and nothing final after it.
			return
		}
		if last < time.Second {
			t.Errorf("expected the stream to outlive -upstream.timeout;"+
				" the last byte arrived after %s: %s", last, s.log)
		}
	})
}

// TestStreamEscapesTheServerTimeouts passes the flags a stream has to outlive,
// which no other SSE test does — so the central claim of the streaming path,
// that a subscription is bounded by none of them, was asserted nowhere.
//
// All four are set well below the length of the stream:
// an implementation applying any one of them to it loses the subscription.
func TestStreamEscapesTheServerTimeouts(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// About three seconds of events.
		api := newEventAPI(t, 30, 100*time.Millisecond)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir,
			"-upstream.timeout", "500ms",
			"-server.write-timeout", "1s",
			"-server.read-header-timeout", "400ms",
			"-server.read-timeout", "800ms",
			"-server.idle-timeout", "600ms")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
				"Content-Type: application/json\r\nAccept: text/event-stream\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}

		status, _, events := readEvents(t, conn, 15*time.Second)
		if !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Fatalf("expected 200; received %q: %s", status, s.log)
		}
		if got := len(next(events)); got != 30 {
			t.Errorf("expected all 30 events past every timeout; received %d: %s",
				got, s.log)
		}
		if len(events) == 0 || events[len(events)-1].name != "complete" {
			t.Errorf("expected the stream to end on its own: %v", events)
		}
	})
}
