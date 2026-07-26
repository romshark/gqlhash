[![Coverage Status](https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main)](https://coveralls.io/github/romshark/gqlhash?branch=main)
![License](https://img.shields.io/github/license/romshark/gqlhash)

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/romshark/gqlhash)](https://github.com/romshark/gqlhash/releases)
[![Awesome GraphQL](https://img.shields.io/badge/Awesome-GraphQL-%23e535ab?logo=graphql&logoColor=white)](https://github.com/chentsulin/awesome-graphql?tab=readme-ov-file#tools---miscellaneous)
[![GoDoc](https://godoc.org/github.com/romshark/gqlhash?status.svg)](https://pkg.go.dev/github.com/romshark/gqlhash)

# gqlhash

gqlhash generates SHA1 ([and other](#hash-function)) hashes from GraphQL [executable documents](https://spec.graphql.org/September2025/#sec-Executable-Definitions). Comments and differences in formatting don't change the hash, so two documents that differ only in their layout hash alike. A string value is hashed by the value it stands for, so a string and a block string holding the same value hash alike too.

It's a [CLI tool](#usage) for scripts and CI pipelines and a Go package ([Compare](https://pkg.go.dev/github.com/romshark/gqlhash#Compare), [CompareWithBuffer](https://pkg.go.dev/github.com/romshark/gqlhash#CompareWithBuffer), [AppendQueryHash](https://pkg.go.dev/github.com/romshark/gqlhash#AppendQueryHash)).

It's [faster](#performance) than parsing a document into an AST and comparing the ASTs, and faster than comparing documents after minification.

With [`-ignore=variables`](#ignoring-variables) the following two documents produce the same SHA1 hash, despite differing in formatting, comments, input values and variables:

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

gqlhash implements the GraphQL specification of [September 2025](https://spec.graphql.org/September2025/).

`-ignore=inputs` ignores input values, so the following two documents produce the same hash despite differing argument values and value types:

```graphql
{ object(x: 42, y: 1.0) { id } }
```

```graphql
{ object(x: 7, y: "hello") { id } }
```

Both produce the same hex-encoded SHA1 hash `f298bdffe58cc1791fb9bc37b338d472641ab59c`.

`-ignore=variables` ignores what `-ignore=inputs` ignores and the variables on top of that, both their definitions and their usages. A parameterized document then matches its inline-value equivalent:

```graphql
query ($x: Int) { object(x: $x) { id } }
```

```graphql
{ object(x: 42) { id } }
```

Both produce the same hex-encoded SHA1 hash `b09f92659125366c58ec90c771eba361e921aa2f`.

## Use cases

### Trusted documents

A server that implements [trusted documents](https://benjie.dev/graphql/trusted-documents), also known as persisted queries or a query allowlist, keeps the hash of every document it accepts and rejects everything else. The client sends a document, the server hashes it and looks the hash up.

The hash ignores formatting, so a client that reformats, minifies or re-indents a document keeps the hash it was registered under. Without that, the allowlist has to store the document byte for byte and every layout change is a new entry.

### Cache keys

The hash of a document is a key for a query plan cache or a response cache. Two clients that send the same document formatted differently share the entry.

### Change detection

Comparing the hash of a committed document against a generated one reports a change in the document and not in its layout, which is what a CI check wants to fail on.

### Grouping operations

With [`-ignore=inputs`](#ignoring-input-values) documents that differ only in their literal values share a hash, which groups them in logs and metrics by shape rather than by argument.

## Installation

### Homebrew

```sh
brew tap romshark/tools
brew install gqlhash
```

### Compiled Binary

Download a compiled binary from [GitHub Releases](https://github.com/romshark/gqlhash/releases).

### From Source

```sh
go install github.com/romshark/gqlhash/cmd/gqlhash@latest
```

This requires the latest version of [Go](https://go.dev).

## Usage

> [!IMPORTANT]
> The CLI spawns a process per invocation. It's for scripts, CI pipelines and local use, not for a per-request path. A Go server uses the package functions ([Compare](https://pkg.go.dev/github.com/romshark/gqlhash#Compare), [CompareWithBuffer](https://pkg.go.dev/github.com/romshark/gqlhash#CompareWithBuffer), [AppendQueryHash](https://pkg.go.dev/github.com/romshark/gqlhash#AppendQueryHash)), which all take a `string` or a `[]byte` document. A [parser.Parser](https://pkg.go.dev/github.com/romshark/gqlhash/parser#Parser) per goroutine reuses its buffers instead of taking them from a global pool.

gqlhash reads the document from stdin until EOF and prints its SHA1 hash as a hexadecimal string to stdout:

```sh
# prints: 102fe40ed0c19cf540a8223ae7f425b895a02f1f
echo '{foo bar}' | gqlhash
```

To print the version:

```sh
gqlhash -version
```

### File Input

`-file` reads the document from a file instead:

```sh
gqlhash -file ./executable_document.graphql
```

### Output Format

The supported output formats:

- `hex` (hexadecimal string)
- `base32` (base32 encoding as defined in
  [RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648))
- `base64` (base64 encoding as defined in
  [RFC 4648](https://datatracker.ietf.org/doc/html/rfc4648))
- `base64url` (URL-safe base64 encoding as defined in
  [RFC 4648 §5](https://datatracker.ietf.org/doc/html/rfc4648#section-5))

The default is `hex`. `-format` selects another one:

```sh
# prints: EC/kDtDBnPVAqCI65/QluJWgLx8=
echo '{foo bar}' | gqlhash -format base64
```

`base64url` avoids the `+` and `/` characters, which makes it the format for a URL, a header or a file name:

```sh
# prints: EC_kDtDBnPVAqCI65_QluJWgLx8=
echo '{foo bar}' | gqlhash -format base64url
```

### Hash Function

The supported hash functions:

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

The default is `sha1`. `-hash` selects another one:

```sh
# prints: 1ZLCPgw2KjpJtMSxgxbZv8W9os51d7CSXSXEtMupwuw=
echo '{foo bar}' | gqlhash -hash sha2 -format base64
```

<details>
<summary>Performance</summary>

Hashing `testdata/big.graphql` (2854 bytes), sorted fastest first.

| `-hash`   | time    | throughput  |
| --------- | ------- | ----------- |
| `xxh64`   | 2.04 µs | 1336 MiB/s  |
| `crc32`   | 2.10 µs | 1297 MiB/s  |
| `sha1`    | 2.39 µs | 1137 MiB/s  |
| `sha2`    | 2.40 µs | 1135 MiB/s  |
| `crc64`   | 2.72 µs | 999 MiB/s   |
| `blake2b` | 3.26 µs | 834 MiB/s   |
| `fnv`     | 3.57 µs | 762 MiB/s   |
| `fnv1a`   | 3.62 µs | 751 MiB/s   |
| `blake3`  | 3.77 µs | 721 MiB/s   |
| `md5`     | 3.93 µs | 693 MiB/s   |
| `blake2s` | 4.06 µs | 670 MiB/s   |
| `sha3`    | 4.85 µs | 561 MiB/s   |

Measured with `go test ./cmd/gqlhash -bench BenchmarkHashFunctions` on an Apple M4 Pro, Go 1.26.5, `GOMAXPROCS=1`, over 8 runs.
</details>

### Ignoring Input Values

`-ignore` selects what to leave out of the hash: `nothing` (the default), `inputs` or `variables`. Each one leaves out what the one before it leaves out, and more.

`-ignore=inputs` ignores input values, so documents that differ only in their argument or default values, whatever the value type, hash alike:

```sh
# Both print the same hash.
echo '{ object(x: 42, y: 1.0) { id } }' | gqlhash -ignore=inputs
echo '{ object(x: 7, y: "hello") { id } }' | gqlhash -ignore=inputs
```

Variable usages are ignored like literals: `object(x: $v)` and `object(x: 1)` hash alike. The variable signature is kept, so `query ($v: ID)` differs from an operation that declares no variables.

### Ignoring Variables

`-ignore=variables` ignores variables entirely, both definitions and usages, on top of what `-ignore=inputs` ignores. A parameterized document then matches its inline-value equivalent:

```sh
# Both print the same hash.
echo 'query ($x: Int) { object(x: $x) { id } }' | gqlhash -ignore=variables
echo '{ object(x: 42) { id } }' | gqlhash -ignore=variables
```

## Performance

Measured against two references across the benchmark documents:

- Hashing the document bytes directly with SHA1, which does no parsing: gqlhash takes ~4x as long (min ~2.5x, max ~5x).
- Parsing the document into an AST with [vektah/gqlparser/v2](https://github.com/vektah/gqlparser): gqlhash takes ~1/53 of the time (min ~1/18, max ~1/113). gqlhash allocates nothing, gqlparser/v2 allocates a few hundred times per document.

Results below.

<details>

```
goos: darwin
goarch: arm64
pkg: github.com/romshark/gqlhash
cpu: Apple M4 Pro
BenchmarkReferenceSHA1/blockstring/minified/direct-14         	27854104	        44.93 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash-14        	 5863072	       208.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/vektah-14         	  110545	     10269 ns/op	   10905 B/op	     195 allocs/op

BenchmarkReferenceSHA1/blockstring/formatted/direct-14        	27563870	        42.82 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash-14       	 5406764	       225.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/vektah-14        	  103300	     10666 ns/op	   10953 B/op	     195 allocs/op

BenchmarkReferenceSHA1/tiny/minified/direct-14                	37450552	        33.23 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash-14               	14820181	        81.72 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/vektah-14                	  122278	      9244 ns/op	    9449 B/op	     174 allocs/op

BenchmarkReferenceSHA1/tiny/formatted/direct-14               	39172748	        31.60 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash-14              	13722361	        87.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/vektah-14               	  132538	      9356 ns/op	    9449 B/op	     174 allocs/op

BenchmarkReferenceSHA1/medium/minified/direct-14              	14299480	        82.30 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash-14             	 3132934	       407.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/vektah-14              	   83083	     13969 ns/op	   17361 B/op	     285 allocs/op

BenchmarkReferenceSHA1/medium/formatted/direct-14             	 8527635	       137.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash-14            	 2370624	       523.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/vektah-14             	   76917	     15946 ns/op	   17977 B/op	     300 allocs/op

BenchmarkReferenceSHA1/big/minified/direct-14                 	 1979839	       595.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash-14                	  629686	      1918 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/vektah-14                 	   30684	     39336 ns/op	   53360 B/op	     839 allocs/op

BenchmarkReferenceSHA1/big/formatted/direct-14                	 1402633	       868.5 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash-14               	  495696	      2502 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/vektah-14                	   26110	     44907 ns/op	   54880 B/op	     877 allocs/op
PASS
ok  	github.com/romshark/gqlhash	34.943s
```

</details>

## Known Limitations

### Order of Operations, Selections and Arguments

Operations, selections and arguments are hashed in the order they appear. Reordering them changes the hash, although GraphQL considers their order insignificant.

Rationale: hashing them order-insensitively requires sorting, which costs time and code.

### Fragments

Fragment spreads and fragment definitions are hashed as they appear, not inlined. A document using a named fragment produces a different hash than its inlined equivalent, although both select the same fields:

```graphql
{ user { ...userFields } }
fragment userFields on User { id name }
```

```graphql
{ user { id name } }
```

Rationale: inlining a fragment requires the schema to resolve the type condition against the parent type. gqlhash needs no schema.
