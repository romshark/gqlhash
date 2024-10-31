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

gqlhash with SHA1 has just about ~5x the performance overhead compared to direct SHA1.
See benchmark results below.

<details>

```
go test -bench BenchmarkReferenceSHA1 -benchmem -count 3
goos: darwin
goarch: arm64
pkg: github.com/romshark/gqlhash
cpu: Apple M1 Max
BenchmarkReferenceSHA1/sha1_direct-10            6103970               178.1 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/sha1_direct-10            6706924               178.3 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/sha1_direct-10            6722865               178.0 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/sha1_gqlhash-10           1358607               883.8 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/sha1_gqlhash-10           1356372               886.0 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/sha1_gqlhash-10           1358155               884.5 ns/op             0 B/op          0 allocs/op
PASS
ok      github.com/romshark/gqlhash     10.522s
```

</details>

Fully compliant with the latest GraphQL specification of
[October 2021](https://spec.graphql.org/October2021/)
