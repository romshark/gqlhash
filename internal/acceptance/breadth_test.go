package acceptance

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rows the audit found unpinned: each is one or two assertions on
// behaviour that already holds, so what they buy is that a reimplementation
// can't get them wrong quietly.

// TestBatchShapes covers -max-batch beyond the well-formed array of two.
// Every shape here is refused with nothing forwarded, and a batch is a JSON
// array of GraphQL requests or it isn't a batch.
func TestBatchShapes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-max-batch", "8")

		for _, tc := range []struct{ name, body string }{
			{"an empty array", `[]`},
			{"a number", `[42]`},
			{"a string that is the document", `["` + allowedText + `"]`},
			{"an array of arrays", `[[{"query":"` + allowedText + `"}]]`},
			{"a null element", `[null]`},
			{"an element naming no document", `[{}]`},
			{"the document under another member",
				`[{"variables":{"query":"` + allowedText + `"}}]`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, answer := post(t, e.server, tc.body)
				if code != http.StatusBadRequest {
					t.Errorf("expected 400; received %d: %s", code, answer)
				}
			})
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}

		// The shape that is a batch is still served, so the rows above are
		// refusals of the shape rather than of batching.
		if code, answer := post(t, e.server,
			`[{"query":"`+allowedText+`"},{"query":"`+allowedText+`"}]`,
		); code != http.StatusOK {
			t.Errorf("expected a well-formed batch served; received %d: %s",
				code, answer)
		}

		// Where the rule falls: every document a batch carries has to be
		// allowed, and an element carrying none carries none. So an allowed
		// document beside a number is served, where that number alone is
		// refused for naming no document. Nothing unhashed reaches the API
		// either way, but the two rows read oddly together.
		if code, answer := post(t, e.server,
			`[{"query":"`+allowedText+`"},7]`); code != http.StatusOK {
			t.Errorf("expected the element carrying no document ignored;"+
				" received %d: %s", code, answer)
		}
		// And an element that does carry one is checked like any other.
		if code, _ := post(t, e.server,
			`[{"query":"`+allowedText+`"},{"query":"`+rejectedText+`"}]`,
		); code != http.StatusForbidden {
			t.Errorf("expected a batch carrying a refused document refused;"+
				" received %d", code)
		}
	})
}

// TestQueryParamBesideBodyShapes covers the shapes of that refusal the suite
// reached only through `&`-separated, valued, application/json requests.
func TestQueryParamBesideBodyShapes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		encoded := url.QueryEscape(allowedText)

		for _, tc := range []struct {
			name, rawQuery, contentType, body string
			expect                            int
		}{
			// The parameter with no value at all is still the parameter.
			{"a valueless query parameter", "query", "application/json",
				docAllowed, http.StatusBadRequest},
			{"an empty query parameter", "query=", "application/json",
				docAllowed, http.StatusBadRequest},
			// `;` separates as well as `&` does, on the reading side too.
			{"a semicolon-separated parameter", "a=1;query=" + encoded,
				"application/json", docAllowed, http.StatusBadRequest},
			// The check runs before the content type is looked at,
			// so a body that is the document itself is refused the same way.
			{"beside an application/graphql body", "query=" + encoded,
				"application/graphql", allowedText, http.StatusBadRequest},
			// The negative control: the parameter name is case-sensitive where
			// the JSON member is not, so this one is no collision at all.
			{"a parameter that isn't named query", "QUERY=" + encoded,
				"application/json", docAllowed, http.StatusOK},
			{"another parameter entirely", "operationName=GetUser",
				"application/json", docAllowed, http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodPost,
					"http://"+e.address+"/graphql?"+tc.rawQuery,
					strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", tc.contentType)
				if code, answer := send(t, req); code != tc.expect {
					t.Errorf("expected %d; received %d: %s", tc.expect, code, answer)
				}
			})
		}
	})
}

// TestJSONEscapeShapes covers what the JSON layer refuses and what it hashes.
//
// The line between 400 and 403 is where the escape stops being JSON:
// a sequence JSON has no reading for is a body that couldn't be parsed,
// while a byte that is merely not UTF-8 parses into a document that simply
// isn't on the list.
func TestJSONEscapeShapes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, tc := range []struct {
			name, body string
			expect     int
		}{
			{"an escape JSON doesn't define", `{"query":"\x41"}`,
				http.StatusBadRequest},
			{"an escaped letter", `{"query":"\q"}`, http.StatusBadRequest},
			{"a truncated unicode escape", `{"query":"\u12"}`,
				http.StatusBadRequest},
			{"unicode escape digits that aren't hex", `{"query":"\uZZZZ"}`,
				http.StatusBadRequest},
			{"a raw control byte", "{\"query\":\"\x01\"}",
				http.StatusBadRequest},
			// Not an escape problem: it parses, it hashes, and the hash isn't
			// on the list.
			{"a raw 0xFF byte", "{\"query\":\"\xff\"}", http.StatusForbidden},
			// Lowercase hex is as valid as uppercase, and every GET in this
			// suite is built by url.QueryEscape, which only ever writes upper.
			{"a lowercase unicode escape", `{"query":"{}"}`,
				http.StatusForbidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, answer := post(t, e.server, tc.body)
				if code != tc.expect {
					t.Errorf("expected %d; received %d: %s", tc.expect, code, answer)
				}
			})
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}

// TestGETPercentEscapeCase covers a URL written with lowercase hex, which
// url.QueryEscape never produces and so no other test sends.
// A decoder accepting only A-F answers 400 for an ordinary URL from any other client.
func TestGETPercentEscapeCase(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		upper := "query=" + url.QueryEscape(allowedText)
		lower := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'F' {
				return r + ('a' - 'A')
			}
			return r
		}, upper)
		if lower == upper {
			t.Fatal("expected the escaped document to carry hex digits")
		}

		for name, rawQuery := range map[string]string{
			"uppercase hex": upper, "lowercase hex": lower,
		} {
			t.Run(name, func(t *testing.T) {
				if code, answer := get(t, e.server, rawQuery); code != http.StatusOK {
					t.Errorf("expected 200; received %d: %s", code, answer)
				}
			})
		}
	})
}

// TestGETIgnoresContentType covers a content type on a request whose document
// is its query string. The type says where a body's document is,
// and a GET reads no body, so it decides nothing.
func TestGETIgnoresContentType(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, contentType := range []string{
			"application/graphql", "application/json", "text/plain",
		} {
			t.Run(contentType, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodGet, "http://"+e.address+
					"/graphql?query="+url.QueryEscape(allowedText), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", contentType)
				if code, answer := send(t, req); code != http.StatusOK {
					t.Errorf("expected 200; received %d: %s", code, answer)
				}
			})
		}
	})
}

// TestEmptyGraphQLBody covers an application/graphql body of no length.
// The body is the document, so an empty one is a document of no length:
// it's read, it's hashed, and nothing on the list is empty. That's 403 and not 400,
// which is the same reading an empty `query=` parameter gets.
func TestEmptyGraphQLBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(""))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/graphql")
		if code, answer := send(t, req); code != http.StatusForbidden {
			t.Errorf("expected 403; received %d: %s", code, answer)
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}

// TestHEADWithQueryString covers the method a cache or a health checker sends.
//
// Only a GET reads the query string, so for every other method a `query`
// parameter is a parameter beside a body: `ambiguous`, the one series the
// design calls worth an alert. A health check pointed at the wrong URL raises
// the alarm meant for somebody probing — shared behaviour worth knowing.
func TestHEADWithQueryString(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		rawQuery := "query=" + url.QueryEscape(allowedText)

		req, err := http.NewRequest(http.MethodHead,
			"http://"+e.address+"/graphql?"+rawQuery, nil)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := send(t, req); code != http.StatusBadRequest {
			t.Errorf("expected 400 for a HEAD carrying a query string; received %d",
				code)
		}

		// The identical GET is served, which is what makes the row above a
		// property of the method rather than of the URL.
		if code, answer := get(t, e.server, rawQuery); code != http.StatusOK {
			t.Errorf("expected the GET served; received %d: %s", code, answer)
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if v := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="ambiguous"}`); v != 1 {
			t.Errorf("expected the HEAD counted ambiguous; received %v", v)
		}
	})
}

// TestShapesRealClientsSendThatAreRefused covers two requests a GraphQL client
// may well produce and that this proxy has no reading for. Both are refused
// with nothing forwarded, which is correct — there is no document to hash —
// and both are worth pinning so the refusal is a decision rather than an
// accident of parsing.
func TestShapesRealClientsSendThatAreRefused(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		t.Run("a multipart upload", func(t *testing.T) {
			body := "--X\r\nContent-Disposition: form-data; name=\"operations\"\r\n\r\n" +
				docAllowed + "\r\n--X--\r\n"
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.address+"/graphql", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "multipart/form-data; boundary=X")
			if code, answer := send(t, req); code != http.StatusBadRequest {
				t.Errorf("expected 400; received %d: %s", code, answer)
			}
		})

		t.Run("an APQ request carrying only a hash", func(t *testing.T) {
			body := `{"extensions":{"persistedQuery":{"version":1,` +
				`"sha256Hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`
			if code, answer := post(t, e.server, body); code != http.StatusBadRequest {
				t.Errorf("expected 400; received %d: %s", code, answer)
			}
		})

		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}

// TestDuplicateResponseHeaders covers an API answering two Set-Cookie values.
//
// Every other assertion in the suite reads an answer header with Header.Get,
// which returns the first value and cannot see a collapse. A proxy holding
// headers in a map[string]string drops every cookie but one, which is a broken
// login through the proxy and nothing else failing.
func TestDuplicateResponseHeaders(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		upstream := rawUpstream(t, func(conn net.Conn) {
			body := upstreamAnswer
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\n"+
				"Content-Type: application/json\r\n"+
				"Set-Cookie: first=1; Path=/\r\n"+
				"Set-Cookie: second=2; Path=/\r\n"+
				"Content-Length: %d\r\nConnection: close\r\n\r\n%s",
				len(body), body)
		})
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir)

		req, err := http.NewRequest(http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		code, _, header := sendFor(t, req)
		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d", code)
		}
		cookies := header.Values("Set-Cookie")
		if len(cookies) != 2 {
			t.Fatalf("expected both cookies relayed; received %q", cookies)
		}
		if !strings.HasPrefix(cookies[0], "first=1") ||
			!strings.HasPrefix(cookies[1], "second=2") {
			t.Errorf("expected them in order; received %q", cookies)
		}
	})
}

// TestMaxBodyDefault covers the default of -server.max-body, which no test
// passes a flag for: a megabyte, so a body just inside it is served and one
// just past it is refused.
func TestMaxBodyDefault(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// A request whose padding takes it just past a megabyte.
		padding := strings.Repeat("p", 1<<20)
		body := `{"query":"` + allowedText + `","variables":{"p":"` + padding + `"}}`
		if len(body) <= 1<<20 {
			t.Fatalf("expected a body past the default; it is %d bytes", len(body))
		}
		// Either a 413 or a connection the server closed under the client's write:
		// a body past the limit is refused before it has all arrived,
		// and which of the two a client sees is a race between the refusal and
		// the megabyte still going out. What must not happen is it being served.
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		switch {
		case err != nil: // refused before the body finished arriving
		case res.StatusCode != http.StatusRequestEntityTooLarge:
			_ = res.Body.Close()
			t.Errorf("expected 413 past the default megabyte; received %d",
				res.StatusCode)
		default:
			_ = res.Body.Close()
		}
		// The connection it was refused on may be half-written,
		// so it isn't reused for what follows.
		http.DefaultClient.CloseIdleConnections()

		// And one comfortably inside it is served.
		small := `{"query":"` + allowedText + `","variables":{"p":"` +
			strings.Repeat("p", 1000) + `"}}`
		if code, answer := post(t, e.server, small); code != http.StatusOK {
			t.Errorf("expected a body inside the default served; received %d: %s",
				code, answer)
		}
	})
}

// TestUnknownEnvironmentVariable covers a GQLHASH_PROXY_ variable that names no flag,
// including the typos of ones that do. They're ignored: the proxy starts
// and serves, so a misspelled variable is a setting that silently didn't apply
// rather than a start that failed.
func TestUnknownEnvironmentVariable(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)

		t.Setenv("GQLHASH_PROXY_NOT_A_FLAG", "1")
		t.Setenv("GQLHASH_PROXY_SERVER_MAXBODY", "4096") // a real flag, misspelled
		t.Setenv("GQLHASH_PROXY_UPSTREAM_TIMOUT", "1s")  // and another

		// It starts, which is the whole assertion:
		// an unknown variable is neither applied nor a reason to refuse to run.
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)
		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected it serving; received %d: %s", code, answer)
		}
		// And the misspelled -server.max-body didn't take: a body past the 4096
		// it names is still served, since the default megabyte is what applies.
		body := `{"query":"` + allowedText + `","variables":{"p":"` +
			strings.Repeat("p", 8000) + `"}}`
		if code, _ := post(t, s, body); code != http.StatusOK {
			t.Errorf("expected the misspelled limit ignored; received %d", code)
		}
	})
}

// TestWhitespaceControlToken covers a control token that is only whitespace.
// It's trimmed to nothing, which is no token: /reload stays open,
// and an operator who set the variable to a blank line has no protection they think
// they have. Pinning it is what makes that a documented reading rather than an
// accident.
func TestWhitespaceControlToken(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)

		t.Setenv(controlTokenEnv, "   \t  ")
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		if code, body := control(t, s, http.MethodPost, "/reload", ""); code !=
			http.StatusOK {
			t.Errorf("expected a whitespace token to leave /reload open;"+
				" received %d: %s", code, body)
		}
	})
}

// TestUpstreamURLShapes covers what -upstream.url may name past a scheme and a
// host: a path the forward replaces its own with, and a query string on the
// upstream URL, which the request's own replaces.
func TestUpstreamURLShapes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := new(spy)
		mux := http.NewServeMux()
		mux.Handle("/v1/graphql", api)
		upstream := httptest.NewServer(mux)
		t.Cleanup(upstream.Close)

		s := serve(t, tgt, "-upstream.url", upstream.URL+"/v1/graphql",
			"-allowlist", dir)

		if code, answer := post(t, s, docAllowed); code != http.StatusOK {
			t.Fatalf("expected the nested path reached; received %d: %s",
				code, answer)
		}
		if got := api.last(t).path; got != "/v1/graphql" {
			t.Errorf("expected the upstream path used; received %q", got)
		}
	})
}

// TestReloadMethodGate covers the method gate on /reload,
// with and without a token configured.
//
// /reload stays POST-first: a wrong method is 405 whatever the token says,
// so a browser or scraper wandering onto the control address can't spend the work of
// a reload, and a 405 tells nobody whether they had the right token.
func TestReloadMethodGate(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)
		t.Setenv(controlTokenEnv, "s3cret")
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		for _, method := range []string{
			http.MethodGet, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodHead, http.MethodOptions,
		} {
			for _, token := range []string{"", "s3cret", "wrong"} {
				t.Run(method+"/"+token, func(t *testing.T) {
					code, body, header := controlFor(
						t, s, method, "/reload", token)
					if code != http.StatusMethodNotAllowed {
						t.Fatalf("expected 405; received %d: %s", code, body)
					}
					if got := header.Get("Allow"); got != http.MethodPost {
						t.Errorf("expected Allow: POST; received %q", got)
					}
					// A HEAD carries no body to check; the rest say why.
					if method != http.MethodHead && body == "" {
						t.Error("expected a reason in the body")
					}
				})
			}
		}

		// And the method that is allowed still needs the token,
		// so the gate above is the method's rather than the token's.
		if code, _ := control(t, s, http.MethodPost, "/reload", "wrong"); code !=
			http.StatusUnauthorized {
			t.Errorf("expected 401 for a wrong token on POST; received %d", code)
		}
		if code, _ := control(t, s, http.MethodPost, "/reload", "s3cret"); code !=
			http.StatusOK {
			t.Errorf("expected the right token to reload; received %d", code)
		}
	})
}

// TestFailedReloadLeavesTheStateAlone covers what /status says after a reload
// that failed.
//
// A deploy script reads /status to decide whether its new documents are live,
// so stamping the load time at the start of a reload would tell it the new ones
// had landed while the old ones are still served.
func TestFailedReloadLeavesTheStateAlone(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		_, before := control(t, s, http.MethodGet, "/status", "")

		// The directory goes away, so the reload can't read it.
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if code, body := control(t, s, http.MethodPost, "/reload", ""); code !=
			http.StatusInternalServerError {
			t.Fatalf("expected 500 for a reload that couldn't read; received %d: %s",
				code, body)
		}

		// The documents are still served, and /status still says so,
		// down to the load time.
		if code, _ := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the old documents still served; received %d", code)
		}
		_, after := control(t, s, http.MethodGet, "/status", "")
		beforeState, afterState := documentsAndLoadedAt(before), documentsAndLoadedAt(after)
		if beforeState != afterState {
			t.Errorf("expected the state untouched by a failed reload;"+
				"\n before %s\n after  %s", beforeState, afterState)
		}
	})
}

// documentsAndLoadedAt is the part of /status a failed reload must not move.
func documentsAndLoadedAt(status string) string {
	var out []string
	for _, key := range []string{`"documents":`, `"loaded_at":`} {
		if i := strings.Index(status, key); i >= 0 {
			rest := status[i:]
			if j := strings.IndexByte(rest, ','); j >= 0 {
				out = append(out, rest[:j])
			}
		}
	}
	return strings.Join(out, " ")
}

// TestReloadFilesShape covers the answer of a reload past its totals:
// the order of documents.files and the form the paths take.
//
// Every assertion the suite had was len, Contains or HasSuffix, so basenames,
// absolutised paths and map iteration order all passed. A deploy script diffing
// that list between two reloads needs it stable.
func TestReloadFilesShape(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		// Names chosen so that per-directory sorted depth-first and full-path
		// lexicographic disagree: "b.graphql" sorts after "a" as a directory
		// entry, but "a/z.graphql" sorts before "b.graphql" as a whole path.
		writeDocAt(t, dir, "a/z.graphql", allowedDoc)
		writeDocAt(t, dir, "b.graphql", rejectedText)
		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 2 {
			t.Fatalf("expected both documents; received %s", body)
		}

		// The paths keep the -allowlist argument as it was given,
		// rather than being absolutised or reduced to basenames.
		want := []string{
			filepath.Join(dir, "a", "z.graphql"),
			filepath.Join(dir, "b.graphql"),
		}
		if len(answer.Documents.Files) != len(want) {
			t.Fatalf("expected %d files; received %q", len(want),
				answer.Documents.Files)
		}
		for i := range want {
			if answer.Documents.Files[i] != want[i] {
				t.Errorf("file %d: expected %q; received %q",
					i, want[i], answer.Documents.Files[i])
			}
		}

		// And the same order on the next reload, so a diff between two of them
		// is a change of the allowlist rather than of map iteration.
		_, again := control(t, s, http.MethodPost, "/reload", "")
		if second := reloadAnswer(t, again); !equalStrings(
			second.Documents.Files, answer.Documents.Files) {
			t.Errorf("expected a stable order;\n first  %q\n second %q",
				answer.Documents.Files, second.Documents.Files)
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReloadKeepsTheCounters covers the decision counters across a reload.
//
// Rebuilding the serving state per load is the natural implementation,
// and it would reset requests_total on every deploy — which makes every rate() over a
// deploy wrong and every alert on one fire.
func TestReloadKeepsTheCounters(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		if code, _ := post(t, s, docAllowed); code != http.StatusOK {
			t.Fatal("expected the first request served")
		}
		if code, _ := post(t, s, docRejected); code != http.StatusForbidden {
			t.Fatal("expected the second refused")
		}

		if code, body := control(t, s, http.MethodPost, "/reload", ""); code !=
			http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}

		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		for series, want := range map[string]float64{
			`gqlhash_proxy_requests_total{decision="allowed"}`:  1,
			`gqlhash_proxy_requests_total{decision="rejected"}`: 1,
		} {
			if got := metricValue(t, exposition, series); got != want {
				t.Errorf("%s: expected %v across a reload; received %v",
					series, want, got)
			}
		}
	})
}

// TestTooDeepAcrossCarriers covers the depth limit reached by every carrier a
// document has.
//
// 403 is indistinguishable from a plain rejection in the answer, so the label
// is the only thing telling an operator a nesting flood from an allowlist out
// of date. Classifying depth only where the JSON extraction lives books the
// cheapest carrier as `rejected`.
func TestTooDeepAcrossCarriers(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir,
			"-depth-limit", "4", "-max-batch", "8")

		// Nesting well past the limit.
		deep := "{a{b{c{d{e{f{g{h{i{j{k}}}}}}}}}}}"

		body, err := jsonRequest(deep)
		if err != nil {
			t.Fatal(err)
		}
		carriers := []struct {
			name string
			send func() (int, string)
		}{
			{"a JSON body", func() (int, string) { return post(t, s, body) }},
			{"a query parameter", func() (int, string) {
				return get(t, s, "query="+url.QueryEscape(deep))
			}},
			{"an application/graphql body", func() (int, string) {
				req, err := http.NewRequest(http.MethodPost,
					"http://"+s.address+"/graphql", strings.NewReader(deep))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/graphql")
				return send(t, req)
			}},
			{"a batch", func() (int, string) {
				return post(t, s, "["+body+"]")
			}},
		}

		for i, carrier := range carriers {
			t.Run(carrier.name, func(t *testing.T) {
				code, answer := carrier.send()
				if code != http.StatusForbidden {
					t.Fatalf("expected 403; received %d: %s", code, answer)
				}
				_, exposition := control(t, s, http.MethodGet, "/metrics", "")
				if got := metricValue(t, exposition,
					`gqlhash_proxy_requests_total{decision="too_deep"}`,
				); got != float64(i+1) {
					t.Errorf("expected it counted too_deep; the series is at %v",
						got)
				}
			})
		}

		if got := metricValue(t, scrape(t, s),
			`gqlhash_proxy_requests_total{decision="rejected"}`); got != 0 {
			t.Errorf("expected none of them counted rejected; received %v", got)
		}
	})
}

// scrape reads the exposition off the control server.
func scrape(t *testing.T, s *server) string {
	t.Helper()
	_, exposition := control(t, s, http.MethodGet, "/metrics", "")
	return exposition
}

// TestStatusReportsUpstreamErrors covers /status's upstream_errors, which every
// test read at zero and every non-zero observation read off /metrics instead.
//
// /status is the documented alternative to Prometheus, so a smoke test built on
// it would report a healthy proxy while every forward failed.
func TestStatusReportsUpstreamErrors(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// An address nothing listens on, so every forward fails.
		dead := freePort(t)
		s := serve(t, tgt, "-upstream.url", "http://"+dead+"/graphql",
			"-allowlist", dir, "-upstream.timeout", "1s")

		if code, _ := post(t, s, docAllowed); code != http.StatusBadGateway {
			t.Fatalf("expected 502; received %d", code)
		}

		_, status := control(t, s, http.MethodGet, "/status", "")
		if !strings.Contains(status, `"upstream_errors":1`) {
			t.Errorf("expected /status to report the failure; received %s", status)
		}
		// And the two agree, which is what makes either one usable alone.
		if got := metricValue(t, scrape(t, s),
			"gqlhash_proxy_upstream_errors_total"); got != 1 {
			t.Errorf("expected /metrics to agree; received %v", got)
		}
	})
}

// TestMetricsHistogramBuckets covers the whole bucket set of the duration
// histogram. Matching a `le=` value or two leaves a reimplementation free to
// ship five buckets, giving different quantiles from the same traffic — a
// dashboard that disagrees with itself across a deploy — or to drop +Inf,
// which is no histogram Prometheus can read.
func TestMetricsHistogramBuckets(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		want := []string{
			"0.0001", "0.00025", "0.0005", "0.001", "0.0025", "0.005", "0.01",
			"0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "+Inf",
		}
		exposition := scrape(t, e.server)

		var got []string
		for line := range strings.SplitSeq(exposition, "\n") {
			const prefix = `gqlhash_proxy_request_duration_seconds_bucket{decision="allowed",le="`
			rest, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			value, _, _ := strings.Cut(rest, `"`)
			got = append(got, value)
		}
		if !equalStrings(got, want) {
			t.Errorf("expected the bucket set;\n want %q\n have %q", want, got)
		}
	})
}

// TestMetricsIsAWholeExposition parses the scrape as a document rather than
// searching it for substrings.
//
// A repeated series, or a second # TYPE for a family, passes every
// strings.Contains this suite makes — and makes Prometheus reject the whole scrape,
// which takes the target down and every metric with it.
func TestMetricsIsAWholeExposition(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		// Some traffic, so the counters and the histogram carry values.
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Fatal("expected the request served")
		}
		if code, _ := post(t, e.server, docRejected); code != http.StatusForbidden {
			t.Fatal("expected the request refused")
		}

		types := map[string]int{}
		series := map[string]int{}
		for line := range strings.SplitSeq(scrape(t, e.server), "\n") {
			line = strings.TrimRight(line, "\r")
			switch {
			case line == "":
				continue
			case strings.HasPrefix(line, "# TYPE "):
				name, _, ok := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
				if !ok {
					t.Errorf("a TYPE line naming no type: %q", line)
					continue
				}
				types[name]++
			case strings.HasPrefix(line, "#"):
				continue
			default:
				name, value, ok := strings.Cut(line, " ")
				if !ok {
					t.Errorf("a sample line with no value: %q", line)
					continue
				}
				if strings.TrimSpace(value) == "" {
					t.Errorf("a sample line with an empty value: %q", line)
				}
				series[name]++
			}
		}

		for name, count := range types {
			if count > 1 {
				t.Errorf("expected one TYPE for %s; received %d", name, count)
			}
		}
		for name, count := range series {
			if count > 1 {
				t.Errorf("expected %s once; received %d", name, count)
			}
		}
		if len(series) == 0 {
			t.Error("expected an exposition carrying samples")
		}
		// The families this proxy is asked for, each declared exactly once.
		for _, name := range []string{
			"gqlhash_proxy_requests_total",
			"gqlhash_proxy_upstream_errors_total",
			"gqlhash_proxy_request_duration_seconds",
		} {
			if types[name] != 1 {
				t.Errorf("expected one TYPE line for %s; received %d",
					name, types[name])
			}
		}
	})
}

// TestUpstreamTimeoutAfterTheHeaders covers -upstream.timeout firing once the
// answer has begun: the API sends its headers and a partial body, then stops.
//
// A timeout before the headers is a 504 (TestUpstreamTimeout). After them the
// status is on the wire and can't be taken back, so the truncated-answer rule
// applies instead: no complete-looking answer, and which of "nothing" and "502"
// the client sees is the implementation's, as in TestBrokenUpstreamAnswer.
//
// What both keep: the timeout is an upstream failure and is counted as one.
func TestUpstreamTimeoutAfterTheHeaders(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		url := rawUpstream(t, func(conn net.Conn) {
			_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\n" +
				"Content-Type: application/json\r\n" +
				"Content-Length: 4096\r\n\r\n{\"data\":"))
			time.Sleep(10 * time.Second)
		})
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "500ms")

		client := &http.Client{Timeout: 10 * time.Second}
		defer client.CloseIdleConnections()
		res, err := client.Post("http://"+s.address+"/graphql",
			"application/json", strings.NewReader(docAllowed))
		if err == nil {
			defer func() { _ = res.Body.Close() }()
			body, readErr := io.ReadAll(res.Body)
			if res.StatusCode == http.StatusOK && readErr == nil {
				t.Errorf("expected no whole answer; received 200 and %d bytes",
					len(body))
			}
		}

		// However it ended for the client, the proxy saw an upstream that
		// didn't answer within the flag and says so.
		if got := metricValue(t, scrape(t, s),
			"gqlhash_proxy_upstream_errors_total"); got != 1 {
			t.Errorf("expected one upstream error; received %v", got)
		}
	})
}

// TestUpstreamTimeoutIsTimedAsAllowed covers which histogram series a 504
// lands in.
//
// The proxy decided `allowed` and the API then failed, so that's where it's
// counted and timed. The design calls this a known wart — the fix is an outcome
// label on the histogram rather than a seventh decision — and an unpinned wart
// is one a reimplementation gets to differ on.
func TestUpstreamTimeoutIsTimedAsAllowed(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// An address nothing answers on, so the forward times out before any
		// header arrives and the 504 is the proxy's own.
		url := rawUpstream(t, func(conn net.Conn) { time.Sleep(10 * time.Second) })
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "500ms")

		if code, _ := post(t, s, docAllowed); code != http.StatusGatewayTimeout {
			t.Fatalf("expected 504; received %d", code)
		}

		exposition := scrape(t, s)
		for series, want := range map[string]float64{
			`gqlhash_proxy_requests_total{decision="allowed"}`:                 1,
			`gqlhash_proxy_request_duration_seconds_count{decision="allowed"}`: 1,
			"gqlhash_proxy_upstream_errors_total":                              1,
		} {
			if got := metricValue(t, exposition, series); got != want {
				t.Errorf("%s: expected %v; received %v", series, want, got)
			}
		}
	})
}

// TestAllowlistUnreadableDocument covers a file inside the allowlist that can't be read.
//
// It's a skip like any other, named in the reload answer, and the rest of the
// directory is served. Only the directory-level failure is a failed reload —
// one unreadable file must not take an allowlist out of service.
func TestAllowlistUnreadableDocument(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		writeDoc(t, dir, "b.graphql", rejectedText)
		unreadable := filepath.Join(dir, "b.graphql")
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Skipf("chmod unavailable: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
		if _, err := os.ReadFile(unreadable); err == nil {
			t.Skip("the file is readable anyway; this test needs a non-root user")
		}

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir)

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("expected the reload to succeed; received %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 {
			t.Errorf("expected the readable document served; received %s", body)
		}
		if answer.Skipped.Total != 1 || len(answer.Skipped.Errors) != 1 ||
			!strings.Contains(answer.Skipped.Errors[0], "b.graphql") {
			t.Errorf("expected the unreadable file named as a skip; received %s",
				body)
		}
		if code, _ := post(t, s, docAllowed); code != http.StatusOK {
			t.Errorf("expected the rest of the directory served; received %d", code)
		}
	})
}

// TestDepthLimitAtLoadTime covers the half of -depth-limit that isn't the
// request path: a document on the allowlist that nests past it.
//
// The flag applies to the allowlist and to requests alike, so the two can't disagree.
// Checking requests alone leaves the allowlist holding a document the
// proxy refuses to serve: an entry that can never match anything.
func TestDepthLimitAtLoadTime(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "shallow.graphql", allowedDoc)
		deep := "{" + strings.Repeat("a{", 8) + "a" + strings.Repeat("}", 9)
		writeDoc(t, dir, "deep.graphql", deep)

		api := newAPI(t)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir,
			"-depth-limit", "4")

		code, body := control(t, s, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 {
			t.Errorf("expected only the shallow document loaded; received %s", body)
		}
		if answer.Skipped.Total != 1 || len(answer.Skipped.Errors) != 1 ||
			!strings.Contains(answer.Skipped.Errors[0], "deep.graphql") {
			t.Errorf("expected the deep document skipped by name; received %s",
				body)
		}
	})
}

// TestFlagsThatAreOnlyEverParsed covers flags no test had ever handed a running
// server: they were pinned at startup and never observed serving traffic,
// so a flag accepted and then dropped on the floor passed.
func TestFlagsThatAreOnlyEverParsed(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		for _, tc := range []struct {
			name string
			args []string
		}{
			// -upstream.http2 is refused by fasthttp and taken by
			// the other, so what's shared is that a server given it serves.
			// each() has no "this target speaks HTTP/2" predicate,
			// so that's the whole rule this can hold.
			{"the pool flags", []string{
				"-upstream.max-idle-conns-per-host", "8",
				"-upstream.max-idle-conns", "16",
				"-upstream.max-conn-lifetime", "30s"}},
			{"the log flags", []string{
				"-log.json=false", "-log.requests", "-log.level", "debug"}},
			{"opaque errors and batching together", []string{
				"-opaque-errors", "-max-batch", "8"}},
			{"trust-forwarded", []string{"-trust-forwarded"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newEnv(t, tgt, []string{allowedDoc}, tc.args...)
				if code, answer := post(t, e.server, docAllowed); code != http.StatusOK {
					t.Errorf("expected it serving; received %d: %s", code, answer)
				}
				if code, _ := post(t, e.server, docRejected); code != http.StatusForbidden {
					t.Errorf("expected it still refusing; received %d", code)
				}
				if n := e.api.count(); n != 1 {
					t.Errorf("expected one forward; the API saw %d", n)
				}
			})
		}
	})
}

// TestMaxBodyBelowOneIsRefused covers -server.max-body below 1,
// which every other size and duration flag reads as "no limit" and
// which here is a limit no body can be under.
//
// Refused at startup, where a value nothing can be served under belongs.
// It used to be taken: the proxy started and answered 413 to every POST there is,
// an empty one included, while a GET carrying its document in the query string was
// still served.
func TestMaxBodyBelowOneIsRefused(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		for _, value := range []string{"0", "-1"} {
			t.Run(value, func(t *testing.T) {
				code, _, stderr := run(t, tgt,
					"-upstream.url", "http://api:4000/graphql", "-allowlist", dir,
					"-server.max-body", value)
				if code != exitBadArgs {
					t.Errorf("expected exit %d; received %d: %s",
						exitBadArgs, code, stderr)
				}
				if !strings.Contains(stderr, "-server.max-body must be 1 or more") {
					t.Errorf("expected the reason named; received %s", stderr)
				}
			})
		}
	})
}

// TestUpstreamURLScheme covers what -upstream.url may name.
//
// A scheme that is neither http nor https is refused at startup, which is where
// a value nobody can forward over belongs. Accepted, it would be read as one of
// them anyway and differently by each implementation: an ftp:// upstream is a 502
// under one and a served request under the other.
func TestUpstreamURLScheme(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		for _, tc := range []struct{ upstream, reason string }{
			{"ftp://api:4000/graphql", "http or https"},
			{"ws://api:4000/graphql", "http or https"},
			// A scheme carrying no host is refused a step earlier,
			// for having no host: both are the same exit, and each says which it was.
			{"file:///etc/passwd", "no absolute URL"},
		} {
			t.Run(tc.upstream, func(t *testing.T) {
				code, _, stderr := run(t, tgt,
					"-upstream.url", tc.upstream, "-allowlist", dir)
				if code != exitBadArgs {
					t.Errorf("expected exit %d; received %d: %s",
						exitBadArgs, code, stderr)
				}
				if !strings.Contains(stderr, tc.reason) {
					t.Errorf("expected %q; received %s", tc.reason, stderr)
				}
			})
		}
	})
}

// TestMethodsTheHTTPLayerAnswersItself covers two request targets that never
// reach a GraphQL endpoint: the asterisk-form OPTIONS and the authority-form
// CONNECT of a client that thinks this is a forward proxy.
//
// The two commands answer them differently — one above its mux, the other in
// its parser — which is the wire-refusal difference again.
// What both keep is that neither is a way to reach the API.
func TestMethodsTheHTTPLayerAnswersItself(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, tc := range []struct{ name, request string }{
			{"OPTIONS *", "OPTIONS * HTTP/1.1\r\nHost: x\r\n\r\n"},
			{"CONNECT in authority form",
				"CONNECT api.example.com:443 HTTP/1.1\r\nHost: x\r\n\r\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				status := raw(t, e.address, tc.request)
				if strings.HasPrefix(status, "HTTP/1.1 2") &&
					!strings.HasPrefix(status, "HTTP/1.1 200 OK") {
					t.Errorf("unexpected answer %q", status)
				}
			})
		}
		// Whatever each answered, the API saw none of it.
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing forwarded; the API saw %d", n)
		}
	})
}
