package acceptance

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romshark/gqlhash/v2/internal/gqlhashtest"
)

// TestForwarding is the decision the proxy exists to make: a document on the
// allowlist reaches the API, and the bytes the client sent are the bytes the
// API receives.
func TestForwarding(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// The allowed document, however it's written: what's hashed is the
		// document, not its formatting.
		for _, body := range []string{
			docAllowed,
			`{"query":"query GetUser {\n  user(id: 1) {\n    name\n  }\n}"}`,
			`{"query":"query GetUser{ # comment\n user(id:1){name}}"}`,
			`{"operationName":"GetUser","query":"` + allowedText + `",` +
				`"variables":{"x":[1,2,3]}}`,
		} {
			code, answer := post(t, e.server, body)
			if code != http.StatusOK {
				t.Errorf("expected 200; received %d; body: %s", code, body)
				continue
			}
			if answer != upstreamAnswer {
				t.Errorf("expected the upstream answer; received %q", answer)
			}
			got := e.api.last(t)
			// The body upstream receives is the one the client sent.
			if got.body != body {
				t.Errorf("expected upstream to receive %s; received %s", body, got.body)
			}
			// The path of -upstream.url is the endpoint, whatever the client used.
			if got.path != "/graphql" {
				t.Errorf("expected /graphql upstream; received %s", got.path)
			}
		}

		// A GET carries the document in the query string.
		if code, body := get(
			t, e.server, "query="+url.QueryEscape(allowedText),
		); code != http.StatusOK {
			t.Fatalf("GET: expected 200; received %d: %s", code, body)
		}
	})
}

// TestRejected covers a document that isn't on the list, including the ones
// that differ from an allowed document by the least a document can differ by.
func TestRejected(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, body := range []string{
			docRejected,
			// One field more, and the anonymous form of the allowed operation:
			// each is another document.
			`{"query":"query GetUser{user(id:1){name id}}"}`,
			`{"query":"{user(id:1){name}}"}`,
			`{"query":"mutation{deleteEverything}"}`,
			// A document that doesn't parse is rejected as not allowed, not as
			// malformed: the JSON is fine, the document just isn't on the list.
			`{"query":"{ broken"}`,
		} {
			code, answer := post(t, e.server, body)
			if code != http.StatusForbidden {
				t.Errorf("expected 403; received %d; body: %s", code, body)
			}
			if !strings.Contains(answer, "OPERATION_NOT_ALLOWED") ||
				!strings.Contains(answer, `"errors"`) {
				t.Errorf("expected a GraphQL error body; received %s", answer)
			}
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("a rejected request must not reach upstream; received %d", n)
		}
	})
}

// TestRejectsUnparsableDocuments feeds the running proxy every document the
// parser refuses. None of them can be on an allowlist, so each is a rejection
// and none of them reaches the API.
//
// Which error each produces is the parser's own contract, covered where the
// error is visible; here they collapse into one answer, and what's under test
// is that the server makes it for all of them rather than for the sample in
// [TestRejected]. The documents come from [gqlhashtest], which is fixtures.
func TestRejectsUnparsableDocuments(t *testing.T) {
	documents := slices.Concat(gqlhashtest.UnexpectedEOF, gqlhashtest.UnexpectedToken)

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, document := range documents {
			body, err := jsonRequest(document)
			if err != nil {
				t.Fatal(err)
			}
			code, answer := post(t, e.server, body)
			if code != http.StatusForbidden ||
				!strings.Contains(answer, "OPERATION_NOT_ALLOWED") {
				t.Errorf("%q: expected 403 and a GraphQL error; received %d: %s",
					document, code, answer)
			}
		}

		if n := e.api.count(); n != 0 {
			t.Errorf("none of them may reach upstream; received %d", n)
		}
	})
}

// TestMalformed covers a request carrying no document to look up, which is a
// different answer from one carrying a document that isn't allowed.
func TestMalformed(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, body := range []string{
			`not json`, `{"variables":{}}`, `{"query":`, `{"query":null}`,
			`{"query":42}`, `{}`, ``,
		} {
			code, answer := post(t, e.server, body)
			if code != http.StatusBadRequest {
				t.Errorf("expected 400; received %d; body: %q", code, body)
			}
			if !strings.Contains(answer, "BAD_REQUEST") {
				t.Errorf("expected the error shape; received %s", answer)
			}
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("nothing must reach upstream; received %d", n)
		}
	})
}

// TestContentTypes covers where the document is: an application/graphql body is
// the document itself, a JSON body carries it in a member, and a GET in the
// query string.
func TestContentTypes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		graphql := func(t *testing.T, contentType, document string) int {
			t.Helper()
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.address+"/graphql", strings.NewReader(document))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", contentType)
			code, _ := send(t, req)
			return code
		}

		if code := graphql(t, "application/graphql", allowedText); code !=
			http.StatusOK {
			t.Errorf("application/graphql: expected 200; received %d", code)
		}
		if code := graphql(
			t, "application/graphql; charset=utf-8", rejectedText,
		); code != http.StatusForbidden {
			t.Errorf("application/graphql: expected 403; received %d", code)
		}
		if code, _ := get(
			t, e.server, "query="+url.QueryEscape(rejectedText),
		); code != http.StatusForbidden {
			t.Errorf("GET: expected 403; received %d", code)
		}
	})
}

// TestMaxBody covers -server.max-body, which bounds what a request costs before
// there's any reason to think it carries a document at all.
func TestMaxBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-server.max-body", "128")

		body := `{"query":"` + allowedText + `","padding":"` +
			strings.Repeat("x", 512) + `"}`
		if code, answer := post(t, e.server, body); code !=
			http.StatusRequestEntityTooLarge {
			t.Errorf("expected 413; received %d: %s", code, answer)
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("an oversized request must not reach upstream; received %d", n)
		}

		// A body with no declared length is bounded too, so a chunked request
		// can't spend more than the flag allows.
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1
		if code, answer := send(t, req); code != http.StatusRequestEntityTooLarge {
			t.Errorf("unknown length: expected 413; received %d: %s", code, answer)
		}
	})
}

// TestOpaqueErrors makes sure -opaque-errors withholds the same detail
// everywhere: what a refusal says is a rule of its own, not a property of the
// server carrying it.
func TestOpaqueErrors(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-opaque-errors")

		// Malformed and not-allowed become the same answer.
		for _, body := range []string{`{"query":`, docRejected} {
			code, answer := post(t, e.server, body)
			if code != http.StatusForbidden {
				t.Errorf("expected 403 for everything; received %d: %s", code, answer)
			}
			if !strings.Contains(answer, "OPERATION_NOT_ALLOWED") ||
				strings.Contains(answer, "BAD_REQUEST") {
				t.Errorf("expected no detail; received %s", answer)
			}
		}
	})
}

// TestBatch covers -max-batch: an array of requests is refused outright
// without it, and every document of one has to be allowed with it.
func TestBatch(t *testing.T) {
	const batch = `[{"query":"` + allowedText + `"},{"query":"{ b }"}]`

	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc, "{ b }"})
		if code, body := post(t, e.server, batch); code != http.StatusBadRequest {
			t.Errorf("expected 400 without -max-batch; received %d: %s", code, body)
		}

		allowed := newEnv(t, tgt, []string{allowedDoc, "{ b }"}, "-max-batch", "8")
		if code, body := post(t, allowed.server, batch); code != http.StatusOK {
			t.Errorf("expected 200; received %d: %s", code, body)
		}
		// The batch is one request upstream, as it arrived.
		if n := allowed.api.count(); n != 1 {
			t.Errorf("expected one forwarded request; received %d", n)
		}

		// One document of the batch is enough to reject the whole batch.
		partly := newEnv(t, tgt, []string{allowedDoc}, "-max-batch", "8")
		if code, body := post(t, partly.server, batch); code != http.StatusForbidden {
			t.Errorf("expected 403; received %d: %s", code, body)
		}
		if n := partly.api.count(); n != 0 {
			t.Error("a partly allowed batch must not reach upstream")
		}
	})
}

// TestMaxBatch covers the cap -max-batch puts on a batch: a request carrying
// more documents than it allows is refused whole, counted apart from every other
// refusal, and nothing of it reaches the API.
//
// The cap is what makes batching bounded. One request holds as many operations as
// -server.max-body has room for — tens of thousands of them at the default —
// so without a count the API's work per allowed request has no limit,
// and one request is all a dashboard sees.
func TestMaxBatch(t *testing.T) {
	// batchOf returns a batch of n allowed documents.
	batchOf := func(n int) string {
		one := `{"query":"` + allowedText + `"}`
		elements := make([]string, n)
		for i := range elements {
			elements[i] = one
		}
		return "[" + strings.Join(elements, ",") + "]"
	}

	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-max-batch", "3")

		// At the cap and under it, every document allowed.
		for _, n := range []int{1, 2, 3} {
			if code, body := post(t, e.server, batchOf(n)); code != http.StatusOK {
				t.Errorf("expected a batch of %d served; received %d: %s",
					n, code, body)
			}
		}
		if n := e.api.count(); n != 3 {
			t.Errorf("expected three forwarded requests; received %d", n)
		}

		// One past it, and far past it, refused with a reason of its own.
		for _, n := range []int{4, 1000} {
			code, body := post(t, e.server, batchOf(n))
			if code != http.StatusRequestEntityTooLarge {
				t.Errorf("expected a batch of %d refused; received %d: %s",
					n, code, body)
			}
			if !strings.Contains(body, "BATCH_TOO_LARGE") {
				t.Errorf("expected a batch error body; received %s", body)
			}
		}
		if n := e.api.count(); n != 3 {
			t.Errorf("a batch past the cap must not reach upstream; the API saw %d", n)
		}

		// Counted as its own decision rather than as a malformed request.
		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if !strings.Contains(exposition,
			`gqlhash_proxy_requests_total{decision="batch_too_large"} 2`) {
			t.Errorf("expected two counted batch_too_large; received %s", exposition)
		}

		// A cap of 1 takes a batch of one: an array is still a batch,
		// and one document in it is one operation.
		one := newEnv(t, tgt, []string{allowedDoc}, "-max-batch", "1")
		if code, body := post(t, one.server, batchOf(1)); code != http.StatusOK {
			t.Errorf("expected a batch of one served; received %d: %s", code, body)
		}
		if code, body := post(t, one.server, batchOf(2)); code !=
			http.StatusRequestEntityTooLarge {
			t.Errorf("expected a batch of two refused; received %d: %s", code, body)
		}

		// A lone request object is one document whatever the cap says,
		// so -max-batch never stands between a client and an ordinary request.
		if code, body := post(t, one.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected an ordinary request served; received %d: %s", code, body)
		}
	})
}

// TestNamedOperations covers documents holding more than one named operation,
// where the request picks which to run with operationName.
//
// What's allowed is the document, so the pick is the API's to resolve. That
// cuts both ways, and both ways are what this covers: every operation of an
// allowed file is allowed, and an allowed operation carries nothing else in
// with it, since the document it arrived in is what was hashed.
func TestNamedOperations(t *testing.T) {
	const (
		getUser  = "query GetUser {\n  user { name }\n}"
		getPosts = "query GetPosts {\n  posts { title }\n}"
		destroy  = "mutation Destroy {\n  deleteEverything\n}"
	)
	body := func(name, document string) string {
		return `{"operationName":` + strconv.Quote(name) +
			`,"query":` + strconv.Quote(document) + `}`
	}

	each(t, func(t *testing.T, tgt target) {
		// The allowed file holds two operations, and a request runs one of them.
		t.Run("an operation of an allowed file", func(t *testing.T) {
			both := getUser + "\n\n" + getPosts
			e := shared(t, tgt)
			e.allow(t, both)

			for _, name := range []string{"GetUser", "GetPosts", ""} {
				code, answer := post(t, e.server, body(name, both))
				if code != http.StatusOK {
					t.Errorf("operationName %q: expected 200; received %d: %s",
						name, code, answer)
					continue
				}
				// The name is forwarded untouched, since resolving it is the
				// API's job. An empty one against a document of two operations
				// is an error there, which the proxy has no opinion about.
				got := e.api.last(t)
				if !strings.Contains(got.body, `"operationName":`+strconv.Quote(name)) {
					t.Errorf("operationName %q: expected it forwarded; received %s",
						name, got.body)
				}
			}

			// Only the operation the client wants, lifted out of the allowed file.
			if code, _ := post(t, e.server, body("GetUser", getUser)); code !=
				http.StatusForbidden {
				t.Errorf("expected one operation on its own to be refused; received %d",
					code)
			}
			// The same two the other way round is another document too.
			if code, _ := post(
				t, e.server, body("GetUser", getPosts+"\n\n"+getUser),
			); code != http.StatusForbidden {
				t.Errorf("expected the reordered document to be refused; received %d",
					code)
			}
		})

		// The other direction, and the one that matters: the request selects an
		// operation that is allowed on its own, and carries one that isn't.
		t.Run("an operation smuggled in beside an allowed one", func(t *testing.T) {
			e := shared(t, tgt)
			e.allow(t, getUser)

			if code, answer := post(t, e.server, body("GetUser", getUser)); code !=
				http.StatusOK {
				t.Fatalf("expected the allowed operation on its own; received %d: %s",
					code, answer)
			}

			// Selecting GetUser doesn't make the document GetUser: what was
			// hashed is everything the request carried, so the mutation rides in
			// with it or the request is refused. It's refused.
			for _, document := range []string{
				getUser + "\n\n" + destroy,
				destroy + "\n\n" + getUser,
			} {
				code, answer := post(t, e.server, body("GetUser", document))
				if code != http.StatusForbidden {
					t.Errorf("expected the smuggled operation to be refused; "+
						"received %d: %s", code, answer)
				}
			}
			if n := e.api.count(); n != 1 {
				t.Errorf("expected only the allowed request forwarded; received %d", n)
			}
		})
	})
}

// TestDepthLimit covers the depth limit at the proxy: a request nesting past it
// is refused when it's hashed, which is what keeps a nesting attack from
// costing the API anything. It's a rejection and not a malformed request: the
// JSON is fine, the document just isn't something the proxy will hash.
func TestDepthLimit(t *testing.T) {
	// nested returns a document whose selection sets nest depth deep.
	nested := func(depth int) string {
		return "{" + strings.Repeat("f{", depth-1) + "f" + strings.Repeat("}", depth)
	}
	// The default of -depth-limit, which is what a run that doesn't set the
	// flag applies. TestDepthLimitFlag covers setting it.
	const limit = 128

	each(t, func(t *testing.T, tgt target) {
		atLimit := nested(limit)
		e := shared(t, tgt)
		e.allow(t, atLimit)

		// A document at the limit is deep, not too deep: it's allowed like any
		// other, so nesting on its own isn't what rejects the next one.
		if code, answer := post(
			t, e.server, `{"query":`+strconv.Quote(atLimit)+`}`,
		); code != http.StatusOK {
			t.Fatalf("expected the document at the limit to be allowed; %d: %s",
				code, answer)
		}

		code, answer := post(t, e.server,
			`{"query":`+strconv.Quote(nested(limit+1))+`}`)
		if code != http.StatusForbidden {
			t.Errorf("expected 403; received %d: %s", code, answer)
		}
		if !strings.Contains(answer, "OPERATION_NOT_ALLOWED") {
			t.Errorf("expected a GraphQL error body; received %s", answer)
		}
		if n := e.api.count(); n != 1 {
			t.Errorf("expected only the request at the limit forwarded; received %d", n)
		}
	})
}

// TestUpstreamDown covers an API that can't be reached: the request was
// allowed, so what failed is the forwarding, and it's answered and counted as
// that rather than as a rejection.
func TestUpstreamDown(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// A port nothing listens on.
		s := serve(t, tgt, "-upstream.url", "http://127.0.0.1:1/graphql",
			"-allowlist", dir)

		code, answer := post(t, s, docAllowed)
		if code != http.StatusBadGateway {
			t.Fatalf("expected 502; received %d: %s", code, answer)
		}
		if !strings.Contains(answer, "UPSTREAM_UNAVAILABLE") {
			t.Errorf("expected an upstream error body; received %s", answer)
		}

		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		if !strings.Contains(exposition, "gqlhash_proxy_upstream_errors_total 1") {
			t.Errorf("expected an upstream error to be counted; received %s", exposition)
		}
		// The decision was to allow it, so that's the label of its duration.
		if !strings.Contains(exposition,
			`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`) {
			t.Errorf("expected the request to be timed as allowed; received %s",
				exposition)
		}
	})
}

// TestForwardedHeaders covers the headers a proxy is trusted to get right.
// Without -trust-forwarded none of them may pass on what a client claimed about
// where it came from; with it the chain of a balancer in front is kept and the
// peer appended to it.
func TestForwardedHeaders(t *testing.T) {
	claim := func(t *testing.T, e *env) http.Header {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		// What a client would claim if it could.
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		req.Header.Set("X-Forwarded-Host", "api.example.com")
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("Forwarded", "for=203.0.113.9;proto=https")
		if code, body := send(t, req); code != http.StatusOK {
			t.Fatalf("expected 200; received %d: %s", code, body)
		}
		return e.api.last(t).header
	}

	each(t, func(t *testing.T, tgt target) {
		t.Run("without trust", func(t *testing.T) {
			e := shared(t, tgt)
			e.allow(t, allowedDoc)
			got := claim(t, e)

			// The client is the direct peer, whatever it claims.
			if v := got.Get("X-Forwarded-For"); strings.Contains(v, "203.0.113.9") {
				t.Errorf("expected the claimed chain to be dropped; received %q", v)
			}
			if got.Get("X-Forwarded-For") == "" {
				t.Error("expected the peer to be reported as X-Forwarded-For")
			}
			if got.Get("X-Forwarded-Host") == "api.example.com" {
				t.Error("expected the claimed host to be replaced")
			}
			if got.Get("X-Forwarded-Host") == "" {
				t.Error("expected X-Forwarded-Host to be set")
			}
			if v := got.Get("X-Forwarded-Proto"); v != "http" {
				t.Errorf("expected the proto the proxy was reached over; received %q", v)
			}
			// An RFC 7239 header of the client must not reach upstream unchecked.
			if v := got.Get("Forwarded"); v != "" {
				t.Errorf("expected Forwarded to be dropped; received %q", v)
			}
			if v := got.Get("Keep-Alive"); v != "" {
				t.Errorf("expected Keep-Alive not to be forwarded; received %q", v)
			}
		})

		t.Run("with trust", func(t *testing.T) {
			got := claim(t, newEnv(t, tgt, []string{allowedDoc}, "-trust-forwarded"))

			// The chain of the balancer is kept and the peer appended to it.
			if chain := got.Get("X-Forwarded-For"); !strings.HasPrefix(
				chain, "203.0.113.9, ",
			) {
				t.Errorf("expected the chain plus the peer; received %q", chain)
			}
			if v := got.Get("X-Forwarded-Host"); v != "api.example.com" {
				t.Errorf("expected the forwarded host; received %q", v)
			}
			if v := got.Get("X-Forwarded-Proto"); v != "https" {
				t.Errorf("expected the forwarded protocol; received %q", v)
			}
		})

		t.Run("with trust and nothing to keep", func(t *testing.T) {
			e := newEnv(t, tgt, []string{allowedDoc}, "-trust-forwarded")
			if code, body := post(t, e.server, docAllowed); code != http.StatusOK {
				t.Fatalf("expected 200; received %d: %s", code, body)
			}
			// The peer is the whole chain, and it's where the request came from
			// rather than anything a client said.
			if v := e.api.last(t).header.Get("X-Forwarded-For"); v != "127.0.0.1" {
				t.Errorf("expected the peer alone; received %q", v)
			}
		})
	})
}

// TestAmbiguousFraming sends the framing a request smuggler would. A request
// whose length is open to two readings has to be refused, and nothing of it may
// reach the upstream.
func TestAmbiguousFraming(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		length := strconv.Itoa(len(docAllowed))

		for _, tc := range []struct{ name, request string }{
			{"content-length and transfer-encoding",
				"POST /graphql HTTP/1.1\r\nHost: x\r\n" +
					"Content-Type: application/json\r\n" +
					"Content-Length: " + length + "\r\n" +
					"Transfer-Encoding: chunked\r\n\r\n0\r\n\r\n"},
			{"two conflicting content-length",
				"POST /graphql HTTP/1.1\r\nHost: x\r\n" +
					"Content-Type: application/json\r\n" +
					"Content-Length: " + length + "\r\n" +
					"Content-Length: 4\r\n\r\n" + docAllowed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				before := e.api.count()
				status := raw(t, e.address, tc.request)
				// A refusal, or no answer at all where the connection was
				// dropped. What must not happen is the request being served.
				if strings.HasPrefix(status, "HTTP/1.1 2") {
					t.Errorf("expected ambiguous framing to be refused; "+
						"received %q", status)
				}
				if after := e.api.count(); after != before {
					t.Error("expected nothing to reach the upstream")
				}
			})
		}
	})
}

// TestDuplicateQueryParam covers a GET carrying the query parameter twice.
//
// The proxy reads the first and an API may read the last, so forwarding one it
// checked while the API runs the other is how an unchecked document gets
// executed. It answers rather than choosing.
func TestDuplicateQueryParam(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		allowed := url.QueryEscape(allowedText)
		evil := url.QueryEscape(rejectedText)

		// The allowed document on its own goes through.
		if code, body := get(t, e.server, "query="+allowed); code != http.StatusOK {
			t.Fatalf("expected the allowed document; received %d: %s", code, body)
		}

		// The allowed document first and another after it, either separator.
		// ';' ends a pair here and doesn't for net/url, so a Go API sees no
		// query where one is used: reading it the wider way is the safe half.
		for _, rawQuery := range []string{
			"query=" + allowed + "&query=" + evil,
			"query=" + allowed + ";query=" + evil,
			"query=" + evil + "&query=" + allowed,
		} {
			code, body := get(t, e.server, rawQuery)
			if code != http.StatusBadRequest {
				t.Errorf("%s: expected 400; received %d: %s", rawQuery, code, body)
			}
			if !strings.Contains(body, "duplicate query parameter") {
				t.Errorf("%s: expected the reason; received %s", rawQuery, body)
			}
		}

		if n := e.api.count(); n != 1 {
			t.Errorf("expected only the single-document request forwarded; received %d",
				n)
		}
	})
}

// TestQueryKeyCollision is the rule that a request names the document once and
// once only. Two members whose names match once unescaped are a collision,
// whatever they carry: which the API runs is the decoder's business,
// so the request is refused rather than one picked and a body forwarded that an API
// may read the other way round.
//
// The rule TestDuplicateQueryParam holds a GET to, and what closes the bypass
// an escaped key opens: "query" beside "quer\u0079" is one name written twice,
// so neither case nor escape smuggles a second document past the hashed one.
func TestQueryKeyCollision(t *testing.T) {
	// The member name written four ways that name it: plain, the y as \u0079,
	// the q as \u0051, which unescapes to another case, and that case itself.
	const (
		plain = `"query"`
		escY  = `"quer\u0079"`
		escQ  = `"\u0051uery"`
		upper = `"QUERY"`
	)

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// One name, however it's written, is a request like any other.
		for _, name := range []string{plain, escY, escQ, upper} {
			body := `{` + name + `:"` + allowedText + `"}`
			if code, answer := post(t, e.server, body); code != http.StatusOK {
				t.Errorf("%s: expected 200; received %d: %s", body, code, answer)
			}
		}
		// The same of a GET, where percent encoding is what the escape is in a
		// body: net/url decodes a parameter name before it matches it,
		// so this is the query to the API and has to be one here.
		if code, answer := get(
			t, e.server, "quer%79="+url.QueryEscape(allowedText),
		); code != http.StatusOK {
			t.Errorf("quer%%79: expected 200; received %d: %s", code, answer)
		}
		forwarded := e.api.count()

		// Two of them collide. The documents don't decide it: an allowed one
		// beside an allowed one is refused the same as one that isn't on the
		// list, since what's wrong is the request naming the document twice.
		for _, names := range [][2]string{
			{plain, escY}, {escY, plain}, {plain, escQ}, {plain, upper},
			{plain, plain}, {escY, escQ},
		} {
			for _, second := range []string{allowedText, rejectedText} {
				body := `{` + names[0] + `:"` + allowedText + `",` +
					names[1] + `:"` + second + `"}`
				code, answer := post(t, e.server, body)
				if code != http.StatusBadRequest {
					t.Errorf("%s: expected 400; received %d: %s", body, code, answer)
					continue
				}
				// The answer names what was wrong with the request, which is
				// the collision and not the document. It's read rather than searched:
				// the message is the one error of this proxy carrying a quote of its own,
				// so a client gets it as JSON or not at all.
				var refusal struct {
					Errors []struct {
						Message    string `json:"message"`
						Extensions struct {
							Code string `json:"code"`
						} `json:"extensions"`
					} `json:"errors"`
				}
				if err := json.Unmarshal([]byte(answer), &refusal); err != nil {
					t.Errorf("%s: answering no JSON: %v: %s", body, err, answer)
					continue
				}
				if len(refusal.Errors) != 1 ||
					refusal.Errors[0].Extensions.Code != "BAD_REQUEST" ||
					!strings.Contains(refusal.Errors[0].Message, "collision") {
					t.Errorf("%s: expected the collision named; received %s",
						body, answer)
				}
			}
		}

		// A GET names it in the query string, where percent encoding is what
		// the escape is in a body.
		for _, rawQuery := range []string{
			"query=" + url.QueryEscape(allowedText) +
				"&quer%79=" + url.QueryEscape(allowedText),
			"quer%79=" + url.QueryEscape(rejectedText) +
				"&query=" + url.QueryEscape(allowedText),
		} {
			if code, answer := get(t, e.server, rawQuery); code !=
				http.StatusBadRequest {
				t.Errorf("%s: expected 400; received %d: %s", rawQuery, code, answer)
			}
		}

		// A refused request is one the API never sees.
		if n := e.api.count(); n != forwarded {
			t.Errorf("expected nothing past the collisions; upstream saw %d of %d",
				n, forwarded)
		}
	})
}

// TestConnRecycling makes sure -upstream.max-conn-lifetime turns over the
// upstream pool, which is what lets a name standing for several backends reach
// one added after the pool filled.
//
// It counts connections rather than requests: the point of the flag is that the
// same traffic arrives over more of them.
func TestConnRecycling(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		// An upstream whose listener counts what it accepts.
		var conns atomic.Int64
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle("/graphql", &spy{})
		upstream := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
		go func() {
			_ = upstream.Serve(&countingListener{Listener: listener, conns: &conns})
		}()
		t.Cleanup(func() { _ = upstream.Close() })

		s := serve(t, tgt,
			"-upstream.url", "http://"+listener.Addr().String()+"/graphql",
			"-allowlist", dir,
			"-upstream.max-conn-lifetime", "100ms",
		)

		// Enough requests spread over enough time for the lifetime to fall due
		// more than once.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if code, body := post(t, s, docAllowed); code != http.StatusOK {
				t.Fatalf("expected 200; received %d: %s", code, body)
			}
			time.Sleep(20 * time.Millisecond)
		}

		// Without recycling one connection carries all of it. With it, the pool
		// turns over, so the upstream sees several.
		if n := conns.Load(); n < 2 {
			t.Errorf("expected the pool to turn over; the upstream accepted %d "+
				"connection(s)", n)
		}
	})
}

type countingListener struct {
	net.Listener
	conns *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.conns.Add(1)
	}
	return conn, err
}
