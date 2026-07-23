[![GoReportCard](https://goreportcard.com/badge/github.com/romshark/gqlhash)](https://goreportcard.com/report/github.com/romshark/gqlhash)
[![Coverage Status](https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main)](https://coveralls.io/github/romshark/gqlhash?branch=main)
![License](https://img.shields.io/github/license/romshark/gqlhash)

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/romshark/gqlhash)](https://github.com/romshark/gqlhash/releases)
[![Awesome GraphQL](https://img.shields.io/badge/Awesome-GraphQL-%23e535ab?logo=graphql&logoColor=white)](https://github.com/chentsulin/awesome-graphql?tab=readme-ov-file#tools---miscellaneous)
[![GoDoc](https://godoc.org/github.com/romshark/gqlhash?status.svg)](https://pkg.go.dev/github.com/romshark/gqlhash)

# gqlhash

Generates SHA1 ([and other](#hash-function)) hashes from GraphQL
[executable documents](https://spec.graphql.org/September2025/#sec-Executable-Definitions)
ignoring formatting and comment diffs to enable fast and robust hash-based comparisons.

gqlhash is [significantly faster](#performance) ⚡ than parsing query documents and
comparing the ASTs or comparing documents after minification.
It can be used to efficiently check whether a GraphQL query is in a set of
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
[September 2025](https://spec.graphql.org/September2025/).

## Installation

### Homebrew 🍺

```sh
brew tap romshark/tools
brew install gqlhash
```

### Compiled Binary

Download one of the compiled binaries from
[GitHub Releases](https://github.com/romshark/gqlhash/releases).

### From Source

```sh
go install github.com/romshark/gqlhash@latest
```

This requires the latest version of [Go](https://go.dev) to be installed.

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

gqlhash supports the following output formats:

- `hex` (hexadecimal string)
- `base32` (base32 encoding as defined in
  [RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648))
- `base64` (base64 encoding as defined in
  [RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648))

By default `hex` is used. Use `-format` to specify a different hash function:

```sh
# prints: +o65hy+DX8NvieIOdiUWUQYiq6g=
echo '{foo bar}' | gqlhash -format base64
```

### Hash Function

gqlhash supports multiple common hash functions:

- `sha1`
- `sha2` (SHA-256)
- `sha3` (SHA3-512)
- `md5`
- `blake2b` (unkeyed)
- `blake2s` (unkeyed)
- `fnv`
- `crc32` (IEEE polynomial)
- `crc64` (ISO polynomial, defined in ISO 3309)

By default `sha1` is used. Use `-hash` to specify a different hash function:

```sh
# prints: t2XWfakQNusOObQfnS09PT3NOgfVqFOyizwqxYzxn4k=
echo '{foo bar}' | gqlhash -hash sha2 -format base64
```

## Performance

- Compared to plain SHA1 hashing gqlhash performance overhead is just **~4x**
  on average across benchmarks (min: ~2x, max: ~6x).
- Compared to parsing the queries into AST with
  [vektah/gqlparser/v2](https://github.com/vektah/gqlparser).
  gqlhash shows a significant advantage of **~66x**
  on average across benchmarks (min: ~19x; max: ~151x).
  The difference can mainly be explained by the fact that gqlhash **doesn't allocate**,
  compared to hundreds of memory allocations for the same queries by gqlparser/v2.

See benchmark results below.

<details>

```
goos: linux
goarch: amd64
pkg: github.com/romshark/gqlhash
cpu: Intel(R) Xeon(R) w5-2455X
BenchmarkReferenceSHA1/blockstring/minified/direct-24           10923078                97.66 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash-24           2913408               404.9 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/vektah-24             46718             25602 ns/op           10905 B/op        195 allocs/op

BenchmarkReferenceSHA1/blockstring/formatted/direct-24          11947243                96.40 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash-24          2685093               438.9 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/vektah-24            47787             26984 ns/op           10953 B/op        195 allocs/op

BenchmarkReferenceSHA1/tiny/minified/direct-24                  16543544                70.84 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash-24                  7444413               153.8 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/vektah-24                    51908             23287 ns/op            9449 B/op        174 allocs/op

BenchmarkReferenceSHA1/tiny/formatted/direct-24                 15954482                69.19 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash-24                 6663024               173.7 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/vektah-24                   53880             23137 ns/op            9449 B/op        174 allocs/op

BenchmarkReferenceSHA1/medium/minified/direct-24                 7753413               147.7 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash-24                1312906               894.0 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/vektah-24                  32834             39554 ns/op           17361 B/op        285 allocs/op

BenchmarkReferenceSHA1/medium/formatted/direct-24                5087644               237.1 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash-24               1051874              1161 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/vektah-24                32373             39461 ns/op           17977 B/op        300 allocs/op

BenchmarkReferenceSHA1/big/minified/direct-24                    1333972               904.3 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash-24                    222135              5101 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/vektah-24                      10000            107211 ns/op           53360 B/op        839 allocs/op

BenchmarkReferenceSHA1/big/formatted/direct-24                    924752              1297 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash-24                   190370              6081 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/vektah-24                     10000            113547 ns/op           54880 B/op        877 allocs/op
PASS
ok      github.com/romshark/gqlhash     35.588s
```

</details>

## Known Limitations

### Order of Operations, Selections and Arguments

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
we usually don't want variations to be allowed, instead we just want the irrelevant
formatting and comments to be ignored.
Whether strings and block strings with equal value should result in the same hash
is up for debate and should probably be configurable via CLI flag.
