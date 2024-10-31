<a href="https://pkg.go.dev/github.com/romshark/gqlhash">
    <img src="https://godoc.org/github.com/romshark/gqlhash?status.svg" alt="GoDoc">
</a>
<a href="https://goreportcard.com/report/github.com/romshark/gqlhash">
    <img src="https://goreportcard.com/badge/github.com/romshark/gqlhash" alt="GoReportCard">
</a>
<a href='https://coveralls.io/github/romshark/gqlhash?branch=main'>
    <img src='https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main' alt='Coverage Status' />
</a>

# gqlhash

Generates hashes from GraphQL
[executable documents](https://spec.graphql.org/October2021/#sec-Executable-Definitions)
without taking formatting and comments into account to allow fast and robust comparisons.

The following two documents will generate the same SHA1 hash:

```graphql
{object(x:42,y:1.0){id,name,description@translate(lang:[DE,EN])}}
```

```graphql
query { # Some comment
  object( x: 42 y: 1.0 ) {
    id
    name # We will need this.
    description @translate(
        lang: [DE EN] # Prefer German, if possible.
    )
  }
}
```

However, the order and structure of the document must remain the same.

Fully compliant with the latest GraphQL specification of
[October 2021](https://spec.graphql.org/October2021/)
