package acceptance

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestMethods covers what the data plane makes of the method. It routes on no path,
// and it serves two methods: GraphQL over HTTP defines the document in the
// query string of a GET and in the body of a POST, and a request arriving as
// anything else is refused with 405 before its document is read.
//
// A DELETE carrying an allowed document would otherwise reach the API as a shape
// no entry of the allowlist describes, and what an API makes of one is the API's
// business.
func TestMethods(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		// The two that are served, each reading the document where it belongs.
		if code, answer := post(t, e.server, docAllowed); code != http.StatusOK ||
			answer != upstreamAnswer {
			t.Errorf("POST: expected the upstream answer; received %d: %s",
				code, answer)
		}
		if code, answer := get(t, e.server,
			"query="+url.QueryEscape(allowedText)); code != http.StatusOK ||
			answer != upstreamAnswer {
			t.Errorf("GET: expected the upstream answer; received %d: %s",
				code, answer)
		}
		if n := e.api.count(); n != 2 {
			t.Fatalf("expected two forwarded requests; received %d", n)
		}

		// Every other method, carrying the same allowed document: the method is
		// read before the document, so the answer is the same either way.
		for _, method := range []string{
			http.MethodPut, http.MethodPatch, http.MethodDelete,
			http.MethodOptions, http.MethodHead,
		} {
			t.Run(method, func(t *testing.T) {
				req, err := http.NewRequest(method,
					"http://"+e.address+"/graphql", strings.NewReader(docAllowed))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				code, answer, header := sendFor(t, req)
				if code != http.StatusMethodNotAllowed {
					t.Errorf("expected 405; received %d: %s", code, answer)
				}
				// RFC 9110 asks a 405 to name what is allowed.
				if got := header.Get("Allow"); got != "GET, POST" {
					t.Errorf("expected Allow: GET, POST; received %q", got)
				}
				// A HEAD carries no body to say it in.
				if method != http.MethodHead &&
					!strings.Contains(answer, "METHOD_NOT_ALLOWED") {
					t.Errorf("expected the reason in the body; received %s", answer)
				}
			})
		}

		// Nothing of them reached the API, and each is counted apart from every
		// other refusal: a client using the wrong verb is its own event.
		if n := e.api.count(); n != 2 {
			t.Errorf("a refused method must not reach upstream; the API saw %d", n)
		}
		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if v := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="method_not_allowed"}`); v != 5 {
			t.Errorf("expected five counted method_not_allowed; received %v", v)
		}
	})
}

// TestRequestContentTypes covers what the content type decides, which is where
// the document is and nothing else: only application/graphql makes the body the document,
// so every other type, and none at all, is read as a JSON request.
func TestRequestContentTypes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		post := func(t *testing.T, contentType, body string) (int, string) {
			t.Helper()
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.address+"/graphql", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			return send(t, req)
		}

		// A JSON request, whatever it says it is.
		for _, contentType := range []string{
			"application/json", "application/json; charset=utf-8",
			"APPLICATION/JSON", "text/plain", "",
		} {
			if code, answer := post(t, contentType, docAllowed); code != http.StatusOK {
				t.Errorf("%q: expected 200; received %d: %s", contentType, code, answer)
			}
		}

		// The body is the document itself,
		// under every spelling of the one type that says so.
		for _, contentType := range []string{
			"application/graphql", "application/graphql; charset=utf-8",
			"APPLICATION/GRAPHQL", " application/graphql ",
		} {
			if code, answer := post(t, contentType, allowedText); code != http.StatusOK {
				t.Errorf("%q: expected 200; received %d: %s", contentType, code, answer)
			}
		}

		// A JSON request that isn't JSON is malformed,
		// and a document body that isn't allowed is refused.
		if code, _ := post(t, "application/json", allowedText); code !=
			http.StatusBadRequest {
			t.Errorf("expected the document read as JSON to be malformed; received %d",
				code)
		}
		if code, _ := post(t, "application/graphql", rejectedText); code !=
			http.StatusForbidden {
			t.Errorf("expected 403; received %d", code)
		}
	})
}

// TestGETQueryString covers the query string a GET carries the document in,
// down to the forms that carry none.
func TestGETQueryString(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		encoded := url.QueryEscape(allowedText)

		for _, tc := range []struct {
			name, rawQuery string
			expect         int
		}{
			// A '+' is a space, which is what net/url writes for one.
			{"percent encoded", "query=" + encoded, http.StatusOK},
			{"spaces as +", "query=" + strings.ReplaceAll(encoded, "%20", "+"),
				http.StatusOK},
			{"the document beside other parameters",
				"operationName=GetUser&query=" + encoded + "&variables=%7B%7D",
				http.StatusOK},
			// No query parameter at all is a request carrying no document.
			{"no query string", "", http.StatusBadRequest},
			{"another parameter", "operationName=GetUser", http.StatusBadRequest},
			// The parameter is there and empty, which is a document of no length:
			// it's read, it's hashed, and nothing on the list is empty.
			{"empty", "query=", http.StatusForbidden},
			{"valueless", "query", http.StatusForbidden},
			// A broken escape is no document to read.
			{"broken escape", "query=%7", http.StatusBadRequest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if code, answer := get(t, e.server, tc.rawQuery); code != tc.expect {
					t.Errorf("expected %d; received %d: %s", tc.expect, code, answer)
				}
			})
		}
	})
}

// TestLongDocument covers a document too long for a small read buffer.
// The limit on a request is -server.max-body and nothing else,
// so a document that fits it is served however it arrives: in a body,
// or in the request line of a GET, which is where a server sizes its own buffer.
func TestLongDocument(t *testing.T) {
	// Long enough to pass the 4KiB a read buffer commonly defaults to,
	// and well inside the default -server.max-body of a megabyte.
	long := "query GetUser {\n  user(id: 1) {\n    name\n" +
		strings.Repeat("    alias: name\n", 512) + "  }\n}"

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, long)
		body, err := jsonRequest(long)
		if err != nil {
			t.Fatal(err)
		}

		if code, answer := post(t, e.server, body); code != http.StatusOK {
			t.Errorf("POST: expected 200; received %d: %s", code, answer)
		}
		rawQuery := "query=" + url.QueryEscape(long)
		if code, answer := get(t, e.server, rawQuery); code != http.StatusOK {
			t.Errorf("GET: expected 200 for a request line of %d bytes; received %d: %s",
				len(rawQuery), code, answer)
		}
	})
}

// TestGETWithBody covers a GET carrying a body. Its document is the query parameter,
// so a body is a second place one could be, and which of them an API reads is the API's
// business: the request is refused rather than forwarded with a body nothing here read.
func TestGETWithBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		rawQuery := "query=" + url.QueryEscape(allowedText)

		for _, tc := range []struct{ name, body string }{
			{"a document of its own", docRejected},
			{"the allowed document", docAllowed},
			{"anything at all", "x"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodGet,
					"http://"+e.address+"/graphql?"+rawQuery,
					strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				code, answer := send(t, req)
				if code != http.StatusBadRequest {
					t.Errorf("expected 400; received %d: %s", code, answer)
				}
				if !strings.Contains(answer, "ambiguous") {
					t.Errorf("expected the reason; received %s", answer)
				}
			})
		}

		// The same request without the body is the one it was pretending to be.
		if code, _ := get(t, e.server, rawQuery); code != http.StatusOK {
			t.Errorf("expected the document itself allowed; received %d", code)
		}
		if n := e.api.count(); n != 1 {
			t.Errorf("expected only that one forwarded; received %d", n)
		}
	})
}

// TestGETBodyIsBytesNotFraming covers what "carrying a body" means when the
// body is empty or its length isn't declared.
//
// A body is bytes. An empty one names no document, so the request is decided on
// its query string; bytes do, whatever framing declared them. Neither shape
// here is reachable through [http.Request], which always declares a length,
// so both are written raw — and both are where the two commands can disagree,
// one reading a framing and the other the bytes.
func TestGETBodyIsBytesNotFraming(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		allowed := "query=" + url.QueryEscape(allowedText)

		for _, tc := range []struct {
			name    string
			request string
			status  string
		}{
			{
				// No bytes under a length of zero.
				name: "a declared length of zero",
				request: "GET /graphql?" + allowed + " HTTP/1.1\r\nHost: x\r\n" +
					"Content-Length: 0\r\n\r\n",
				status: "HTTP/1.1 200",
			},
			{
				// No bytes under a chunked framing either, which is the shape
				// net/http counted as a body because its length is unknown.
				name: "an empty chunked body",
				request: "GET /graphql?" + allowed + " HTTP/1.1\r\nHost: x\r\n" +
					"Transfer-Encoding: chunked\r\n\r\n0\r\n\r\n",
				status: "HTTP/1.1 200",
			},
			{
				// Bytes, so the document is named twice.
				name: "a chunked body carrying bytes",
				request: "GET /graphql?" + allowed + " HTTP/1.1\r\nHost: x\r\n" +
					"Transfer-Encoding: chunked\r\n\r\n2\r\nhi\r\n0\r\n\r\n",
				status: "HTTP/1.1 400",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := raw(t, e.address, tc.request); !strings.HasPrefix(
					got, tc.status) {
					t.Errorf("expected %s; received %q", tc.status, got)
				}
			})
		}
	})
}

// TestGETBodyOverMaxBody covers a GET whose body is past -server.max-body.
//
// The size bound is checked first, whatever the method, because it's what
// bounds the work: the proxy never reads a megabyte to conclude that a request
// was also ambiguous. So it's `too_large` and not `ambiguous`,
// which matters because `ambiguous` is the series worth an alert —
// a probe must raise the same one on both commands or it raises it on neither.
func TestGETBodyOverMaxBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-server.max-body", "128")
		allowed := "query=" + url.QueryEscape(allowedText)
		body := strings.Repeat("x", 2000)

		got := raw(t, e.address, fmt.Sprintf(
			"GET /graphql?%s HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s",
			allowed, len(body), body))
		if !strings.HasPrefix(got, "HTTP/1.1 413") {
			t.Errorf("expected 413; received %q: %s", got, e.log)
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if v := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="too_large"}`); v != 1 {
			t.Errorf("expected it counted too_large; received %v", v)
		}
		if v := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="ambiguous"}`); v != 0 {
			t.Errorf("expected nothing counted ambiguous; received %v", v)
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}

// TestMaxBodyDoesNotBoundAGETQueryString covers the other half of that flag:
// it bounds bodies, and a GET carries its document in the request line,
// which -server.max-body has never bounded. What bounds that is the header limit of
// the implementation, which differs between the two on purpose.
func TestMaxBodyDoesNotBoundAGETQueryString(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// A document far past the limit set below, so an implementation
		// applying -server.max-body to the query string would refuse it.
		long := "query GetUser {\n  user(id: 1) {\n    name\n" +
			strings.Repeat("    alias: name\n", 50) + "  }\n}"
		e := newEnv(t, tgt, []string{long}, "-server.max-body", "128")

		if len(long) < 512 {
			t.Fatalf("expected a document past the limit; it is %d bytes", len(long))
		}
		code, answer := get(t, e.server, "query="+url.QueryEscape(long))
		if code != http.StatusOK {
			t.Errorf("expected it served; received %d: %s", code, answer)
		}
		if n := e.api.count(); n != 1 {
			t.Errorf("expected it forwarded; the API saw %d", n)
		}
	})
}
