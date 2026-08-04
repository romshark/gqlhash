/**
 * The files the two panes start with, and what the reset button puts back.
 *
 * The first tab of each side is the plain case — one small query, registered
 * and sent — so the page opens on the rule and nothing else: the text differs,
 * the hash doesn't. It opens with Ignore set to Variables, which is what lets
 * the sent one write its id in where the registered one takes a variable. The
 * tabs after it are where it gets interesting: a field nobody registered, a
 * document a build step minified, argument values only the two wider Ignore
 * levels forgive.
 */
export interface ExampleFile {
  readonly name: string;
  readonly text: string;
}

/** The directory gqlhash-proxy would serve with -allowlist. */
export const exampleAllowlist: readonly ExampleFile[] = [
  {
    name: "user",
    text: `query User($id: ID!) {
  user(id: $id) {
    id
    name
  }
}
`,
  },
  {
    name: "user-profile",
    text: `query UserProfile($id: ID!, $withPosts: Boolean! = true) {
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
  },
  {
    name: "list-users",
    text: `query ListUsers($first: Int!, $after: String) {
  users(first: $first, after: $after) {
    edges {
      node {
        id
        name
      }
    }
    pageInfo {
      endCursor
      hasNextPage
    }
  }
}
`,
  },
];

/** The documents a client sends, each checked against the allowlist. */
export const exampleOperations: readonly ExampleFile[] = [
  {
    name: "user",
    text: `# Hardcoded id where the entry takes a variable.
# Matches at Ignore=Variables.
query User { user(id: "42") { id, name } }
`,
  },
  {
    name: "user-with-email",
    text: `# One field more than the entry: email. That's a different document, so it
# hashes differently and the proxy answers 403.
query User($id: ID!) {
  user(id: $id) {
    id
    name
    email
  }
}
`,
  },
  {
    name: "profile-minified",
    text: `# The second entry, minified by the client's build step. Still a match.
query UserProfile($id:ID!,$withPosts:Boolean! = true){user(id:$id){id displayName:name avatar(size:128){url}posts(first:10,filter:{published:true,tags:["graphql","go"]})@include(if:$withPosts){edges{node{...PostSummary}}}}}fragment PostSummary on Post{id title}
`,
  },
  {
    name: "profile-other-values",
    text: `# The same shape as the second entry with other argument values:
# avatar(size: 256) and first: 25. Switch Ignore to Nothing and this is
# rejected; Inputs and Variables leave the values out and let it through.
query UserProfile($id: ID!, $withPosts: Boolean! = true) {
  user(id: $id) {
    id
    displayName: name
    avatar(size: 256) {
      url
    }
    posts(first: 25, filter: { published: true, tags: ["graphql", "go"] })
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
  },
  {
    name: "list-users",
    text: `# Minified too, and a match for the third entry.
query ListUsers($first:Int!,$after:String){users(first:$first,after:$after){edges{node{id name}}pageInfo{endCursor hasNextPage}}}
`,
  },
];
