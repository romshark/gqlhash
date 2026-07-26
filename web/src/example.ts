/** The document the editor starts with. */
export const exampleDocument = `# gqlhash ignores comments, whitespace and descriptions,
# so reformatting this document doesn't change its hash.
# Renaming a field or reordering selections does.

query UserProfile($id: ID!, $withPosts: Boolean! = true) {
  user(id: $id) {
    id
    displayName: name
    email
    role
    avatar(size: 128) {
      url
      width
      height
    }
    posts(first: 10, filter: { published: true, tags: ["graphql", "go"] })
      @include(if: $withPosts) {
      edges {
        cursor
        node {
          ...PostSummary
        }
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}

fragment PostSummary on Post {
  id
  title
  publishedAt
  author {
    name
  }
}
`;
