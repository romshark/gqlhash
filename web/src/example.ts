/**
 * The two documents the editors start with. They're the same document written
 * two ways, so the page opens on a demonstration of what gqlhash is for: the
 * verdict reads identical even though the text plainly differs.
 */
export const exampleDocuments = {
  a: `query UserProfile($id: ID!, $withPosts: Boolean! = true) {
  user(id: $id) {
    id
    displayName: name
    avatar(size: 128) {
      url
    }
    posts(first: 10, filter: { published: true, tags: ["graphql", "go"] })
      @include(if: $withPosts) {
      edges {
        node {
          ...PostSummary
        }
      }
    }
  }
}

fragment PostSummary on Post {
  id
  title
}
`,

  b: `# The same document, only formatted differently. Comments, line breaks,
# indentation and commas are ignorable — the hash doesn't change.
query UserProfile(
  $id: ID!,
  $withPosts: Boolean! = true,
) {
  user(id: $id) { id, displayName: name
    avatar(size: 128) { url }
    posts(
      first: 10,
      filter: {published: true, tags: ["graphql","go"]}
    ) @include(if: $withPosts) { edges { node { ...PostSummary } } }
  }
}
fragment PostSummary on Post { id  title }
`,
} as const;
