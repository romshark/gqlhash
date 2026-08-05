package acceptance

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestNestedQueryMember covers a query member that isn't the request's:
// the document is the member of the request object, and one nested inside another
// member is a value like any other. A reader that walks the whole body finds the
// wrong one and flips the verdict.
func TestNestedQueryMember(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		// The document is the one at the top. What variables carry is data.
		body := `{"query":"` + allowedText + `","variables":{"query":"` +
			rejectedText + `"}}`
		if code, answer := post(t, e.server, body); code != http.StatusOK {
			t.Errorf("expected the top-level document to decide; received %d: %s",
				code, answer)
		}

		// The other way round: an allowed document nested in variables is no
		// document of this request, so the request carries none.
		body = `{"variables":{"query":"` + allowedText + `"}}`
		if code, answer := post(t, e.server, body); code != http.StatusBadRequest {
			t.Errorf("expected a request carrying no document; received %d: %s",
				code, answer)
		}
		// And one nested beside a rejected one is still rejected.
		body = `{"query":"` + rejectedText + `","variables":{"query":"` +
			allowedText + `"}}`
		if code, _ := post(t, e.server, body); code != http.StatusForbidden {
			t.Errorf("expected the top-level document to decide; received %d", code)
		}
	})
}

// TestQueryCollisionOnAnyValue covers the collision rule reaching a member whose
// value is no document at all. The name is what collides: a decoder reading the
// body into a struct takes the last member and runs it,
// so a member this read nothing from is still a second place the document could be.
func TestQueryCollisionOnAnyValue(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		for _, second := range []string{`null`, `42`, `{"x":1}`, `["x"]`, `true`} {
			for _, body := range []string{
				`{"query":"` + allowedText + `","query":` + second + `}`,
				`{"query":` + second + `,"query":"` + allowedText + `"}`,
			} {
				code, answer := post(t, e.server, body)
				if code != http.StatusBadRequest {
					t.Errorf("%s: expected 400; received %d: %s", body, code, answer)
					continue
				}
				if !strings.Contains(answer, "collision") {
					t.Errorf("%s: expected the collision named; received %s", body, answer)
				}
			}
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected none of them forwarded; received %d", n)
		}
	})
}

// TestBatchElementCollision covers -server.max-batch and the collision rule together:
// each element of a batch names its document once,
// and the flag allows a batch rather than requiring one.
func TestBatchElementCollision(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc, "{ b }"}, "-server.max-batch", "8")

		// One element naming the document twice is the whole batch refused.
		body := `[{"query":"` + allowedText + `","query":"` + rejectedText + `"}]`
		code, answer := post(t, e.server, body)
		if code != http.StatusBadRequest || !strings.Contains(answer, "collision") {
			t.Errorf("expected the element's collision to refuse it; received %d: %s",
				code, answer)
		}
		// The second element's, too.
		body = `[{"query":"` + allowedText + `"},{"query":"{ b }","query":"{ b }"}]`
		if code, answer := post(t, e.server, body); code != http.StatusBadRequest {
			t.Errorf("expected the second element's collision; received %d: %s",
				code, answer)
		}

		// The flag allows a batch, it doesn't require one.
		if code, answer := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Errorf("expected a lone request still served; received %d: %s",
				code, answer)
		}
		if n := e.api.count(); n != 1 {
			t.Errorf("expected only that one forwarded; received %d", n)
		}
	})
}

// TestGETParameterNameCase covers the case of the query parameter,
// which is unlike the JSON member: every URL library matches a parameter name exactly,
// so ?Query= names no document, where the JSON member is matched without case
// because a JSON decoder matches a struct field that way.
//
// The asymmetry is the contract: folding case here refuses requests the original serves,
// folding it nowhere serves requests it should refuse.
func TestGETParameterNameCase(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)
		allowed := url.QueryEscape(allowedText)
		evil := url.QueryEscape(rejectedText)

		// Another case is another parameter, so the request carries no document.
		for _, rawQuery := range []string{"Query=" + allowed, "QUERY=" + allowed} {
			if code, answer := get(t, e.server, rawQuery); code !=
				http.StatusBadRequest {
				t.Errorf("%s: expected a request carrying no document; received %d: %s",
					rawQuery, code, answer)
			}
		}

		// And beside the document it's a parameter the API may read for
		// anything, so it's no collision here.
		if code, answer := get(
			t, e.server, "query="+allowed+"&Query="+evil,
		); code != http.StatusOK {
			t.Errorf("expected the document to decide; received %d: %s", code, answer)
		}

		// The JSON member is the other way round: case doesn't part it from the
		// document, so two of them collide.
		body := `{"query":"` + allowedText + `","QUERY":"` + rejectedText + `"}`
		if code, _ := post(t, e.server, body); code != http.StatusBadRequest {
			t.Errorf("expected the JSON member matched without case; received %d", code)
		}
	})
}

// TestMaxBodyBoundary covers the edge of -server.max-body, which is inclusive:
// a body of exactly that many bytes is read and decided on, one byte more is
// refused unread. Off by one here is a request a deployment sized for and this
// wouldn't serve.
func TestMaxBodyBoundary(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// A request body of exactly this many bytes, carrying the allowed
		// document padded to the limit.
		const limit = 256
		pad := func(n int) string {
			body := `{"query":"` + allowedText + `","x":"` +
				strings.Repeat("p", n) + `"}`
			return body
		}
		// Grow the padding until the body is the limit exactly.
		n := 0
		for len(pad(n)) < limit {
			n++
		}
		atLimit := pad(n)
		if len(atLimit) != limit {
			t.Fatalf("built a body of %d bytes, expected %d", len(atLimit), limit)
		}

		e := newEnv(t, tgt, []string{allowedDoc},
			"-server.max-body", strconv.Itoa(limit))

		// At the limit it's read, hashed and allowed.
		if code, answer := post(t, e.server, atLimit); code != http.StatusOK {
			t.Errorf("at the limit: expected 200; received %d: %s", code, answer)
		}
		// One byte over, it's refused for its size rather than its document.
		over := pad(n + 1)
		if code, answer := post(t, e.server, over); code !=
			http.StatusRequestEntityTooLarge {
			t.Errorf("over the limit by %d: expected 413; received %d: %s",
				len(over)-limit, code, answer)
		}
	})
}

// TestJSONEscapes covers the escapes a document arrives under.
// What's hashed is the document the escapes spell,
// so every spelling of one document is that document.
func TestJSONEscapes(t *testing.T) {
	// Documents holding what the escapes below spell.
	const (
		withSolidus = `{ f(s: "a/b") }`
		withUnicode = `{ f(s: "é") }`
		withNewline = "{\n  f(s: \"x\")\n}"
	)

	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, withSolidus, withUnicode, withNewline)

		for _, tc := range []struct{ name, body string }{
			// \/ is the escape most languages don't write and every JSON reader
			// has to take. encoding/json never emits it, so this is by hand.
			{`\/`, `{"query":"{ f(s: \"a\/b\") }"}`},
			{"the same document unescaped", `{"query":"{ f(s: \"a/b\") }"}`},
			// A rune written as an escape, and as itself.
			{`é`, `{"query":"{ f(s: \"é\") }"}`},
			{"the rune itself", `{"query":"{ f(s: \"é\") }"}`},
			// A newline of the document, escaped as JSON requires.
			{`\n`, `{"query":"{\n  f(s: \"x\")\n}"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if code, answer := post(t, e.server, tc.body); code != http.StatusOK {
					t.Errorf("expected the document allowed; received %d: %s",
						code, answer)
				}
			})
		}

		// Half of a surrogate pair is no rune. A leading one is a broken escape,
		// which is a malformed request; a trailing one alone is read as the
		// replacement rune, which is a document nobody allowed.
		if code, answer := post(t, e.server, `{"query":"\ud83d"}`); code !=
			http.StatusBadRequest {
			t.Errorf("a lone leading surrogate: expected 400; received %d: %s",
				code, answer)
		}
		if code, answer := post(t, e.server, `{"query":"\udca9"}`); code !=
			http.StatusForbidden {
			t.Errorf("a lone trailing surrogate: expected 403; received %d: %s",
				code, answer)
		}
	})
}

// TestContentLengthShorterThanBody covers a request declaring fewer bytes than
// it sends. The declared length is what's read and hashed, so the bytes past it are
// the next request on that connection rather than something that rode in with this one:
// reading to the end instead would hash one document and hand the API another.
func TestContentLengthShorterThanBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := shared(t, tgt)
		e.allow(t, allowedDoc)

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// The allowed document, and a rejected one past the declared length.
		trailing := `{"query":"` + rejectedText + `"}`
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\n"+
				"Content-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n%s%s",
			len(docAllowed), docAllowed, trailing,
		); err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		status, _, _ := strings.Cut(string(buf[:n]), "\r\n")
		if !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Errorf("expected the declared body to be the document; received %q",
				status)
		}
		// One request reached the API: the allowed one. Whatever the server
		// makes of the bytes past the length, they are not part of it.
		if got := e.api.count(); got != 1 {
			t.Errorf("expected one request forwarded; received %d", got)
		}
		for _, r := range e.api.all() {
			if r.body != docAllowed {
				t.Errorf("expected only the declared body forwarded; received %q",
					r.body)
			}
		}
	})
}
