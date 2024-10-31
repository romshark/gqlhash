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
{
  object(x: 42, y: 1.0) {
    id
    name
    description @translate(lang: [DE, EN])
  }
}
```

```graphql
query {
  # Some comment
  object(x: 42, y: 1.0) {
    id
    name # We will need this.
    description
      @translate(
        lang: [DE, EN] # Prefer German, if possible.
      )
  }
}
```

However, the order and structure of the document must remain the same.

## Performance

gqlhash with SHA1 has just about ~5x the performance overhead compared to direct SHA1
and ~13x faster than parsing the query with
[vektah/gqlparser/v2](https://github.com/vektah/gqlparser).
See benchmark results below.

<details>

```
goos: darwin
goarch: arm64
pkg: github.com/romshark/gqlhash
cpu: Apple M1 Max
BenchmarkReferenceSHA1/direct-10                 6124714               178.4 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/gqlhash-10                1359817               882.4 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/vektah-10                   96661             12014 ns/op           13873 B/op        261 allocs/op
PASS
ok      github.com/romshark/gqlhash     4.862s
```

</details>

Fully compliant with the latest GraphQL specification of
[October 2021](https://spec.graphql.org/October2021/)
