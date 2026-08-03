package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestControlStatus covers /status: what the allowlist holds and what the proxy
// decided, in a shape a deployment can read.
func TestControlStatus(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		if code, body := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Fatalf("expected the allowed document; received %d: %s", code, body)
		}
		for range 2 {
			if code, body := post(t, e.server, docRejected); code !=
				http.StatusForbidden {
				t.Fatalf("expected 403; received %d: %s", code, body)
			}
		}
		if code, body := post(t, e.server, "not json"); code != http.StatusBadRequest {
			t.Fatalf("expected 400; received %d: %s", code, body)
		}

		code, body := control(t, e.server, http.MethodGet, "/status", "")
		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d: %s", code, body)
		}
		var status struct {
			Documents int    `json:"documents"`
			LoadedAt  string `json:"loaded_at"`
			Allowed   int    `json:"allowed"`
			Rejected  int    `json:"rejected"`
			Malformed int    `json:"malformed"`
			TooLarge  int    `json:"too_large"`
			Ambiguous int    `json:"ambiguous"`
			TooDeep   int    `json:"too_deep"`
			BatchBig  int    `json:"batch_too_large"`
			Upstream  int    `json:"upstream_errors"`
		}
		if err := json.Unmarshal([]byte(body), &status); err != nil {
			t.Fatalf("answering no JSON: %v: %s", err, body)
		}
		if status.Documents != 1 || status.Allowed != 1 || status.Rejected != 2 ||
			status.Malformed != 1 || status.TooLarge != 0 ||
			status.Ambiguous != 0 || status.TooDeep != 0 || status.BatchBig != 0 ||
			status.Upstream != 0 {
			t.Errorf("unexpected status: %+v", status)
		}
		if status.LoadedAt == "" {
			t.Error("expected a load time")
		}
	})
}

// TestControlOnlyOnControlAddress pins that the control endpoints live on the
// control address alone. The data-plane port routes nothing but the proxy,
// so a request to one of their paths is read as a GraphQL request and answered as
// one, whatever the path says.
func TestControlOnlyOnControlAddress(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// An empty body is no GraphQL request, so the proxy answers 400 on every
		// one of these paths. A route would answer something else.
		for _, path := range []string{"/reload", "/metrics", "/status"} {
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.address+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			code, body := send(t, req)
			if code != http.StatusBadRequest {
				t.Errorf("%s: expected 400 from the proxy; received %d: %s",
					path, code, body)
			}
			if !strings.Contains(body, `"code":"BAD_REQUEST"`) {
				t.Errorf("%s: expected the answer of the proxy; received %s", path, body)
			}
		}

		// The same paths answer on the control address, which is what makes the
		// point: they exist, just not there.
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/status"},
			{http.MethodGet, "/metrics"},
			{http.MethodPost, "/reload"},
		} {
			if code, body := control(
				t, e.server, c.method, c.path, "",
			); code != http.StatusOK {
				t.Errorf("%s on the control address: expected 200; received %d: %s",
					c.path, code, body)
			}
		}

		// Nothing else is served there.
		if code, _ := control(
			t, e.server, http.MethodGet, "/graphql", "",
		); code != http.StatusNotFound {
			t.Errorf("expected 404 outside the control endpoints; received %d", code)
		}

		// None of it reached the API.
		if n := e.api.count(); n != 0 {
			t.Errorf("expected nothing to be forwarded; received %d", n)
		}
	})
}

// reloadAnswer is what a reload replies, read rather than searched:
// a deployment fails on these numbers, so their shape is part of the contract.
// reloadAnswerShape is the shape of that answer, which a test reads by field.
type reloadAnswerShape struct {
	Documents struct {
		Total int      `json:"total"`
		Files []string `json:"files"`
	} `json:"documents"`
	Skipped struct {
		Total  int      `json:"total"`
		Errors []string `json:"errors"`
	} `json:"skipped"`
}

func reloadAnswer(t *testing.T, body string) (answer reloadAnswerShape) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("answering no JSON: %v: %s", err, body)
	}
	return answer
}

// TestControlReload covers a reload end to end: files on disk,
// a running proxy and the request that publishes them.
func TestControlReload(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		const added = `{"query":"{new}"}`

		// A document on disk waits for a reload.
		writeDoc(t, e.dir, "new.graphql", "{ new }")
		if code, _ := post(t, e.server, added); code != http.StatusForbidden {
			t.Errorf("expected 403 before the reload; received %d", code)
		}

		// A reload takes POST, so a browser or a scraper that wanders onto the
		// address can't spend the work of one. The answer says which method
		// does, in the body and in the header a client reads for it.
		code, body, header := controlFor(t, e.server, http.MethodGet, "/reload", "")
		if code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for GET; received %d", code)
		}
		if !strings.Contains(body, "reload takes POST") {
			t.Errorf("expected the method named; received %s", body)
		}
		if got := header.Get("Allow"); got != http.MethodPost {
			t.Errorf("expected Allow: POST; received %q", got)
		}

		code, body = control(t, e.server, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("reload: received %d: %s", code, body)
		}
		// A reload that took everything says so:
		// both files, and a skipped block that is empty rather than absent.
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 2 || len(answer.Documents.Files) != 2 {
			t.Errorf("expected the two loaded files; received %s", body)
		}
		if !strings.Contains(body, "new.graphql") || !strings.Contains(body, "a.graphql") {
			t.Errorf("expected both files named; received %s", body)
		}
		if answer.Skipped.Total != 0 || len(answer.Skipped.Errors) != 0 {
			t.Errorf("expected nothing skipped; received %s", body)
		}

		// The list the reload published is the one being served:
		// what it added, and what it kept.
		for _, request := range []string{added, docAllowed} {
			if code, answer := post(t, e.server, request); code != http.StatusOK {
				t.Errorf("%s: expected it allowed after the reload; received %d: %s",
					request, code, answer)
			}
		}

		// A removed document stops being allowed once reloaded.
		if err := os.Remove(e.dir + "/a.graphql"); err != nil {
			t.Fatal(err)
		}
		if code, body := control(
			t, e.server, http.MethodPost, "/reload", "",
		); code != http.StatusOK {
			t.Fatalf("reload: received %d: %s", code, body)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected the removed document to be rejected; received %d", code)
		}
	})
}

// TestControlReloadReportsSkipped pins that a document left out is named in the
// answer, so a deployment fails on it without reading the log.
// The reload itself worked, so it's a 200 that carries the bad news,
// and what parsed keeps being served.
func TestControlReloadReportsSkipped(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		writeDoc(t, e.dir, "broken.graphql", "query Q {\n  f(a: 01)\n}")

		code, body := control(t, e.server, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d %s", code, body)
		}
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 || answer.Skipped.Total != 1 {
			t.Errorf("expected 1 document and 1 skip; received %+v", answer)
		}
		if len(answer.Documents.Files) != 1 ||
			!strings.HasSuffix(answer.Documents.Files[0], "a.graphql") {
			t.Errorf("expected the loaded file to be named; received %v",
				answer.Documents.Files)
		}
		// The position of the error, not just the file.
		if len(answer.Skipped.Errors) != 1 ||
			!strings.Contains(answer.Skipped.Errors[0], "broken.graphql:2:9") {
			t.Errorf("expected the position of the error; received %v",
				answer.Skipped.Errors)
		}

		// What parsed is still served.
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected the working document to stay allowed; received %d", code)
		}
	})
}

// TestControlReloadEmptyDirectory pins the shape of the answer where a reload
// took nothing: an empty list of files, not a null. A client that reads the
// answer to fail a deployment shouldn't need a case for the empty directory,
// and JSON tells the two apart even though Go's zero value doesn't.
func TestControlReloadEmptyDirectory(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t)

		code, body := control(t, e.server, http.MethodPost, "/reload", "")
		if code != http.StatusOK {
			t.Fatalf("expected 200; received %d %s", code, body)
		}
		if !strings.Contains(body, `"total":0,"files":[]`) {
			t.Errorf("expected an empty list of files; received %s", body)
		}
		var answer struct {
			Documents struct {
				Total int       `json:"total"`
				Files *[]string `json:"files"`
			} `json:"documents"`
		}
		if err := json.Unmarshal([]byte(body), &answer); err != nil {
			t.Fatalf("answering no JSON: %v: %s", err, body)
		}
		if answer.Documents.Files == nil {
			t.Error("expected files to be a list; received null")
		}

		// An empty allowlist rejects everything rather than allowing it.
		if code, _ := post(t, e.server, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected an empty allowlist to reject; received %d", code)
		}
	})
}

// TestControlReloadFails covers a directory that can't be read,
// where the allowlist in use is kept: a failed reload changes nothing.
func TestControlReloadFails(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		if err := os.RemoveAll(e.dir); err != nil {
			t.Fatal(err)
		}

		code, body := control(t, e.server, http.MethodPost, "/reload", "")
		if code != http.StatusInternalServerError {
			t.Errorf("expected 500; received %d %s", code, body)
		}
		if !strings.Contains(body, "reloading the allowlist failed") {
			t.Errorf("expected the failure named; received %s", body)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected the allowlist to be kept; received %d", code)
		}
	})
}

// TestControlToken covers the token: it guards the reload and nothing else on
// that server. A scraper carries no Authorization header,
// so /metrics and /status have to answer without one even where a token is configured.
func TestControlToken(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		t.Setenv(controlTokenEnv, "s3cret")
		e := newEnv(t, tgt, []string{allowedDoc})

		code, body, header := controlFor(t, e.server, http.MethodPost, "/reload", "")
		if code != http.StatusUnauthorized {
			t.Errorf("expected 401 without the token; received %d", code)
		}
		if !strings.Contains(body, "unauthorized") {
			t.Errorf("expected the refusal named; received %s", body)
		}
		// The header a client reads to learn what the endpoint takes.
		if got := header.Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("expected a Bearer challenge; received %q", got)
		}
		if code, _ := control(
			t, e.server, http.MethodPost, "/reload", "wrong",
		); code != http.StatusUnauthorized {
			t.Errorf("expected 401 for a wrong token; received %d", code)
		}
		// A token of the right value under another scheme is no token.
		req, err := http.NewRequest(http.MethodPost,
			"http://"+e.control+"/reload", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Basic s3cret")
		if code, _ := send(t, req); code != http.StatusUnauthorized {
			t.Errorf("expected 401 for another scheme; received %d", code)
		}
		if code, body := control(
			t, e.server, http.MethodPost, "/reload", "s3cret",
		); code != http.StatusOK {
			t.Errorf("expected 200 with the token; received %d %s", code, body)
		}

		for _, path := range []string{"/metrics", "/status"} {
			if code, body := control(
				t, e.server, http.MethodGet, path, "",
			); code != http.StatusOK {
				t.Errorf("expected 200 from %s without a token; received %d %s",
					path, code, body)
			}
		}
	})
}

// TestControlMetrics covers the exposition: every decision is counted and timed,
// the allowlist is described, and the Go collector comes along,
// which is what an operator wants.
func TestControlMetrics(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Fatalf("expected 200")
		}
		for range 2 {
			if code, _ := post(t, e.server, docRejected); code != http.StatusForbidden {
				t.Fatalf("expected 403")
			}
		}
		if code, _ := post(t, e.server, "not json"); code != http.StatusBadRequest {
			t.Fatalf("expected 400")
		}

		code, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if code != http.StatusOK {
			t.Fatalf("expected 200 from /metrics; received %d", code)
		}
		for _, want := range []string{
			`gqlhash_proxy_requests_total{decision="allowed"} 1`,
			`gqlhash_proxy_requests_total{decision="rejected"} 2`,
			`gqlhash_proxy_requests_total{decision="malformed"} 1`,
			// Every decision has a series from the start, so a dashboard reads
			// zero rather than nothing where one hasn't happened yet.
			`gqlhash_proxy_requests_total{decision="too_large"} 0`,
			`gqlhash_proxy_request_duration_seconds_count{decision="too_large"} 0`,
			`gqlhash_proxy_requests_total{decision="ambiguous"} 0`,
			`gqlhash_proxy_requests_total{decision="too_deep"} 0`,
			`gqlhash_proxy_requests_total{decision="batch_too_large"} 0`,
			`gqlhash_proxy_upstream_errors_total 0`,
			`gqlhash_proxy_allowlist_documents 1`,
			"gqlhash_proxy_allowlist_loaded_timestamp_seconds",
			// Every decision is timed, whichever way out of the handler it took.
			`gqlhash_proxy_request_duration_seconds_count{decision="allowed"} 1`,
			`gqlhash_proxy_request_duration_seconds_count{decision="rejected"} 2`,
			`gqlhash_proxy_request_duration_seconds_count{decision="malformed"} 1`,
			"go_goroutines",
		} {
			if !strings.Contains(exposition, want) {
				t.Errorf("expected %q in the exposition", want)
			}
		}

		// The data-plane port serves no metrics, so a scrape target isn't reachable
		// where the clients are. /metrics there is just a request without a document.
		req, err := http.NewRequest(http.MethodGet,
			"http://"+e.address+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := send(t, req); strings.Contains(
			leaked, "gqlhash_proxy_requests_total",
		) {
			t.Errorf("expected no exposition on the data-plane port; received %s", leaked)
		}
	})
}

// TestControlReloadSchema covers the schema: a `.graphqls` file in the
// allowlist directory is read as one, and every document is then checked against it.
// A document asking for a field the schema doesn't have is skipped like one that
// doesn't parse, and a schema that doesn't parse leaves the
// documents unchecked rather than unserved.
func TestControlReloadSchema(t *testing.T) {
	const (
		schema = "type Query { user(id: Int!): User }\n" +
			"type User { name: String }\n"
		// A document the schema has no field for.
		unknownField = "query GetSecret {\n  user(id: 1) {\n    secret\n  }\n}"
		asked        = `{"query":"query GetSecret{user(id:1){secret}}"}`
	)

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		writeDoc(t, e.dir, "b.graphql", unknownField)
		writeDoc(t, e.dir, "schema.graphqls", schema)

		// Checked against the schema, one of the two documents is left out,
		// and the answer names the file and the position of what it asked for.
		_, body := control(t, e.server, http.MethodPost, "/reload", "")
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 1 || answer.Skipped.Total != 1 {
			t.Fatalf("expected one document and one skip; received %s", body)
		}
		if len(answer.Skipped.Errors) != 1 ||
			!strings.Contains(answer.Skipped.Errors[0], "b.graphql:") ||
			!strings.Contains(answer.Skipped.Errors[0], "secret") {
			t.Errorf("expected the field and its position; received %v",
				answer.Skipped.Errors)
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected the checked document to be served; received %d", code)
		}
		if code, _ := post(t, e.server, asked); code != http.StatusForbidden {
			t.Errorf("expected the unchecked field to be refused; received %d", code)
		}

		// A schema that doesn't parse is reported and leaves every document unchecked
		// rather than unserved, so the one it would have refused is served now.
		writeDoc(t, e.dir, "schema.graphqls", "type Query {")
		_, body = control(t, e.server, http.MethodPost, "/reload", "")
		answer = reloadAnswer(t, body)
		if answer.Documents.Total != 2 {
			t.Errorf("expected both documents served unchecked; received %s", body)
		}
		if answer.Skipped.Total != 1 || len(answer.Skipped.Errors) != 1 ||
			!strings.Contains(answer.Skipped.Errors[0], "schema.graphqls") {
			t.Errorf("expected the schema named; received %s", body)
		}
		if code, _ := post(t, e.server, asked); code != http.StatusOK {
			t.Errorf("expected it served with no schema to check it; received %d", code)
		}
	})
}

// TestControlReloadDuplicateHash covers two files whose documents hash alike:
// which one a request meant is unknowable, so neither is served and both are named.
func TestControlReloadDuplicateHash(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		// The same document written differently, which hashes alike.
		writeDoc(t, e.dir, "b.graphql", "query GetUser{user(id:1){name}}")

		_, body := control(t, e.server, http.MethodPost, "/reload", "")
		answer := reloadAnswer(t, body)
		if answer.Documents.Total != 0 || answer.Skipped.Total != 2 {
			t.Fatalf("expected neither served and both skipped; received %s", body)
		}
		for _, e := range answer.Skipped.Errors {
			if !strings.Contains(e, "the same hash as") {
				t.Errorf("expected the collision named; received %q", e)
			}
		}
		if code, _ := post(t, e.server, docAllowed); code != http.StatusForbidden {
			t.Errorf("expected neither of them served; received %d", code)
		}
	})
}

// TestControlMethodsAndTypes covers what the control endpoints answer to,
// and what they say they are. /status and /metrics read state, so every method
// reaches them: a health checker sends HEAD, and a scraper that can't read the
// exposition's content type can't parse it.
func TestControlMethodsAndTypes(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, path := range []string{"/status", "/metrics"} {
			for _, method := range []string{
				http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
			} {
				code, _, header := controlFor(t, e.server, method, path, "")
				if code != http.StatusOK {
					t.Errorf("%s %s: expected 200; received %d", method, path, code)
					continue
				}
				if got := header.Get("Content-Type"); got == "" {
					t.Errorf("%s %s: expected a content type", method, path)
				}
			}
		}

		// What each of them says it is.
		_, _, header := controlFor(t, e.server, http.MethodGet, "/status", "")
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("/status: expected JSON; received %q", got)
		}
		_, _, header = controlFor(t, e.server, http.MethodGet, "/metrics", "")
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("/metrics: expected the exposition format; received %q", got)
		}
		_, _, header = controlFor(t, e.server, http.MethodPost, "/reload", "")
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("/reload: expected JSON; received %q", got)
		}
	})
}

// TestControlStatusLoadedAt covers the timestamp of the allowlist:
// it's the time the list in use was loaded, in RFC 3339, and a reload moves it.
// A deployment reads it to see whether the reload it just asked for happened.
func TestControlStatusLoadedAt(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		loadedAt := func(t *testing.T) time.Time {
			t.Helper()
			_, body := control(t, e.server, http.MethodGet, "/status", "")
			var status struct {
				LoadedAt string `json:"loaded_at"`
			}
			if err := json.Unmarshal([]byte(body), &status); err != nil {
				t.Fatalf("answering no JSON: %v: %s", err, body)
			}
			at, err := time.Parse(time.RFC3339, status.LoadedAt)
			if err != nil {
				t.Fatalf("expected RFC 3339; received %q: %v", status.LoadedAt, err)
			}
			return at
		}

		first := loadedAt(t)
		if time.Since(first) > time.Hour || time.Until(first) > time.Minute {
			t.Errorf("expected the time of the load; received %s", first)
		}

		// A second passes here for the timestamp to differ at the resolution
		// RFC 3339 carries.
		time.Sleep(1100 * time.Millisecond)
		if code, body := control(
			t, e.server, http.MethodPost, "/reload", "",
		); code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}
		if second := loadedAt(t); !second.After(first) {
			t.Errorf("expected the reload to move it; %s then %s", first, second)
		}
	})
}

// TestControlTokenScheme covers the scheme of the Authorization header,
// which RFC 7235 defines without case. The token that follows it is matched exactly.
func TestControlTokenScheme(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		t.Setenv(controlTokenEnv, "s3cret")
		e := newEnv(t, tgt, []string{allowedDoc})

		for _, authorization := range []string{
			"Bearer s3cret", "bearer s3cret", "BEARER s3cret", "BeArEr s3cret",
			// Trailing space and all: HTTP doesn't carry the whitespace around
			// a header value, so this arrives as the line above it.
			"Bearer s3cret ",
		} {
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.control+"/reload", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", authorization)
			if code, _ := send(t, req); code != http.StatusOK {
				t.Errorf("%q: expected it authorized; received %d", authorization, code)
			}
		}

		// The token itself is a secret, matched as one.
		for _, authorization := range []string{
			"Bearer S3CRET", "Bearer  s3cret", "Basic s3cret", "s3cret",
		} {
			req, err := http.NewRequest(http.MethodPost,
				"http://"+e.control+"/reload", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", authorization)
			if code, _ := send(t, req); code != http.StatusUnauthorized {
				t.Errorf("%q: expected 401; received %d", authorization, code)
			}
		}
	})
}

// TestReloadServesThroughout covers the window a reload opens: there is none.
// The list in use answers every request until the new one is ready,
// so a reload under traffic refuses nothing it would have served a moment earlier.
func TestReloadServesThroughout(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		// Enough documents that a reload takes long enough to race.
		for i := range 200 {
			writeDoc(t, e.dir, fmt.Sprintf("d%d.graphql", i),
				fmt.Sprintf("query D%d {\n  user(id: %d) {\n    name\n  }\n}", i, i))
		}

		stop := make(chan struct{})
		refused := make(chan int, 1)
		go func() {
			for {
				select {
				case <-stop:
					refused <- 0
					return
				default:
				}
				if code := <-postAsync(e.address, docAllowed); code != http.StatusOK {
					refused <- code
					return
				}
			}
		}()

		for range 5 {
			if code, body := control(
				t, e.server, http.MethodPost, "/reload", "",
			); code != http.StatusOK {
				t.Fatalf("reload: %d: %s", code, body)
			}
		}
		close(stop)

		if code := <-refused; code != 0 {
			t.Errorf("expected the list in use to answer throughout; received %d", code)
		}
	})
}

// TestControlHealthz covers /healthz: the liveness probe a deployment points at.
// It takes no token, since a kubelet carries no Authorization header, and it
// answers on the control port alone — the traffic port belongs to the API.
func TestControlHealthz(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		t.Setenv(controlTokenEnv, "s3cret")
		e := newEnv(t, tgt, []string{allowedDoc})

		code, body := control(t, e.server, http.MethodGet, "/healthz", "")
		if code != http.StatusOK {
			t.Errorf("expected 200 without the token; received %d: %s", code, body)
		}
		if body != "ok\n" {
			t.Errorf("expected a body a probe can read; received %q", body)
		}
		// HEAD is what a probe configured for one sends, and net/http answers it
		// from the same handler with the body dropped.
		if code, _ := control(
			t, e.server, http.MethodHead, "/healthz", "",
		); code != http.StatusOK {
			t.Errorf("expected 200 for HEAD; received %d", code)
		}

		code, body, header := controlFor(t, e.server, http.MethodPost, "/healthz", "")
		if code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405 for POST; received %d: %s", code, body)
		}
		if got := header.Get("Allow"); got != "GET, HEAD" {
			t.Errorf("expected the methods named; received %q", got)
		}

		// The traffic port serves the API and nothing else:
		// /healthz there is a request carrying no document, not a probe.
		req, err := http.NewRequest(http.MethodGet,
			"http://"+e.address+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := send(t, req); code == http.StatusOK {
			t.Error("expected the traffic port to answer no probe")
		}
	})
}
