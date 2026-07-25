[![Coverage Status](https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main)](https://coveralls.io/github/romshark/gqlhash?branch=main)
![License](https://img.shields.io/github/license/romshark/gqlhash)

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/romshark/gqlhash)](https://github.com/romshark/gqlhash/releases)
[![Awesome GraphQL](https://img.shields.io/badge/Awesome-GraphQL-%23e535ab?logo=graphql&logoColor=white)](https://github.com/chentsulin/awesome-graphql?tab=readme-ov-file#tools---miscellaneous)
[![GoDoc](https://godoc.org/github.com/romshark/gqlhash?status.svg)](https://pkg.go.dev/github.com/romshark/gqlhash)

# gqlhash

Generates SHA1 ([and other](#hash-function)) hashes from GraphQL
[executable documents](https://spec.graphql.org/September2025/#sec-Executable-Definitions)
ignoring formatting and comment diffs to enable fast and robust hash-based comparisons.

Use it as a [CLI tool](#usage) in scripts and CI pipelines or programmatically via the [Compare](https://pkg.go.dev/github.com/romshark/gqlhash#Compare) and [CompareWithBuffer](https://pkg.go.dev/github.com/romshark/gqlhash#CompareWithBuffer) functions for high-throughput firewalls.

gqlhash is [significantly faster](#performance) ⚡ than parsing query documents and
comparing the ASTs or comparing documents after minification.
It can be used to efficiently check whether a GraphQL query is in a set of
[trusted documents](https://benjie.dev/graphql/trusted-documents) by hash.

With [`-ignore-variables`](#ignoring-variables), the following two documents
generate the same SHA1 hash despite their differences in formatting, comments,
input values and variables:

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
query (
  $x: Int
  $y: Float
  $langs: [Language!] # Prefer German, if possible.
  $text: String
) {
  # Some comment
  object(x: $x, y: $y) {
    id
    name # We will need this.
    description @translate(lang: $langs)
    blockstring(s: $text)
  }
}
```

gqlhash is fully compliant with the latest GraphQL specification of
[September 2025](https://spec.graphql.org/September2025/).

Optionally, gqlhash can ignore even more. With `-ignore-inputs`, input values are
ignored, so the following two documents produce the same hash despite their
different argument values (and even value types):

```graphql
{ object(x: 42, y: 1.0) { id } }
```

```graphql
{ object(x: 7, y: "hello") { id } }
```

Both produce the same hex-encoded SHA1 hash `f298bdffe58cc1791fb9bc37b338d472641ab59c`.

`-ignore-variables` does everything `-ignore-inputs` does and, on top of that,
ignores variables entirely — both their definitions and their usages. So a
parameterized document matches its inline-value equivalent:

```graphql
query ($x: Int) { object(x: $x) { id } }
```

```graphql
{ object(x: 42) { id } }
```

Both produce the same hex-encoded SHA1 hash `b09f92659125366c58ec90c771eba361e921aa2f`.

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
go install github.com/romshark/gqlhash/cmd/gqlhash@latest
```

This requires the latest version of [Go](https://go.dev) to be installed.

## Usage

> [!IMPORTANT]
> The CLI spawns a process per invocation, so it's meant for scripts, CI
> pipelines and local use. Don't call it per request on a hot GraphQL request
> handling path. A Go server can use the functions
> ([Compare](https://pkg.go.dev/github.com/romshark/gqlhash#Compare),
> [CompareWithBuffer](https://pkg.go.dev/github.com/romshark/gqlhash#CompareWithBuffer)
> and [AppendQueryHash](https://pkg.go.dev/github.com/romshark/gqlhash#AppendQueryHash)).


gqlhash can read the GraphQL query from stdin until EOF and
print the resulting SHA1 hash as hexadecimal string to stdout:

```sh
# prints: 102fe40ed0c19cf540a8223ae7f425b895a02f1f
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
- `base64url` (URL-safe base64 encoding as defined in
  [RFC 4648 §5](https://datatracker.ietf.org/doc/html/rfc4648#section-5))

By default `hex` is used. Use `-format` to specify a different output format:

```sh
# prints: EC/kDtDBnPVAqCI65/QluJWgLx8=
echo '{foo bar}' | gqlhash -format base64
```

Use `base64url` when the hash goes into a URL, header or file name, since it
avoids the `+` and `/` characters:

```sh
# prints: EC_kDtDBnPVAqCI65_QluJWgLx8=
echo '{foo bar}' | gqlhash -format base64url
```

### Hash Function

gqlhash supports multiple common hash functions:

- `sha1`
- `sha2` (SHA-256)
- `sha3` (SHA3-512)
- `md5`
- `blake2b` (unkeyed)
- `blake2s` (unkeyed)
- `blake3` (unkeyed, 256 bits)
- `fnv` (FNV-1, 64 bits)
- `fnv1a` (FNV-1a, 64 bits)
- `xxh64` (XXH64, unseeded)
- `crc32` (IEEE polynomial)
- `crc64` (ISO polynomial, defined in ISO 3309)

By default `sha1` is used. Use `-hash` to specify a different hash function:

```sh
# prints: 1ZLCPgw2KjpJtMSxgxbZv8W9os51d7CSXSXEtMupwuw=
echo '{foo bar}' | gqlhash -hash sha2 -format base64
```

<details>
<summary>Performance</summary>

Hashing `testdata/big.graphql` (2854 bytes), sorted fastest first.

| `-hash`   | time     | throughput |
| --------- | -------- | ---------- |
| `xxh64`   | 4.35 µs  | 627 MiB/s  |
| `fnv1a`   | 4.75 µs  | 573 MiB/s  |
| `fnv`     | 4.77 µs  | 571 MiB/s  |
| `sha2`    | 5.19 µs  | 524 MiB/s  |
| `blake2b` | 5.58 µs  | 488 MiB/s  |
| `crc32`   | 5.59 µs  | 487 MiB/s  |
| `sha1`    | 5.78 µs  | 471 MiB/s  |
| `blake2s` | 6.15 µs  | 442 MiB/s  |
| `md5`     | 6.21 µs  | 438 MiB/s  |
| `crc64`   | 6.27 µs  | 435 MiB/s  |
| `blake3`  | 6.32 µs  | 431 MiB/s  |
| `sha3`    | 12.82 µs | 212 MiB/s  |

Measured with `go test ./cmd/gqlhash -bench BenchmarkHashFunctions` on an Intel
Xeon w5-2455X, Go 1.26.5, `GOMAXPROCS=1`, over 8 runs.
</details>

### Ignoring Input Values

Use `-ignore-inputs` to ignore input values entirely, so documents that differ
only in their argument or default values (regardless of value type) hash equally:

```sh
# Both print the same hash.
echo '{ object(x: 42, y: 1.0) { id } }' | gqlhash -ignore-inputs
echo '{ object(x: 7, y: "hello") { id } }' | gqlhash -ignore-inputs
```

Variable *usages* are ignored just like literals (`object(x: $v)` and
`object(x: 1)` hash equally), but the variable *signature* is kept — a query
declaring `query ($v: ID)` still differs from one that declares no variables.

### Ignoring Variables

Use `-ignore-variables` to ignore variables entirely — both definitions and
usages — in addition to everything `-ignore-inputs` ignores. A parameterized
document therefore matches its inline-value equivalent:

```sh
# Both print the same hash.
echo 'query ($x: Int) { object(x: $x) { id } }' | gqlhash -ignore-variables
echo '{ object(x: 42) { id } }' | gqlhash -ignore-variables
```

## Performance

- Compared to plain SHA1 hashing gqlhash performance overhead is just **~4x**
  on average across benchmarks (min: ~2x, max: ~6x).
- Compared to parsing the queries into AST with
  [vektah/gqlparser/v2](https://github.com/vektah/gqlparser).
  gqlhash shows a significant advantage of **~64x**
  on average across benchmarks (min: ~18x; max: ~144x).
  The difference can mainly be explained by the fact that gqlhash **doesn't allocate**,
  compared to hundreds of memory allocations for the same queries by gqlparser/v2.

See benchmark results below.

<details>

```
goos: linux
goarch: amd64
pkg: github.com/romshark/gqlhash
cpu: Intel(R) Xeon(R) w5-2455X
BenchmarkReferenceSHA1/blockstring/minified/direct-24           11474282                96.73 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash-24           2948920               399.6 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/vektah-24             44791             26185 ns/op           10905 B/op        195 allocs/op

BenchmarkReferenceSHA1/blockstring/formatted/direct-24          12384094                94.37 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash-24          2730768               434.1 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/vektah-24            43952             25329 ns/op           10953 B/op        195 allocs/op

BenchmarkReferenceSHA1/tiny/minified/direct-24                  17291330                71.16 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash-24                  7187283               154.9 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/vektah-24                    55484             22294 ns/op            9449 B/op        174 allocs/op

BenchmarkReferenceSHA1/tiny/formatted/direct-24                 16904895                68.23 ns/op            0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash-24                6539211               178.3 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/vektah-24                   54963             22998 ns/op            9449 B/op        174 allocs/op

BenchmarkReferenceSHA1/medium/minified/direct-24                 7915527               147.7 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash-24               1370991               885.0 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/minified/vektah-24                 32888             37685 ns/op           17361 B/op        285 allocs/op

BenchmarkReferenceSHA1/medium/formatted/direct-24                5031510               231.2 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash-24              1043098              1171 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/vektah-24               30879             39050 ns/op           17977 B/op        300 allocs/op

BenchmarkReferenceSHA1/big/minified/direct-24                    1307736               906.4 ns/op             0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash-24                   213897              5146 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/minified/vektah-24                     9511            106314 ns/op           53360 B/op        839 allocs/op

BenchmarkReferenceSHA1/big/formatted/direct-24                    897585              1291 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash-24                  186909              6148 ns/op               0 B/op          0 allocs/op
BenchmarkReferenceSHA1/big/formatted/vektah-24                    11332            112696 ns/op           54880 B/op        877 allocs/op
PASS
ok      github.com/romshark/gqlhash     36.408s
```

</details>

## Known Limitations

### Order of Operations, Selections and Arguments

gqlhash ignores **irrelevant differences** between documents such as formatting
and comments — and, optionally, input values and variables (see
[`-ignore-inputs`](#ignoring-input-values) and
[`-ignore-variables`](#ignoring-variables)). It does, however, hash operations,
selections and arguments **in the order they appear**, so reordering them yields
a different hash even though GraphQL considers their order insignificant.
**This is by design**, favoring fast hashing and reduced code complexity.

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

But they won't, because even though the string values are identical, the former uses
a block string while the latter uses a regular string.
In the case when gqlhash is used for query allowlisting
(a.k.a. [Trusted Documents](https://benjie.dev/graphql/trusted-documents))
we usually don't want variations to be allowed, instead we just want the irrelevant
formatting and comments to be ignored.
Whether strings and block strings with equal value should result in the same hash
is up for debate and should probably be configurable via a CLI flag.

### Fragments

gqlhash hashes fragment spreads and fragment definitions as they appear — it does
**not** inline them. A document using a named fragment therefore produces a
different hash than its inlined equivalent, even though both select the same fields:

```graphql
{ user { ...userFields } }
fragment userFields on User { id name }
```

```graphql
{ user { id name } }
```

Inlining a fragment requires the schema (to resolve the type condition against the
parent type), and gqlhash is intentionally schema-agnostic, so it treats fragment
usage as part of the document's structure.
