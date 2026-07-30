package acceptance

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// Requests the HTTP implementation itself deals with, before or beside the
// proxy. What they have in common is that no allowlist decision was involved,
// so what the suite can hold an implementation to is the part that protects the
// API: nothing reaches it, and nothing 2xx reaches the client.

// answers reads HTTP answers off one connection, one after another.
//
// [readAnswer] can't: it keeps no reader between calls, so whatever arrived
// past the answer it returned is dropped. That's fine for one answer and wrong
// for the two an Expect handshake produces or the three a pipelining client
// asks for, which is the whole point of the tests below.
type answers struct {
	reader *bufio.Reader
	t      *testing.T
}

func newAnswers(t *testing.T, conn net.Conn) *answers {
	t.Helper()
	return &answers{reader: bufio.NewReader(conn), t: t}
}

// next reads one answer: the status line, the headers, and the body its
// Content-Length names. A 1xx carries neither a body nor a length.
func (a *answers) next() (status, body string) {
	a.t.Helper()
	line, err := a.reader.ReadString('\n')
	if err != nil {
		a.t.Fatalf("reading the status line: %v", err)
	}
	status = strings.TrimRight(line, "\r\n")

	length := 0
	for {
		line, err := a.reader.ReadString('\n')
		if err != nil {
			a.t.Fatalf("reading the headers of %q: %v", status, err)
		}
		if line == "\r\n" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &length); err != nil {
				a.t.Fatalf("reading the length of %q: %v", status, err)
			}
		}
	}
	if length == 0 {
		return status, ""
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(a.reader, buf); err != nil {
		a.t.Fatalf("reading the body of %q: %v", status, err)
	}
	return status, string(buf)
}

// TestHTTPLayerRefusals covers requests refused by the HTTP implementation
// rather than by the proxy.
//
// `gqlhash-proxy` answers these inside http.Server, before any handler or hook
// exists to count them, so they carry a text/plain body and move no counter;
// `gqlhash-proxy-fhttp` reaches its ErrorHandler and answers the JSON envelope
// counting `malformed`. Neither can be made to do the other's thing without
// replacing its server, so what's pinned is what both keep and what an API
// behind either depends on: not 2xx, and nothing forwarded.
//
// "Every answered request is counted and timed" covers the requests the proxy
// answered. These are the ones it never saw, so sum(requests_total) counts what
// the proxy decided rather than every byte the socket took.
func TestHTTPLayerRefusals(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, tc := range []struct{ name, request string }{
			{"a bad request line", "GARBAGE\r\n\r\n"},
			{"a request line with no version", "GET /graphql\r\n\r\n"},
			{
				name: "two conflicting Content-Length",
				request: "POST /graphql HTTP/1.1\r\nHost: x\r\n" +
					"Content-Type: application/json\r\n" +
					"Content-Length: 5\r\nContent-Length: 6\r\n\r\nhello",
			},
			{
				// RFC 9112 requires a Host on HTTP/1.1.
				name: "HTTP/1.1 with no Host",
				request: fmt.Sprintf("POST /graphql HTTP/1.1\r\n"+
					"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
					len(docAllowed), docAllowed),
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				status := raw(t, e.address, tc.request)
				if !strings.HasPrefix(status, "HTTP/1.1 4") {
					t.Errorf("expected a refusal; received %q", status)
				}
			})
		}

		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}

// TestExpectContinue covers the handshake curl uses for a large body.
//
// The body has to be read before there is a document to decide on, so the
// proxy has nothing to gate the continuation on and answers `100 Continue`
// first — even to a request it is about to refuse. That's worth pinning
// because the opposite is a plausible implementation: withholding the 100 from
// a document that isn't allowed would deadlock every client that waits for it.
func TestExpectContinue(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, tc := range []struct {
			name, body, status string
		}{
			{"an allowed document", docAllowed, "HTTP/1.1 200"},
			{"a document that isn't", docRejected, "HTTP/1.1 403"},
			{"a body that is no request", "not json", "HTTP/1.1 400"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(
					time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				// The whole request at once: a client that waited for the 100
				// would be testing its own patience rather than the proxy,
				// and what's under test is that the 100 comes at all.
				if _, err := fmt.Fprintf(conn,
					"POST /graphql HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\n"+
						"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
					len(tc.body), tc.body); err != nil {
					t.Fatal(err)
				}

				reading := newAnswers(t, conn)
				first, _ := reading.next()
				if !strings.HasPrefix(first, "HTTP/1.1 100") {
					t.Fatalf("expected 100 Continue first; received %q", first)
				}
				second, _ := reading.next()
				if !strings.HasPrefix(second, tc.status) {
					t.Errorf("expected %s after it; received %q", tc.status, second)
				}
				// And nothing after that: the expectation was the proxy's to
				// meet and it met it, so the API is never asked to run the
				// handshake again for a body already in flight. Forwarding the
				// header made net/http relay the API's 100 as well, so a client
				// that had one received two.
				if !strings.HasPrefix(second, "HTTP/1.1 200") {
					return
				}
				if got := e.api.last(t).header.Get("Expect"); got != "" {
					t.Errorf("expected the expectation not forwarded;"+
						" the API received %q", got)
				}
			})
		}
	})
}

// TestExpectationTheProxyCantMeet covers an Expect naming something else.
//
// RFC 9110 has a recipient refuse an expectation it doesn't understand rather
// than pass it on, and a proxy that forwarded one would be asking the API to
// answer for a request the proxy is the recipient of. net/http refuses it
// inside its own server; fasthttp knows only 100-continue, so the fhttp
// underlay refuses it in the handler. Both answer 417 and neither forwards.
//
// What counts it differs — one refusal happens before any handler exists —
// which is the same difference every wire-level refusal has.
func TestExpectationTheProxyCantMeet(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, expect := range []string{"bogus", "200-ok", "100-Continue-Please"} {
			t.Run(expect, func(t *testing.T) {
				status := raw(t, e.address, fmt.Sprintf(
					"POST /graphql HTTP/1.1\r\nHost: x\r\nExpect: %s\r\n"+
						"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
					expect, len(docAllowed), docAllowed))
				if !strings.HasPrefix(status, "HTTP/1.1 417") {
					t.Errorf("expected 417; received %q: %s", status, e.log)
				}
			})
		}

		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}

		// The expectation that is understood isn't refused, whatever case it's
		// written in: RFC 9110 makes it a token, and a client capitalising it
		// asked for something the proxy can do.
		//
		// Only the final answer is pinned. Whether an interim 100 precedes it
		// is the underlay's: net/http matches the token without case and sends
		// one, fasthttp compares it byte for byte and doesn't. Both then serve
		// the request, which is what a client of either depends on — and a client
		// that waits for a continuation it never gets is a client whose
		// own timeout ends the wait, not a request answered wrongly.
		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nExpect: 100-Continue\r\n"+
				"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
			len(docAllowed), docAllowed); err != nil {
			t.Fatal(err)
		}
		reading := newAnswers(t, conn)
		status, _ := reading.next()
		if strings.HasPrefix(status, "HTTP/1.1 100") {
			status, _ = reading.next()
		}
		if !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Errorf("expected it served; received %q: %s", status, e.log)
		}
		if n := e.api.count(); n != 1 {
			t.Errorf("expected it forwarded; the API saw %d", n)
		}
	})
}

// TestPipelinedRequests covers three requests written before any answer is read,
// which is what a pipelining client does.
//
// The failure mode is severe and quiet: answers reordered, or one request
// answered with another's body. Since the three here get different verdicts,
// a proxy that mixed them up can't produce this sequence by accident.
func TestPipelinedRequests(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}

		want := []struct{ body, status string }{
			{docAllowed, "HTTP/1.1 200"},
			{docRejected, "HTTP/1.1 403"},
			{docAllowed, "HTTP/1.1 200"},
		}
		var pipelined strings.Builder
		for _, w := range want {
			_, _ = fmt.Fprintf(&pipelined,
				"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
				len(w.body), w.body)
		}
		if _, err := conn.Write([]byte(pipelined.String())); err != nil {
			t.Fatal(err)
		}

		reading := newAnswers(t, conn)
		for i, w := range want {
			status, body := reading.next()
			if !strings.HasPrefix(status, w.status) {
				t.Fatalf("answer %d: expected %s; received %q", i, w.status, status)
			}
			if w.status == "HTTP/1.1 200" && body != upstreamAnswer {
				t.Errorf("answer %d: expected the upstream answer; received %q",
					i, body)
			}
		}
		if n := e.api.count(); n != 2 {
			t.Errorf("expected the two allowed forwarded; the API saw %d", n)
		}
	})
}

// TestRequestTrailerDoesNotChangeTheDecision covers a chunked request whose
// trailer follows the last chunk.
//
// Whether that trailer reaches the API, and as what, is a difference between
// the two commands that can't be closed on the fasthttp side: it parses every
// trailer field into the ordinary header set before the handler runs, declared
// or not, so afterwards nothing can tell one from a header sent in the head.
// gqlhash-proxy keeps it a trailer, which Go delivers in Request.Trailer and
// never in Request.Header.
//
// What both keep, and what this pins, is that a trailer buys nothing:
// the document is still read from the chunks, still hashed, and still decided on.
// An implementation that let a trailer disturb the body it decides on would be
// reading a document nobody hashed.
func TestRequestTrailerDoesNotChangeTheDecision(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		chunked := func(body string, trailer bool) string {
			head := "POST /graphql HTTP/1.1\r\nHost: x\r\n" +
				"Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n"
			if trailer {
				head += "Trailer: X-Hop\r\n"
			}
			out := head + "\r\n" + fmt.Sprintf("%x\r\n%s\r\n0\r\n", len(body), body)
			if trailer {
				out += "X-Hop: smuggled\r\n"
			}
			return out + "\r\n"
		}

		for _, tc := range []struct {
			name, request, status string
		}{
			{"an allowed document with a declared trailer",
				chunked(docAllowed, true), "HTTP/1.1 200"},
			{"an allowed document with none",
				chunked(docAllowed, false), "HTTP/1.1 200"},
			{"a document that isn't, with a trailer",
				chunked(docRejected, true), "HTTP/1.1 403"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := raw(t, e.address, tc.request); !strings.HasPrefix(
					got, tc.status) {
					t.Errorf("expected %s; received %q: %s", tc.status, got, e.log)
				}
			})
		}

		// The two allowed ones reached the API carrying the document itself,
		// so the chunks were read as the body and the trailer wasn't part of it.
		if n := e.api.count(); n != 2 {
			t.Fatalf("expected 2 forwarded; the API saw %d", n)
		}
		for i, got := range e.api.all() {
			if got.body != docAllowed {
				t.Errorf("request %d: expected the document forwarded whole;"+
					"\n want %q\n have %q", i, docAllowed, got.body)
			}
		}
	})
}

// TestChunkedRequestBody covers the happy path of a chunked body and the two
// ways a client can lie about one. The proxy reads the document out of the chunks,
// so a body that doesn't arrive as declared is no document to decide on.
func TestChunkedRequestBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		t.Run("several chunks", func(t *testing.T) {
			half := len(docAllowed) / 2
			request := "POST /graphql HTTP/1.1\r\nHost: x\r\n" +
				"Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
				fmt.Sprintf("%x\r\n%s\r\n%x\r\n%s\r\n0\r\n\r\n",
					half, docAllowed[:half],
					len(docAllowed)-half, docAllowed[half:])
			if got := raw(t, e.address, request); !strings.HasPrefix(
				got, "HTTP/1.1 200") {
				t.Errorf("expected 200; received %q: %s", got, e.log)
			}
			if got := e.api.last(t).body; got != docAllowed {
				t.Errorf("expected the document reassembled;"+
					"\n want %q\n have %q", docAllowed, got)
			}
		})

		t.Run("a chunk size larger than the bytes", func(t *testing.T) {
			before := e.api.count()
			// The declared chunk never completes, so the request never does.
			// Whatever each command makes of that, the API sees nothing:
			// there is no document to have decided on.
			request := "POST /graphql HTTP/1.1\r\nHost: x\r\n" +
				"Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
				fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(docAllowed)+16, docAllowed)
			status := raw(t, e.address, request)
			if strings.HasPrefix(status, "HTTP/1.1 2") {
				t.Errorf("expected no 2xx for a body that never arrives; received %q",
					status)
			}
			if e.api.count() != before {
				t.Errorf("expected nothing forwarded; the API saw %d", e.api.count())
			}
		})

		t.Run("a chunk size smaller than the bytes", func(t *testing.T) {
			before := e.api.count()
			request := "POST /graphql HTTP/1.1\r\nHost: x\r\n" +
				"Content-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n" +
				fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(docAllowed)-5, docAllowed)
			if got := raw(t, e.address, request); !strings.HasPrefix(
				got, "HTTP/1.1 4") {
				t.Errorf("expected a refusal; received %q", got)
			}
			if e.api.count() != before {
				t.Errorf("expected nothing forwarded; the API saw %d", e.api.count())
			}
		})
	})
}
