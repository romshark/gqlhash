package proxy

import "strings"

// The one rule for what an event stream is, so the two implementations can't disagree
// about which answers escape the ordinary exchange deadlines.

// eventStream is the media type GraphQL over SSE carries its results in.
const eventStream = "text/event-stream"

// AcceptsEventStream reports whether a client asked for an event stream,
// which is what makes a request one the proxy relays as it arrives.
//
// The request decides, not the answer: an implementation chooses how to carry the
// forward before there is an answer to look at.
// The answer still has to agree, see [IsEventStream].
//
// Any mention of the media type counts. A q-value ordering it last is still a
// client that would take one, and reading the ordering would make the rule
// depend on how a client happened to write its Accept header.
func AcceptsEventStream(accept string) bool {
	for len(accept) > 0 {
		var media string
		if before, after, ok := strings.Cut(accept, ","); ok {
			media, accept = before, after
		} else {
			media, accept = accept, ""
		}
		if IsEventStream(media) {
			return true
		}
	}
	return false
}

// IsEventStream reports whether a media type is an event stream,
// parameters and surrounding space aside.
func IsEventStream(mediaType string) bool {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return strings.EqualFold(strings.TrimSpace(mediaType), eventStream)
}
