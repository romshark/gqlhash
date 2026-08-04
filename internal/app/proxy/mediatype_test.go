package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcceptsGraphQLResponseJSON(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"application/graphql-response+json", true},
		{"application/graphql-response+json; charset=utf-8", true},
		{"Application/GraphQL-Response+JSON", true},
		{"  application/graphql-response+json  ", true},
		// What a q-value ordering produces, in either position:
		// naming the media type at all is a client that reads one.
		{"application/graphql-response+json, application/json", true},
		{"application/json, application/graphql-response+json;q=0.9", true},
		{"application/json;q=1.0, application/graphql-response+json;q=0.1", true},
		{"text/event-stream, application/graphql-response+json", true},
		{"", false},
		{"application/json", false},
		{"application/json, text/event-stream", false},
		// A wildcard states no preference, and every client that sends no Accept
		// header at all is read the same way: application/json, as before.
		{"*/*", false},
		{"application/*", false},
		// Not a prefix match: a type that merely starts the same is another type.
		{"application/graphql-response+json-ish", false},
		{"application/graphql", false},
	} {
		if got := AcceptsGraphQLResponseJSON(tc.in); got != tc.want {
			t.Errorf("AcceptsGraphQLResponseJSON(%q) = %v; expected %v",
				tc.in, got, tc.want)
		}
	}
}

// TestErrorContentType covers what a rejection is written with:
// the media type GraphQL over HTTP defines where the client asked for it by name,
// and application/json — its legacy watershed, which every client parses —
// where it didn't.
func TestErrorContentType(t *testing.T) {
	const (
		json    = "application/json; charset=utf-8"
		graphql = "application/graphql-response+json; charset=utf-8"
	)
	for _, tc := range []struct{ accept, want string }{
		{"", json},
		{"*/*", json},
		{"application/json", json},
		{"application/graphql-response+json", graphql},
		{"application/graphql-response+json, application/json", graphql},
	} {
		w := httptest.NewRecorder()
		writeError(w, tc.accept, http.StatusForbidden,
			"operation not allowed", "OPERATION_NOT_ALLOWED")
		if got := w.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("Accept %q: expected %q; received %q", tc.accept, tc.want, got)
		}
		// The envelope itself is the same under either media type:
		// what a client asked to be told apart is the type, not the shape.
		if got := w.Body.String(); got !=
			`{"errors":[{"message":"operation not allowed",`+
				`"extensions":{"code":"OPERATION_NOT_ALLOWED"}}]}` {
			t.Errorf("Accept %q: expected the same envelope; received %s", tc.accept, got)
		}
	}
}

// TestErrorContentTypeAllocatesNothing pins that the choice costs a rejection nothing:
// both media types are shared slices, assigned rather than Set,
// and a rejection is the path a flood takes.
func TestErrorContentTypeAllocatesNothing(t *testing.T) {
	for _, accept := range []string{"", "application/graphql-response+json"} {
		run := func() { _ = errorContentType(accept) }
		run()
		if n := testing.AllocsPerRun(200, run); n != 0 {
			t.Errorf("Accept %q: expected no allocations; received %v", accept, n)
		}
	}
}
