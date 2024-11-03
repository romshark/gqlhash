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

Generates SHA1 hashes from GraphQL
[executable documents](https://spec.graphql.org/October2021/#sec-Executable-Definitions)
without taking formatting and comments into account to allow fast and robust comparisons.

gqlhash can be used to efficiently check whether a GraphQL query is in a set of 
[trusted documents](https://benjie.dev/graphql/trusted-documents) by hash.

The following two documents will generate the same SHA1 hash despite the
difference in formatting and comments:

```graphql
{
  object(x: 42, y: 1.0) {
    id
    name
    description @translate(lang: [DE, EN])
    blockstring(s: """gqlhash parses block string values
      and doesn't care about formatting.""")
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
    blockstring(
      s: """
      gqlhash parses block string values
      and doesn't care about formatting.
      """
    )
  }
}
```

gqlhash is fully compliant with the latest GraphQL specification of
[October 2021](https://spec.graphql.org/October2021/).


## Installation

### Go install

```sh
go install github.com/romshark/gqlhash@latest
```

### Compiled Binary

You can also download one of the compiled libraries from
[GitHub Releases](https://github.com/romshark/gqlhash/releases).

However, the order and structure of the document must remain the same.

## Usage

gqlhash can read the GraphQL query from stdin until EOF and
print the resulting SHA1 hash as hexadecimal string to stdout:

```sh
# prints: fa8eb9872f835fc36f89e20e762516510622aba8
echo '{foo bar}' | gqlhash
```

To print the version of gqlhash, use:

```sh
gqlhash -version
```

### File Input

gqlhash can also read from a file provided via `-file` if necessary:

```sh
gqlhash -file ./executable_document.graphql
```

### Output Format

A different output format can be specified with `-format` and
accepts `hex` and `base64`:

```sh
# prints: +o65hy+DX8NvieIOdiUWUQYiq6g=
echo '{foo bar}' | gqlhash -format base64
```

## Performance

- Compared to plain SHA1 hashing gqlhash performance overhead on average
  across benchmarks is just **~5x** on average (min: ~3x, max: ~7x).
- Compared to parsing the queries into AST with
  [vektah/gqlparser/v2](https://github.com/vektah/gqlparser).
  gqlhash shows siginificant advantage of **15x** (min: ~10x; max: ~25x)
  on average across benchmarks.
  Also, gqlhash **doesn't allocate memory** dynamically at all, compared to
  hundrets of allocations for the same queries by gqlparser.

See benchmark results below.

<details>

```
goos: darwin
goarch: arm64
pkg: github.com/romshark/gqlhash
cpu: Apple M1 Max
BenchmarkReferenceSHA1/blockstring/minified/direct-10           15573957                76.85 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash-10           3062020               392.5 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/vektah-10             206655              5511 ns/op            7105 B/op        156 allocs/op

BenchmarkReferenceSHA1/blockstring/formatted/direct-10          15431370                77.38 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash-10          2743230               436.9 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/vektah-10            202897              5613 ns/op            7153 B/op        156 allocs/op

BenchmarkReferenceSHA1/tiny/minified/direct-10                  21461752                55.36 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash-10                  7236796               164.3 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/vektah-10                    279013              4060 ns/op            5601 B/op        133 allocs/op

BenchmarkReferenceSHA1/tiny/formatted/direct-10                 21669319                55.03 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash-10                 6503784               183.8 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/vektah-10                   278722              4067 ns/op            5601 B/op        133 allocs/op

BenchmarkReferenceSHA1/medium/minified/direct-10                 9457255               128.0 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash-10                1441172               830.6 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/vektah-10                  102486             11350 ns/op           13321 B/op        246 allocs/op

BenchmarkReferenceSHA1/medium/formatted/direct-10                5762872               207.9 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash-10               1000000              1059 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/vektah-10                  94951             12437 ns/op           13937 B/op        261 allocs/op

BenchmarkReferenceSHA1/big/minified/direct-10                    1445761               828.1 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash-10                    253197              4678 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/vektah-10                      22827             52391 ns/op           49096 B/op        798 allocs/op

BenchmarkReferenceSHA1/big/formatted/direct-10                    989251              1195 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash-10                   210759              5661 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/vektah-10                     21392             55751 ns/op           50615 B/op        836 allocs/op
PASS
ok      github.com/romshark/gqlhash     34.615s
```

</details>

## Known Limitations

### Order of Selections and Arguments

gqlhash ignores **irrelevant differences** between documents such as formatting
and comments, but it will return different hashes for queries with different
order of operations, selections and arguments despite them being identical in content.
**This is by design** to allow for fast hashing and reduced code complexity.

### Strings & Block Strings

In theory you'd assume the following two queries should result in the same hash:

```graphql
{
  blockstring(
    s: """
    line 1
    line 2
    """
  )
}
```

```graphql
{
  blockstring(
    s: "line 1\nline 2"
  )
}
```

But they won't because even though the string values are identical, the former uses
a block string while the latter isn't.
In the case when gqlhash is used for query allowlisting
(a.k.a. [Trusted Documents](https://benjie.dev/graphql/trusted-documents))
we usually don't want variantions to be allowed, instead we just want the irrelevant
formatting and comments to be ignored.
Whether strings and block strings with equal value should result in the same hash
is up for debate and should probably be configurable via CLI flag.
