package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestMethods covers what the data plane makes of the method. It routes on none:
// the document is read from the query string of a GET and from the body
// of anything else, and which methods the API answers is the API's business.
// A conformance suite pins that, so a server that started refusing methods
// would be a change made on purpose rather than a difference nobody noticed.
func TestMethods(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// A body-carrying method reaches the API with the document it carried.
		for _, method := range []string{
			http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodOptions,
		} {
			req, err := http.NewRequest(method, "http://"+e.address+"/graphql",
				strings.NewReader(docAllowed))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			code, answer := send(t, req)
			if code != http.StatusOK || answer != upstreamAnswer {
				t.Errorf("%s: expected the upstream answer; received %d: %s",
					method, code, answer)
			}
		}

		// A HEAD carries the same decision and no body to answer with.
		req, err := http.NewRequest(http.MethodHead, "http://"+e.address+"/graphql",
			strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if code, answer := send(t, req); code != http.StatusOK || answer != "" {
			t.Errorf("HEAD: expected 200 and no body; received %d: %q", code, answer)
		}

		// A document that isn't allowed is refused whatever carries it.
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, "http://"+e.address+"/graphql",
				strings.NewReader(docRejected))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if code, _ := send(t, req); code != http.StatusForbidden {
				t.Errorf("%s: expected 403; received %d", method, code)
			}
		}
	})
}

// TestRequestContentTypes covers what the content type decides, which is where
// the document is and nothing else: only application/graphql makes the body the
// document, so every other type, and none at all, is read as a JSON request.
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
