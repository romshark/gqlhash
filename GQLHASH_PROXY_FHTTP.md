# gqlhash-proxy-fhttp

Two commands, same flags, same decision, different HTTP implementation:

| | |
| --- | --- |
| `gqlhash-proxy` | net/http |
| `gqlhash-proxy-fhttp` | [valyala/fasthttp](https://github.com/valyala/fasthttp) v1.73.0 |

Only the command naming fasthttp links it. `gqlhash-proxy` is 12.24 MB stripped and contains no fasthttp symbol; `gqlhash-proxy-fhttp` is 15.03 MB.

Both drive the same `proxy.Core`, so the allowlist, hashing, `-ignore`, `-allow-batch` and `-opaque-errors` behave identically. One parity suite runs every assertion against both.

Measurements below: Xeon w5-2455X, 24 cores, loopback, wrk at 200 connections, 58-byte upstream answer.

## Throughput

Each command at its own best `GOGC`.

| forwarded | req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `gqlhash-proxy`, `GOGC=100` | 160,434 | 5.16 ms | 51 / 49 MB |
| `gqlhash-proxy`, `GOGC=800` | 210,260 | 4.40 ms | 166 / 156 MB |
| `gqlhash-proxy-fhttp`, `GOGC=100` | 312,211 | 2.34 ms | 35 / 33 MB |
| **`gqlhash-proxy-fhttp`, `GOGC=400`** | **390,966** | **1.90 ms** | **55 / 49 MB** |

| rejected | req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `gqlhash-proxy`, `GOGC=100` | 515,733 | 1.67 ms | 50 / 40 MB |
| **`gqlhash-proxy`, `GOGC=800`** | **629,780** | 0.98 ms | 159 / 106 MB |
| `gqlhash-proxy-fhttp`, any `GOGC` | 603,278 | **0.57 ms** | 35 / 35 MB |

- Forwarding: fasthttp is ~86% faster on a third of the memory. It replaces `httputil.ReverseProxy` and `http.Transport`; no `GOGC` value closes that gap.
- Rejecting: a GC-tuned `gqlhash-proxy` is 4% faster on throughput. fasthttp wins the tail, 0.57 ms against 0.98 ms, and the memory, 35 MB against 159 MB peak.
- `GOGC` is worth ~25% to fasthttp when forwarding. On the rejected path fasthttp is flat across it.

## Size

`gqlhash-proxy-fhttp` is 2.79 MB larger. Both binaries link net/http, which serves the control port under either command, so the difference is fasthttp and its dependencies on top. About 1 MB of that is brotli and klauspost/compress, compression neither proxy uses.

fasthttp is not the heavier of the two libraries: a minimal net/http server is 5.77 MB and a minimal fasthttp one 5.64 MB. `gqlhash-proxy` carries no fasthttp symbol and does not list it in its dependency graph.

## Costs

### No cancellation on client disconnect

In fasthttp v1.73.0, `RequestCtx.Done()` closes only on server shutdown, never on client disconnect, and `RequestCtx.Deadline()` reports none. Two consequences, both on the forwarded path:

- net/http cancels the upstream request when the client goes away. fasthttp cannot, so a client that connects, sends and hangs up still costs the upstream a full round trip. In front of an API, that is an amplification path.
- The "client left before the answer" branch has no equivalent. Abandoned requests count as upstream errors.

This is the disconnect signal specifically, not request-level control in general: fasthttp has `DoTimeout` and `DoDeadline`, and the forward is issued through `DoTimeout` under `-upstream.timeout`, as it is under net/http. A forward that outlives its budget is cut off either way. What cannot be cut off early is one whose client has already left.

### Framing is normalized

An allowed document sent with `Transfer-Encoding: chunked`, as the upstream receives it:

| | upstream sees |
| --- | --- |
| `gqlhash-proxy` | `Transfer-Encoding: chunked` |
| `gqlhash-proxy-fhttp` | `Content-Length: 96` |

Both answer 200. fasthttp's framing is unambiguous, but it differs: an upstream that treats chunked bodies differently sees a different request.

### Added headers

- fasthttp sets `User-Agent: fasthttp` when the request carried none.
- net/http adds `Accept-Encoding: gzip` and decompresses transparently. fasthttp does not, so an upstream that would compress is not asked to.

### HTTP/2

fasthttp is HTTP/1.1 on both sides. `gqlhash-proxy-fhttp` refuses `-upstream.http2`:

```
-upstream.http2 can't be served by gqlhash-proxy-fhttp, which speaks HTTP/1.1 only.
Terminate HTTP/2 ahead of this proxy, at the load balancer or the ingress,
and let it reach the upstream over HTTP/1.1. Set -upstream.http2=false to run
with this command.
```

Unset, the flag defaults to true and is forced off, so the startup log reports what is served. Neither command serves HTTP/2 to clients: there are no TLS flags, so h2 is never negotiated on the listening side.

### Protocol upgrades

Not forwarded, under either command: the proxy reads and hashes every request body.

### Buffered bodies

fasthttp reads the whole body before the handler runs, so `-server.max-body` is a memory bound rather than a limit applied to a stream. The proxy buffers every body to hash it under either command, so the bound is the same size either way. Bodies past it are refused before the handler; `ErrorHandler` answers with the same 413 and error envelope `gqlhash-proxy` gives.

## HTTP/1.1 conformance

fasthttp's parser is younger and less exercised than net/http's. Versions 1.0.0–1.65.0 were vulnerable to request smuggling through inconsistent `Content-Length` / `Transfer-Encoding` handling ([AIKIDO-2025-10638](https://intel.aikido.dev/cve/AIKIDO-2025-10638)); there is an older path traversal ([CVE-2022-21221](https://github.com/advisories/GHSA-fx95-883v-4q4h)). v1.73.0 is past both and `govulncheck` reports nothing against it. That is no *known* problem, not no problem.

Ambiguous framing over raw sockets:

| request | `gqlhash-proxy` | `gqlhash-proxy-fhttp` |
| --- | --- | --- |
| `Content-Length` + `Transfer-Encoding` | 400 | 400 |
| two conflicting `Content-Length` | 400 | 400 |
| chunked body then a second request | both parsed | both parsed |
| space before the header colon | 400 | 400 |
| bare LF line endings | 200, accepted | connection closed |

The first two are covered by a parity test that sends them over a raw socket and fails if either command answers 2xx or lets anything reach the upstream. Bare LF is the one divergence, and fasthttp is the stricter. A front end more lenient than what stands behind it is how desync starts, so reason about that pair rather than this proxy alone.

Under either command, a request smuggled past a front end still has to hash to something on the allowlist to reach the upstream.

## Choosing

`gqlhash-proxy-fhttp` for forwarded volume or a small memory footprint.

`gqlhash-proxy` when a client hanging up should stop upstream work, when parser agreement with something in front matters more than throughput, or when the rejected path is what you are sized for. It is also the default: its parser has two decades of adversarial attention on it.

Switching is running the other binary with the same flags.
