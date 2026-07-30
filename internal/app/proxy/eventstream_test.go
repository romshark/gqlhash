package proxy

import "testing"

func TestIsEventStream(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"Text/Event-Stream", true},
		{"  text/event-stream  ", true},
		{"text/event-stream;charset=utf-8", true},
		{"application/json", false},
		{"application/graphql-response+json", false},
		{"", false},
		// Not a prefix match: a type that merely starts the same is another type.
		{"text/event-streaming", false},
		{"text/event", false},
	} {
		if got := IsEventStream(tc.in); got != tc.want {
			t.Errorf("IsEventStream(%q) = %v; expected %v", tc.in, got, tc.want)
		}
	}
}

func TestAcceptsEventStream(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"text/event-stream", true},
		// What a q-value ordering produces, in either position:
		// naming the media type at all is a client that would take a stream.
		{"application/json, text/event-stream", true},
		{"text/event-stream;q=0.9, application/json", true},
		{"application/json;q=1.0, text/event-stream;q=0.1", true},
		{"*/*, text/event-stream", true},
		{"", false},
		{"application/json", false},
		{"application/json, application/graphql-response+json", false},
		// A wildcard is not an ask for a stream: every browser sends one,
		// and reading it as one would put every request on the streaming path.
		{"*/*", false},
		{"text/*", false},
	} {
		if got := AcceptsEventStream(tc.in); got != tc.want {
			t.Errorf("AcceptsEventStream(%q) = %v; expected %v", tc.in, got, tc.want)
		}
	}
}
