[![Coverage Status](https://coveralls.io/repos/github/romshark/gqlhash/badge.svg?branch=main)](https://coveralls.io/github/romshark/gqlhash?branch=main)
![License](https://img.shields.io/github/license/romshark/gqlhash)

[![GitHub release (latest by date)](https://img.shields.io/github/v/release/romshark/gqlhash)](https://github.com/romshark/gqlhash/releases)
[![Awesome GraphQL](https://img.shields.io/badge/Awesome-GraphQL-%23e535ab?logo=graphql&logoColor=white)](https://github.com/chentsulin/awesome-graphql?tab=readme-ov-file#tools---miscellaneous)
[![GoDoc](https://godoc.org/github.com/romshark/gqlhash/v2?status.svg)](https://pkg.go.dev/github.com/romshark/gqlhash/v2)

# gqlhash

gqlhash generates SHA1 ([and other](#hash-function)) hashes from GraphQL [executable documents](https://spec.graphql.org/September2025/#sec-Executable-Definitions) ignoring comments, differences in formatting, and optionally input values and variables.

It's shipped as:
1. The Go package [github.com/romshark/gqlhash/v2](https://pkg.go.dev/github.com/romshark/gqlhash/v2) for fast GraphQL request document hashing.
2. `github.com/romshark/gqlhash/v2/cmd/gqlhash` a [CLI tool](#usage-gqlhash) for scripts and CI pipelines.
3. `github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy`, a fast [allowlist-firewall proxy](#usage-proxy) you can put in front of a GraphQL API.
4. `github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy-fhttp`, the same proxy served with [fasthttp](GQLHASH_PROXY_FHTTP.md) rather than Go's standard `net/http`.

Generating a gqlhash is [faster](#performance) than parsing a document into an AST and comparing the ASTs.

On a 24-core Xeon `gqlhash-proxy` turns away **~632,000** unknown documents a second at a median of **155 µs**, and forwards **~209,000** allowed ones. A rejection costs a third of a forward and never opens the upstream connection. The fasthttp build forwards ~392,000.

`gqlhash-proxy-fhttp` is another implementation based on [valyala/fasthttp](https://github.com/valyala/fasthttp), an alternative HTTP/1.1 only implementation.

Each command at its best, wrk at 200 connections, median of three runs:

| | `gqlhash-proxy` | `gqlhash-proxy-fhttp` |
| --- | --- | --- |
| rejected req/s | **~632,000** | ~604,000 |
| rejected, median / p99 | **155 µs** / 0.97 ms | 170 µs / **0.56 ms** |
| forwarded req/s | ~209,000 | **~392,000** |
| forwarded, median / p99 | 0.96 ms / 4.51 ms | **392 µs** / **1.90 ms** |
| CPU per rejection / forward | 22 / 65 µs | **19 / 28 µs** |
| RSS peak | 161 MB | **57 MB** |
| tuned with | `GOGC=800` | `GOGC=400` |

`gqlhash-proxy-fhttp` is the same proxy and the same decision. Reach for it when forwarded volume or memory is genuinely the problem you have, and take it knowing what it trades for that:

- **No cancellation on client disconnect.** A client that hangs up still costs the upstream a full round trip, bounded only by `-upstream.timeout`.
- **HTTP/1.1 only, on both sides.** It refuses `-upstream.http2`; terminate HTTP/2 at the load balancer ahead of it.
- **A less battle-tested parser.** net/http's has taken two decades of adversarial attention, whereas fasthttp's is younger and less exercised.
- **Small differences on the wire.** Chunked bodies arrive upstream as `Content-Length`, and gzip is never requested.

[The full list and what it costs](GQLHASH_PROXY_FHTTP.md). [The numbers and what moves them](TUNING_GQLHASH_PROXY.md), [where the CPU goes](PERFORMANCE.md).

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

The [gqlhash-proxy](#gqlhash-proxy) implements [trusted documents](https://benjie.dev/graphql/trusted-documents), also known as persisted queries or a query allowlist, keeps the hash of every document it accepts and rejects everything else. The client sends a document, the server hashes it and looks the hash up.

The hash ignores formatting, so a client that reformats, minifies or re-indents a document keeps the hash it was registered under. Without that, the allowlist has to store the document byte for byte and every layout change is a new entry.

### Cache keys

The hash of a document is a key for a query plan cache or a response cache. Two clients that send the same document formatted differently share the entry.

Keep the default `-ignore=nothing`: under the other modes documents differing in their values hash alike, and a cache would answer one with another's response. The hash covers the document alone, so a response cache key needs the variables and the operation name too.

### Change detection

Comparing the hash of a committed document against a generated one reports a change in the document and not in its layout, which is what a CI check wants to fail on.

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

```sh
go install github.com/romshark/gqlhash/v2/cmd/gqlhash-proxy-fhttp@latest
```

This requires the latest version of [Go](https://go.dev).

### gqlhash-proxy

```sh
brew tap romshark/tools
brew install gqlhash-proxy
```

## Usage: gqlhash

> [!IMPORTANT]
> The gqlhash CLI spawns a process per invocation. It's for scripts, CI pipelines and local use, not for a per-request path. Use the [gqlhash-proxy](#gqlhash-proxy) for filtering incoming requests. A Go server may use the package functions ([Compare](https://pkg.go.dev/github.com/romshark/gqlhash/v2#Compare), [AppendHash](https://pkg.go.dev/github.com/romshark/gqlhash/v2#AppendHash)).

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

[gqlhash-proxy](#gqlhash-proxy) serves an allowlist of documents in front of a GraphQL API. A request whose document is on the list is forwarded, every other request is rejected with `403 Forbidden` and never reaches the API.

```sh
gqlhash-proxy \
  -server.listen :8080 \
  -upstream.url http://api:4000/graphql \
  -allowlist ./queries \
  -control.listen 127.0.0.1:9090
```

`-server.listen` serves the proxy on every path: the request path is never routed on, and `-upstream.url` is the endpoint the forwarded request reaches. The document is read where [GraphQL over HTTP](https://graphql.github.io/graphql-over-http/draft/) puts it: the `query` parameter of a `GET`, or the `query` member of an `application/json` body. That specification is a working draft and defines neither an `application/graphql` body nor batching; the proxy reads both anyway, the first as the document itself, the second under `-allow-batch`.

`-allowlist` is a directory of `.graphql` and `.gql` files holding the allowed documents. The proxy hashes them itself, so the documents are the source of truth. Formatting and comments may differ between a file and what a client sends. The set of definitions may not: one file is one entry.

A `.graphqls` file in the same directory is read as the schema, and every document is then checked against it: one asking for a field the schema doesn't have is skipped like one that doesn't parse. Without such a file nothing is checked against a schema. Several `.graphqls` files are read as one schema, and a schema that doesn't parse is reported and leaves the documents unchecked rather than unserved.

The proxy rejects ambiguous requests with `400 Bad Request`. Both keys in `{"query":"<allowed>","quer\u0079":"<anything>"}` unescape to `query`. The proxy can't tell which document would reach the API, so it hashes neither and refuses. Same for a GET naming `query` twice, percent-encoded or not.

Two files whose documents hash alike are both skipped: which one a request meant is unknowable, and allowing the wrong one is worse than allowing neither.

A document that doesn't parse is skipped with an error log, at startup and on reload alike, so one broken file doesn't keep the rest from being served. A directory with no usable document serves an empty allowlist, rejects everything.

`-control.listen 127.0.0.1:9090` serves the control server on that address, which is separate from the port that serves traffic. It provides [Prometheus](https://prometheus.io/) metrics on `/metrics` and rereads the allowlist on `POST /reload`.

`/status` answers what the proxy has decided so far: the size of the allowlist, when it was loaded, and the counters for allowed, rejected and malformed requests and upstream failures. Like the metrics it needs no token, and like them it isn't served on the traffic port — it's operational state, not something a client of the API should see.

`GQLHASH_PROXY_CONTROL_TOKEN` requires `Authorization: Bearer <token>` on `/reload`, compared in constant time. Metrics are served without it. There is no flag for the token: a process argument is readable by anyone on the host through `ps` or `/proc/<pid>/cmdline`, the environment of a process isn't.

### Reloading Allowlist

```sh
# Control server host
curl -fsS -X POST localhost:9090/reload
```

Returns:

```json
{
  "documents": {
    "total": 2,
    "files": ["queries/list-users.graphql", "queries/user-with-email.graphql"]
  },
  "skipped": {
    "total": 1,
    "errors": ["queries/get-user.graphql:2:9: unexpected token: malformed number"]
  }
}
```

Every flag of the proxy, with its default:

| Flag | Default | |
| --- | --- | --- |
| `-upstream.url` | required | the GraphQL API a request is forwarded to |
| `-allowlist` | required | the directory the documents are read from |
| `-server.listen` | `:8080` | where the traffic is served |
| `-control.listen` | `127.0.0.1:9090` | where `/metrics`, `/status` and `/reload` are served |
| `-hash` | `sha2` | `sha2`, `sha3`, `blake2b`, `blake2s` or `blake3` |
| `-ignore` | `nothing` | what to leave out of the hash, see [Ignoring Input Values](#ignoring-input-values) |
| `-server.max-body` | 1 MiB | largest request body accepted |
| `-allow-batch` | off | accept batches, where every document has to be allowed |
| `-opaque-errors` | off | answer every rejection with 403 and no detail |
| `-trust-forwarded` | off | keep the `X-Forwarded-*` headers a request arrives with |
| `-log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `-log.json` | on | JSON instead of readable text |
| `-log.requests` | off | log every forwarded request at debug level |
| `-upstream.timeout` | 30s | how long an upstream request may take |
| `-upstream.max-idle-conns-per-host` | 64 | connections kept open to the upstream |
| `-upstream.max-idle-conns` | 256 | ceiling over the per-host pool, 0 for none |
| `-upstream.max-conn-lifetime` | off | retire an upstream connection once it's this old |
| `-upstream.http2` | on | allow HTTP/2 to an `https` upstream |
| `-server.read-header-timeout` | 10s | how long a client may take to send the headers |
| `-server.read-timeout` | 30s | how long a client may take to send the request |
| `-server.write-timeout` | `-upstream.timeout` + 10s | how long answering may take |
| `-server.idle-timeout` | 2m | how long an idle keep-alive connection is held |
| `-server.shutdown-timeout` | 10s | how long the requests in flight are waited for |

`0` leaves any of the timeouts off. Four of them carry a constraint worth knowing:

- `-server.write-timeout` must stay above `-upstream.timeout`, or the proxy cuts off a response the upstream is still allowed to be sending. It follows that flag unless you set it, and a value at or below it is rejected at startup.
- `-server.read-timeout` has to fit `-server.max-body` arriving over the slowest link you serve, and must not be below `-server.read-header-timeout`, which would leave that one without effect.
- `-server.idle-timeout` belongs above the idle timeout of any load balancer in front, or the balancer reuses a connection the proxy is closing.
- `-upstream.max-idle-conns-per-host` is what caps connection reuse: there is one upstream, so every forwarded request draws from that one pool. It belongs at or above the requests you serve at once, or the surplus connections are dialed again per request, see [tuning](TUNING_GQLHASH_PROXY.md).
- `-upstream.max-conn-lifetime` belongs on wherever the upstream is several backends behind one name. They're balanced per connection, so a pool that never turns over never reaches one added after it filled, and a large `-upstream.max-idle-conns-per-host` makes that worse rather than better.
- `gqlhash-proxy-fhttp` takes the same flags and serves them with fasthttp, forwarding around 59% faster on a third of the memory, against cancellation on client disconnect, HTTP/2 and a well-worn HTTP parser. [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) has the whole trade, measured.

`-hash` takes only the collision-resistant functions, unlike `gqlhash`. Rationale: an allowlist's security property is collision resistance, and `crc32`, `crc64`, `fnv`, `fnv1a` and `xxh64` are collidable by construction while `md5` and `sha1` are broken.

`-trust-forwarded` appends the peer to the `X-Forwarded-*` headers instead of replacing them, which a proxy behind a load balancer needs so the API still sees the original client. Set it only there: a client that connects directly can otherwise claim any address.

Exposed on `/metrics` are request counters by decision, upstream errors, the allowlist size and load time, a request duration histogram, and the Go runtime collectors. Rejections are counted, not logged: a flood of them would otherwise write one line each, so those events sit at debug level.

Every flag can be given through the environment instead, as `GQLHASH_PROXY_` followed by its name with the dashes and dots as underscores: `GQLHASH_PROXY_SERVER_MAX_BODY=4096`, `GQLHASH_PROXY_UPSTREAM_URL=http://api:4000/graphql`. A flag given on the command line wins. `gqlhash` reads no environment, so a variable can't quietly change the hashes a pipeline produces.

[TUNING_GQLHASH_PROXY.md](TUNING_GQLHASH_PROXY.md) has the throughput and latency of both paths, what moves the forwarded one, and how to measure it without measuring the load generator instead. `go run ./internal/cmd/loadtest` runs it.

[playground](playground) runs the proxy in front of a sample GraphQL API with `docker compose up --build`, with a few allowed documents and the schema they're checked against.

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

The reason is that hashing order-insensitively and inlining fragment is more work and that would reduce the effectiveness of the proxy firewall.

## Development

### Testing

```sh
make                               # lint, test, report coverage
make test                          # tests alone
make acceptance                    # both proxy binaries, over real HTTP
make acceptance PROXY=./my-proxy   # any implementation of the same contract
```

`./internal/acceptance` starts a real server process and drives it over HTTP, which is what lets a server written in another language be tested the same way. The contract is documented in `internal/acceptance/doc.go`.

### Coverage

`make` reports it, `make cover` on its own. Two runs, since the acceptance suite drives the proxy as a separate process that `-coverprofile` can't see: `cover-unit` reports what the tests reach in process, `cover-servers` what the running servers reach. The second needs an absolute `GOCOVERDIR` and no `-cover` flag, or it silently collects nothing; the Makefile handles both. `make cover-profile` converts the servers' counters into a profile.
