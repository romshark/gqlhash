[![Coverage Status](https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main)](https://coveralls.io/github/romshark/gqlhash?branch=main)
![License](https://img.shields.io/github/license/romshark/gqlhash)

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/romshark/gqlhash)](https://github.com/romshark/gqlhash/releases)
[![Awesome GraphQL](https://img.shields.io/badge/Awesome-GraphQL-%23e535ab?logo=graphql&logoColor=white)](https://github.com/chentsulin/awesome-graphql?tab=readme-ov-file#tools---miscellaneous)
[![GoDoc](https://godoc.org/github.com/romshark/gqlhash/v2?status.svg)](https://pkg.go.dev/github.com/romshark/gqlhash/v2)
[![Playground](https://img.shields.io/badge/Playground-try%20it%20live-%23e10098?logo=graphql&logoColor=white)](https://romshark.github.io/gqlhash/)

# gqlhash

gqlhash generates SHA-256 ([and other](#hash-function)) hashes from GraphQL [executable documents](https://spec.graphql.org/September2025/#sec-Executable-Definitions) ignoring comments, differences in formatting, and optionally input values and variables.

It's shipped as:
1. The Go package [github.com/romshark/gqlhash/v2](https://pkg.go.dev/github.com/romshark/gqlhash/v2) for fast GraphQL request document hashing.
2. `github.com/romshark/gqlhash/v2/cmd/gqlhash` a [CLI tool](#usage-gqlhash) for scripts and CI pipelines.
3. `github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy`, a fast [allowlist-firewall proxy](cmd/gqlhash-proxy/README.md) you can put in front of a GraphQL API.

Generating a gqlhash is [faster](#performance) than parsing a document into an AST and comparing the ASTs.

> [!IMPORTANT]
> See [MIGRATION.md](MIGRATION.md) for migrating from v1 to the new v2.

On a 24-thread Xeon, sharing the machine with the load generator that drives it, `gqlhash-proxy` turns away **~928,000** unknown documents a second at a median of **140 µs**, and forwards **~211,000** allowed ones.

`gqlhash-proxy` at `GOGC=800`, wrk at 200 connections for 20s, generator and proxy on the same Xeon w5-2455X — 12 physical cores, 24 hardware threads:

| | rejected | forwarded |
| --- | --- | --- |
| req/s | **~928,000** | ~211,000 |
| median latency | **140 µs** | 0.94 ms |
| p99 latency | 2.75 ms | 4.27 ms |
| CPU per request | 18 µs | 66 µs |
| cores held by the proxy | 16.8 of 24 | 13.9 of 24 |
| cores busy on the machine | 23.0 of 24 (96%) | 21.4 of 24 (89%) |
| RSS peak / mean | 202 / 111 MB | 168 / 160 MB |

<details>
<summary>Benchmarking Details</summary>
The results above are a median of three runs.
A rejection never opens an upstream connection, which is where the four-fold difference comes from.

**These are numbers from a contended machine, and they understate the proxy.** The load generator and the sample API run on the same 24 hardware threads, so the proxy never had the box to itself: rejecting, it held 16.8 while wrk took 6.1 and the machine ran out at 96%; forwarding, the sample API alone cost 5.7. What a figure worth quoting needs is a second machine driving the load. `go run ./internal/cmd/loadtest` reproduces exactly the above, contention included, and prints what each of the three held so a run starved by its own generator is visible in its output.

- [More details](TUNING_GQLHASH_PROXY.md)
- [Performance Profile](PERFORMANCE.md)
</details>

----

With [`-ignore=variables`](#ignoring-variables) the following two documents produce the same SHA-256 hash, despite differing in formatting, comments, input values and variables:

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

Both produce the same hex-encoded SHA-256 hash `03f3ba19742426404ddcf6467c7f210472f973d3cc78110ebf4ca94a2326e50d`.

`-ignore=variables` ignores what `-ignore=inputs` ignores and the variables on top of that, both their definitions and their usages. A parameterized document then matches its inline-value equivalent:

```graphql
query ($x: Int) { object(x: $x) { id } }
```

```graphql
{ object(x: 42) { id } }
```

Both produce the same hex-encoded SHA-256 hash `21de2f6884f4e7e93a6c45c729b1d6fc47a73c4e657b9b1bf18923efcc27501a`.

## Use cases

### Trusted documents

The [gqlhash-proxy](cmd/gqlhash-proxy/README.md) implements [trusted documents](https://benjie.dev/graphql/trusted-documents), also known as persisted queries or a query allowlist, keeps the hash of every document it accepts and rejects everything else. The client sends a document, the server hashes it and looks the hash up.

The hash ignores formatting, so a client that reformats, minifies or re-indents a document keeps the hash it was registered under. Without that, the allowlist has to store the document byte for byte and every layout change is a new entry.

### Cache keys

The hash of a document is a key for a query plan cache or a response cache. Two clients that send the same document formatted differently share the entry.

Keep the default `-ignore=nothing`: under the other modes documents differing in their values hash alike, and a cache would answer one with another's response. The hash covers the document alone, so a response cache key needs the variables and the operation name too.

### Grouping operations

With [`-ignore=inputs`](#ignoring-input-values) documents that differ only in their literal values share a hash, which groups them in logs and metrics by shape rather than by argument.

## Installation

### Homebrew

```sh
brew tap romshark/tools
brew install gqlhash       # the hashing command
brew install gqlhash-proxy # the allowlist-firewall proxy
```

### Compiled Binary

Download a compiled binary from [GitHub Releases](https://github.com/romshark/gqlhash/releases).

### From Source

```sh
go install github.com/romshark/gqlhash/v2/cmd/gqlhash@latest
```

```sh
go install github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy@latest
```

This requires the latest version of [Go](https://go.dev).

## Usage: gqlhash

> [!IMPORTANT]
> The gqlhash CLI spawns a process per invocation. It's for scripts, CI pipelines and local use, not for a per-request path. Use the [gqlhash-proxy](cmd/gqlhash-proxy/README.md) for filtering incoming requests. A Go server may use the package functions ([Compare](https://pkg.go.dev/github.com/romshark/gqlhash/v2#Compare), [AppendHash](https://pkg.go.dev/github.com/romshark/gqlhash/v2#AppendHash)).

gqlhash reads the document from stdin until EOF and prints its SHA-256 hash as a hexadecimal string to stdout:

```sh
# prints: d592c23e0c362a3a49b4c4b18316d9bfc5bda2ce7577b0925d25c4b4cba9c2ec
echo '{foo bar}' | gqlhash
```

To print the version:

```sh
gqlhash -version
```

```
gqlhash v2.0.0
Copyright (c) 2026 Roman Scharkov (github.com/romshark/gqlhash)
MIT License
```

`gqlhash-proxy -version` answers the same way, so a script reads the version off either with `-version | head -1`. For the build behind it — the Go version, the module and every dependency — ask the Go toolchain, which prints it for any Go binary:

```sh
go version -m $(command -v gqlhash)
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
# prints: 1ZLCPgw2KjpJtMSxgxbZv8W9os51d7CSXSXEtMupwuw=
echo '{foo bar}' | gqlhash -format base64
```

`base64url` avoids the `+` and `/` characters, which makes it the format for a URL, a header or a file name. This digest carries a `+`, where the two differ:

```sh
# prints: u3Pd9Iuuyzg+q1CF5y6zJa35kLIEs66EsP6CrHfUcE0=
echo '{foo}' | gqlhash -format base64

# prints: u3Pd9Iuuyzg-q1CF5y6zJa35kLIEs66EsP6CrHfUcE0=
echo '{foo}' | gqlhash -format base64url
```

### Hash Function

The supported hash functions:

- `sha2` (SHA-256)
- `sha3` (SHA3-512)
- `blake2b` (unkeyed)
- `blake2s` (unkeyed)
- `blake3` (unkeyed, 256 bits)
- `sha1`
- `md5`
- `fnv` (FNV-1, 64 bits)
- `fnv1a` (FNV-1a, 64 bits)
- `xxh64` (XXH64, unseeded)
- `crc32` (IEEE polynomial)
- `crc64` (ISO polynomial, defined in ISO 3309)

The default is `sha2`, which is also the [proxy's](cmd/gqlhash-proxy/README.md) and the narrowest thing an allowlist or a persisted-query registry needs of a hash: collision resistance. `md5` and `sha1` are broken and `crc32`, `crc64`, `fnv`, `fnv1a` and `xxh64` are collidable by construction — reach for those to group or bucket documents, never to decide whether one may run.

`-hash` selects another one:

```sh
# prints: 9df882f0d75c115d9587c6afd10b6fbeb8d8865b75db3be73e079513667739e9
echo '{foo bar}' | gqlhash -hash blake3
```

<details>
<summary>Performance</summary>

Hashing `testdata/big.graphql` (2854 bytes), sorted fastest first.

| `-hash`   | time    | throughput |
| --------- | ------- | ---------- |
| `xxh64`   | 2.04 µs | 1336 MB/s |
| `crc32`   | 2.10 µs | 1297 MB/s |
| `sha1`    | 2.39 µs | 1137 MB/s |
| `sha2`    | 2.40 µs | 1135 MB/s |
| `crc64`   | 2.72 µs |  999 MB/s |
| `blake2b` | 3.26 µs |  834 MB/s |
| `fnv`     | 3.57 µs |  762 MB/s |
| `fnv1a`   | 3.62 µs |  751 MB/s |
| `blake3`  | 3.77 µs |  721 MB/s |
| `md5`     | 3.93 µs |  693 MB/s |
| `blake2s` | 4.06 µs |  670 MB/s |
| `sha3`    | 4.85 µs |  561 MB/s |

Measured with `go test . -bench BenchmarkHashFunctions` on an Apple M4 Pro, Go 1.26.5, `GOMAXPROCS=1`, over 8 runs.
</details>

### Depth Limit

`-depth-limit` is how deeply selection sets, list values and input object values may nest before a document is refused. The default is 128, past what a document written for an API reaches and far below what one costs to attack with. Below 1 takes the default.

```sh
echo '{ a { b { c } } }' | gqlhash -depth-limit 2   # too deep
```

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

## Usage: Proxy

See [cmd/gqlhash-proxy/README.md](cmd/gqlhash-proxy/README.md) for how to use the `gqlhash-proxy` to protect your GraphQL API using an allowlist of queries.

## Performance

Measured against two references across the benchmark documents:

- Hashing the document bytes directly with SHA1, which does no parsing: gqlhash takes ~4x as long (min ~2.2x, max ~7.6x).
- Parsing the document into an AST with [vektah/gqlparser/v2](https://github.com/vektah/gqlparser): gqlhash takes ~1/77 of the time (min ~1/20, max ~1/182). gqlhash allocates nothing, gqlparser/v2 allocates hundreds of times per document.

<details>
<summary>Results</summary>

```
goos: darwin
goarch: arm64
pkg: github.com/romshark/gqlhash/v2
cpu: Apple M4 Pro
BenchmarkReferenceSHA1/blockstring/minified/direct         	25799564	        45.95 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash/nothing         	 6051105	       197.0 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash/inputs          	 7272676	       169.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/gqlhash/variables       	 7205239	       170.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/minified/vektah                  	   81962	     14455 ns/op	   10905 B/op	     195 allocs/op

BenchmarkReferenceSHA1/blockstring/formatted/direct                 	25974072	        46.24 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash/nothing        	 5082355	       231.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash/inputs         	 5900920	       200.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/gqlhash/variables      	 5972416	       198.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/blockstring/formatted/vektah                 	   83458	     14503 ns/op	   10953 B/op	     195 allocs/op

BenchmarkReferenceSHA1/tiny/minified/direct                         	34142316	        35.10 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash/nothing                	15186620	        75.87 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash/inputs                 	15861388	        77.08 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/gqlhash/variables              	15664713	        77.08 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/minified/vektah                         	   88830	     13835 ns/op	    9449 B/op	     174 allocs/op

BenchmarkReferenceSHA1/tiny/formatted/direct                        	35161786	        34.22 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash/nothing               	14199922	        81.97 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash/inputs                	14837398	        81.67 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/gqlhash/variables             	14761720	        81.73 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/tiny/formatted/vektah                        	   90900	     13597 ns/op	    9449 B/op	     174 allocs/op

BenchmarkReferenceSHA1/medium/minified/direct                       	14672617	        83.77 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash/nothing              	 2948643	       406.8 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash/inputs               	 3523322	       341.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/gqlhash/variables            	 3515204	       347.9 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/minified/vektah                       	   61765	     20165 ns/op	   17361 B/op	     285 allocs/op

BenchmarkReferenceSHA1/medium/formatted/direct                      	 8286560	       147.6 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash/nothing             	 2219371	       523.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash/inputs              	 2614382	       456.7 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/gqlhash/variables           	 2594966	       453.4 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/medium/formatted/vektah                      	   60288	     20314 ns/op	   17977 B/op	     300 allocs/op

BenchmarkReferenceSHA1/big/minified/direct                          	 2025945	       588.2 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash/nothing                 	  570358	      2051 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash/inputs                  	  731978	      1641 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/gqlhash/variables               	  823298	      1469 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/minified/vektah                          	   22948	     51611 ns/op	   53360 B/op	     839 allocs/op

BenchmarkReferenceSHA1/big/formatted/direct                         	 1339686	       896.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash/nothing                	  452890	      2646 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash/inputs                 	  534379	      2184 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/gqlhash/variables              	  585877	      2028 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/big/formatted/vektah                         	   22112	     53901 ns/op	   54880 B/op	     877 allocs/op

BenchmarkReferenceSHA1/nesting_attack/minified/direct               	 1547923	       781.1 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/minified/gqlhash/nothing      	  204498	      5959 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/minified/gqlhash/inputs       	  252505	      4723 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/minified/gqlhash/variables    	  263535	      4521 ns/op	       0 B/op	       0 allocs/op

BenchmarkReferenceSHA1/nesting_attack/formatted/direct              	  746440	      1646 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/formatted/gqlhash/nothing     	  141748	      8337 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/formatted/gqlhash/inputs      	  161920	      7394 ns/op	       0 B/op	       0 allocs/op
BenchmarkReferenceSHA1/nesting_attack/formatted/gqlhash/variables   	  159295	      7335 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/romshark/gqlhash/v2	68.329s
```

</details>

## Known Limitations

### Descriptions are accepted where some parsers refuse them

The specification allows a description on an operation, a fragment and a variable definition, and this parser takes them. `vektah/gqlparser`, which many Go servers use, refuses all three. A document carrying one hashes and is forwarded here, and the API behind it may answer a syntax error.

### Numbers are hashed as written

Formatting is left out of the hash and values are not: `1.0`, `1.00`, `1e2` and `100.0` each hash differently, where a reformatted document hashes the same. A client whose serializer rewrites a value writes a document the allowlist no longer holds, so pin what generates the documents rather than the numbers they carry. `-ignore=inputs` leaves values out entirely, which makes this moot at the cost of hashing by shape.

### Order of Operations, Selections and Arguments

Everything is hashed in the order it appears, so moving anything around changes the hash:

```graphql
{ user { id name } }
{ user { name id } } # a different hash
```

```graphql
{ user(id: 1, role: ADMIN) { name } }
{ user(role: ADMIN, id: 1) { name } } # a different hash
```

```graphql
{ search(where: {name: "ada", role: ADMIN}) { id } }
{ search(where: {role: ADMIN, name: "ada"}) { id } } # a different hash
```

```graphql
query A { a } query B { b }
query B { b } query A { a } # a different hash
```

Fragment spreads and fragment definitions are hashed as they appear, not inlined. A document using a named fragment produces a different hash than its inlined equivalent, although both select the same fields:

```graphql
{ user { ...userFields } }
fragment userFields on User { id name }
```

```graphql
{ user { id name } }
```

Both would cost a pass over the document that hashing doesn't otherwise need — throughput spent on every request to buy something an allowlist never asks for, since a client sends the document it registered.

## Development

See [DEVELOPMENT.md](DEVELOPMENT.md) for how to build and test this repository.
