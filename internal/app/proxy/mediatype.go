package proxy

import "strings"

// The one rule for the media type the proxy answers its own errors with,
// so the two implementations can't disagree about what a client asked for.

// graphqlResponseJSON is the media type GraphQL over HTTP defines for a GraphQL
// response. It's what a client asks for to be told a well-formed GraphQL
// response apart from any other JSON an intermediary may answer with.
const graphqlResponseJSON = "application/graphql-response+json"

// AcceptsGraphQLResponseJSON reports whether a client named
// [graphqlResponseJSON] in its Accept header, which is what makes it the media
// type of the proxy's own error envelopes.
//
// Named, not matched: `*/*` and `application/*` are what a client sends when it
// states no preference at all — most of them, and every one that sends no
// Accept header — and answering those with the newer type would change what a
// library that never asked for it receives. application/json is the legacy
// watershed of GraphQL over HTTP, so it stays the answer where nothing asked.
//
// Any mention counts, as in [AcceptsEventStream]: a q-value ordering it last is
// still a client that would take one, and reading the ordering would make the
// rule depend on how a client happened to write its header.
func AcceptsGraphQLResponseJSON(accept string) bool {
	for len(accept) > 0 {
		var media string
		if before, after, ok := strings.Cut(accept, ","); ok {
			media, accept = before, after
		} else {
			media, accept = accept, ""
		}
		if i := strings.IndexByte(media, ';'); i >= 0 {
			media = media[:i]
		}
		if strings.EqualFold(strings.TrimSpace(media), graphqlResponseJSON) {
			return true
		}
	}
	return false
}
