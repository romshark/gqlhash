package acceptance

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestForwardedRequestFidelity covers what the API receives of a request the
// proxy allowed. The proxy stands between a client and an API that authorizes,
// routes and traces on what it's sent, so everything but the hop-by-hop headers
// and the endpoint itself has to arrive as the client wrote it.
func TestForwardedRequestFidelity(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/somewhere/else?trace=abc&tenant=7",
			strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		// What an API authorizes and traces on.
		req.Header.Set("Authorization", "Bearer client-token")
		req.Header.Set("X-Request-Id", "abc123")
		req.Header.Set("Accept-Language", "de-DE")
		// What belongs to this hop alone.
		req.Header.Set("Proxy-Authorization", "Basic ignored")
		req.Header.Set("Keep-Alive", "timeout=5")
		req.Header.Set("X-Hop", "named by Connection")
		req.Header.Set("Connection", "X-Hop")
		// What the client claims the host is.
		req.Host = "client.example.com"

		if code, answer := send(t, req); code != http.StatusOK {
			t.Fatalf("expected 200; received %d: %s", code, answer)
		}
		got := e.api.last(t)

		// The method is the client's: an API answering GET and POST differently
		// must see which one this was.
		if got.method != http.MethodPost {
			t.Errorf("expected the method forwarded; received %s", got.method)
		}
		// The endpoint is -upstream.url, whatever path the client used.
		if got.path != "/graphql" {
			t.Errorf("expected the path of -upstream.url; received %s", got.path)
		}
		// The query string is the client's, untouched.
		if got.rawQuery != "trace=abc&tenant=7" {
			t.Errorf("expected the query string forwarded; received %q", got.rawQuery)
		}
		// The host is the upstream's: forwarding the client's would let a client
		// pick which virtual host of the API answers.
		if strings.EqualFold(got.host, "client.example.com") {
			t.Errorf("expected the host of the upstream; received %s", got.host)
		}
		// The body arrives byte for byte, since that's what was hashed.
		if got.body != docAllowed {
			t.Errorf("expected the body forwarded; received %q", got.body)
		}

		// Everything the API needs arrives.
		for header, want := range map[string]string{
			"Authorization":   "Bearer client-token",
			"X-Request-Id":    "abc123",
			"Accept-Language": "de-DE",
			"Content-Type":    "application/json",
		} {
			if v := got.header.Get(header); v != want {
				t.Errorf("expected %s %q upstream; received %q", header, want, v)
			}
		}

		// What belongs to this hop is dropped, including a header the client
		// named in Connection: passing one on smuggles it past a front end that
		// asked for it to end here.
		for _, header := range []string{
			"Connection", "Keep-Alive", "Proxy-Authorization", "X-Hop",
		} {
			if v := got.header.Get(header); v != "" {
				t.Errorf("expected %s to be dropped; received %q", header, v)
			}
		}
	})
}

// TestForwardedGETKeepsTheDocument covers the query string of a GET,
// which is where its document is:
// an API that receives the request without it receives no document at all.
func TestForwardedGETKeepsTheDocument(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		rawQuery := "query=" + url.QueryEscape(allowedText) +
			"&operationName=GetUser&variables=%7B%7D"
		if code, answer := get(t, e.server, rawQuery); code != http.StatusOK {
			t.Fatalf("expected 200; received %d: %s", code, answer)
		}

		got := e.api.last(t)
		if got.rawQuery != rawQuery {
			t.Errorf("expected the query string forwarded whole;\n want %q\n have %q",
				rawQuery, got.rawQuery)
		}
		if got.method != http.MethodGet {
			t.Errorf("expected a GET upstream; received %s", got.method)
		}
	})
}

// TestForwardedGETKeepsASemicolonQuery covers the separator that isn't `&`.
//
// The reading rule splits a query string on `;` as well as `&`, so a document
// named twice either way is refused rather than forwarded. The forwarding half
// can defeat that: net/http's proxy drops the parameters net/url can't parse,
// which for a `;`-separated query is all of them, leaving the API an allowed
// GET carrying no document.
//
// Neither half is any use without the other, so the query string reaches the
// API exactly as the client wrote it.
func TestForwardedGETKeepsASemicolonQuery(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, tc := range []struct{ name, rawQuery string }{
			{"a parameter after the document", "query=" +
				url.QueryEscape(allowedText) + ";a=1"},
			{"a parameter before it", "a=1;query=" +
				url.QueryEscape(allowedText)},
			{"both separators at once", "a=1;b=2&query=" +
				url.QueryEscape(allowedText)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := e.api.count()
				if code, answer := get(t, e.server, tc.rawQuery); code != http.StatusOK {
					t.Fatalf("expected 200; received %d: %s", code, answer)
				}
				if e.api.count() != before+1 {
					t.Fatalf("expected it forwarded; the API saw %d", e.api.count())
				}
				if got := e.api.last(t).rawQuery; got != tc.rawQuery {
					t.Errorf("expected the query string forwarded whole;"+
						"\n want %q\n have %q", tc.rawQuery, got)
				}
			})
		}

		// And the reading half it protects: `;` separates,
		// so this names the document twice and is refused rather than forwarded.
		twice := "query=" + url.QueryEscape(allowedText) +
			";query=" + url.QueryEscape(rejectedText)
		before := e.api.count()
		code, answer := get(t, e.server, twice)
		if code != http.StatusBadRequest {
			t.Errorf("expected 400 for a document named twice; received %d: %s",
				code, answer)
		}
		if e.api.count() != before {
			t.Errorf("expected nothing forwarded; the API saw %d", e.api.count())
		}
	})
}

// TestQueryParamBesideBody covers a request whose document is its body and
// which names a query parameter as well. The proxy hashes the body; an API that
// reads the parameter for a POST would run what nobody hashed, and which of the
// two it reads is the API's business. It's the case a GET carrying a body is,
// the other way round, and it's answered the same way.
func TestQueryParamBesideBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, tc := range []struct{ name, rawQuery, body string }{
			{"another document", "query=" + url.QueryEscape(rejectedText), docAllowed},
			{"the same document", "query=" + url.QueryEscape(allowedText), docAllowed},
			{
				"beside other parameters",
				"operationName=GetUser&query=" + url.QueryEscape(rejectedText), docAllowed,
			},
			{"empty", "query=", docAllowed},
			{"percent encoded name", "quer%79=" + url.QueryEscape(rejectedText), docAllowed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodPost,
					"http://"+e.address+"/graphql?"+tc.rawQuery,
					strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				code, answer := send(t, req)
				if code != http.StatusBadRequest {
					t.Errorf("expected 400; received %d: %s", code, answer)
				}
				if !strings.Contains(answer, "ambiguous") {
					t.Errorf("expected the reason; received %s", answer)
				}
			})
		}

		// A parameter that isn't the document is no ambiguity:
		// a client may trace, and an API may route, on what the URL carries.
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql?operationName=GetUser&trace=1",
			strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if code, answer := send(t, req); code != http.StatusOK {
			t.Errorf("expected 200; received %d: %s", code, answer)
		}
	})
}
