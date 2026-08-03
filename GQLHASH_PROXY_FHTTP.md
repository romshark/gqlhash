# gqlhash-proxy-fhttp

> [!WARNING]
> **`gqlhash-proxy-fhttp` is experimental and is not for production use.**
> Run `gqlhash-proxy` in front of anything that matters.
>
> `gqlhash-proxy-fhttp` exists only as a pure performance experiment and isn't
> recommended to be used for production.

Two commands, same flags, same decision, different HTTP implementation:

| | |
| --- | --- |
| `gqlhash-proxy` | net/http |
| `gqlhash-proxy-fhttp` | [valyala/fasthttp](https://github.com/valyala/fasthttp) v1.73.0 |

Only the command naming fasthttp links it. `gqlhash-proxy` is 12.24 MB stripped and contains no fasthttp symbol; `gqlhash-proxy-fhttp` is 15.03 MB.

Both drive the same `proxy.Core`, so the allowlist, hashing, `-ignore`, `-allow-batch` and `-opaque-errors` behave identically. One parity suite runs every assertion against both.

Measurements below: Xeon w5-2455X — 12 physical cores, 24 hardware threads — loopback, wrk at 200 connections, 58-byte upstream answer. `gqlhash-proxy` is driven at `-threads 8` and `gqlhash-proxy-fhttp` at `-threads 10`, the counts that saturate each. They differ because the faster command needs a bigger generator to be saturated at all; the balance each run prints confirms neither is waiting on wrk. An earlier revision of this page drove both at four threads, which understated `gqlhash-proxy` on the rejected path by 22% and `gqlhash-proxy-fhttp` by 119% — enough to reverse the conclusion, since it read as net/http winning that path by 4%.

## Throughput

| forwarded | req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `gqlhash-proxy`, `GOGC=100` | 160,354 | 4.95 ms | 52 / 50 MB |
| `gqlhash-proxy`, `GOGC=800` | 210,877 | 4.27 ms | 168 / 160 MB |
| `gqlhash-proxy-fhttp`, `GOGC=100` | 308,619 | 2.30 ms | 49 / 44 MB |
| **`gqlhash-proxy-fhttp`, `GOGC=800`** | **389,677** | **1.80 ms** | 144 / 83 MB |

| rejected | req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `gqlhash-proxy`, `GOGC=100` | 630,043 | **2.43 ms** | 52 / 41 MB |
| `gqlhash-proxy`, `GOGC=800` | 927,557 | 2.75 ms | 202 / 111 MB |
| **`gqlhash-proxy-fhttp`, any `GOGC`** | **1,319,581** | 3.04 ms | 49 / 49 MB |

- Forwarding: fasthttp is ~92% faster at the same `GOGC` and ~85% faster with each at its best. It replaces `httputil.ReverseProxy` and `http.Transport`; no `GOGC` value closes that gap.
- Rejecting: **fasthttp is 42% faster than a GC-tuned `gqlhash-proxy` and 2.1× faster than an untuned one**, on a quarter of the memory. It allocates nothing on this path, so the collector never runs and `GOGC` does nothing for it.
- The tail is the one thing fasthttp does not win at 200 connections: 3.04 ms against 2.43 ms. It is serving 2.1× the rate to get there, so the two p99s are not measurements of the same load. At 1,000 connections, where both are past their throughput peak, fasthttp wins it back — 3.56 ms against 6.43 ms, see [PERFORMANCE.md](PERFORMANCE.md#connections).
- `GOGC` is worth ~26% to fasthttp when forwarding, and nothing at all when rejecting.

## Size

`gqlhash-proxy-fhttp` is 2.79 MB larger. Both binaries link net/http, which serves the control port under either command, so the difference is fasthttp and its dependencies on top. About 1 MB of that is brotli and klauspost/compress, compression neither proxy uses.

fasthttp is not the heavier of the two libraries: a minimal net/http server is 5.77 MB and a minimal fasthttp one 5.64 MB. `gqlhash-proxy` carries no fasthttp symbol and does not list it in its dependency graph.

## Costs

### No cancellation on client disconnect

In fasthttp v1.73.0, `RequestCtx.Done()` closes only on server shutdown, never on client disconnect, and `RequestCtx.Deadline()` reports none. Two consequences, both on the forwarded path:

- net/http cancels the upstream request when the client goes away. fasthttp cannot, so a client that connects, sends and hangs up still costs the upstream a full round trip. In front of an API, that is an amplification path.
- The "client left before the answer" branch has no equivalent. Abandoned requests count as upstream errors.

This is the disconnect signal specifically, not request-level control in general: fasthttp has `DoTimeout` and `DoDeadline`, and the forward is issued through `DoTimeout` under `-upstream.timeout`, as it is under net/http. A forward that outlives its budget is cut off either way — except an event stream, which has no budget to outlive under either command, see *Event streams* below. What cannot be cut off early is one whose client has already left.

`-upstream.timeout 0` turns the bound off under both commands. fasthttp has no duration that means "no limit": `DoTimeout` with `0` is a deadline already past, and `MaxConnWaitTimeout` at `0` waits for no connection at all, so this command spells the flag being off as a bound no process outlives. `TestUpstreamTimeoutOff` pins that both serve an API slower than any default.

### Framing is normalized

An allowed document sent with `Transfer-Encoding: chunked`, as the upstream receives it:

| | upstream sees |
| --- | --- |
| `gqlhash-proxy` | `Transfer-Encoding: chunked` |
| `gqlhash-proxy-fhttp` | `Content-Length: 96` |

Both answer 200. fasthttp's framing is unambiguous, but it differs: an upstream that treats chunked bodies differently sees a different request.

### Connection pooling

`-upstream.max-idle-conns-per-host` sizes the pool of connections kept. fasthttp has no separate limit for the ones opened, so the flag caps both, and the surplus waits for a free connection instead of being redialed the way net/http redials one. The wait is bounded by `-upstream.timeout`, so a request over the cap is answered late rather than refused, and past that timeout it is a 504.

`-upstream.max-idle-conns`, the total across hosts, has no fasthttp equivalent and is ignored: one upstream URL is one host, which the per-host flag already bounds.

### A GET carries its document in a 64 KiB request line

fasthttp reads the request line and the headers into a buffer of a fixed size, one per open connection, where net/http grows its own. `gqlhash-proxy-fhttp` sizes it at 64 KiB, so a document sent in the query string is refused past that with a 400, while `gqlhash-proxy` takes what its 1 MiB header limit allows. `-server.max-body` bounds bodies and neither bounds this. A document that large belongs in a POST body under either command.

`-server.read-header-timeout` has no fasthttp equivalent either: `-server.read-timeout` covers the whole request there.

### Added headers

- fasthttp sets `User-Agent: fasthttp` when the request carried none.

Neither command asks for an encoding of its own any more, and neither decodes an answer: what the client asks for is forwarded and what the API answers with arrives untouched.

### Trailers and `HTTP_PROXY`

fasthttp reads the whole answer before writing it, so trailers of the API arrive as headers and `Te: trailers` is dropped. `gqlhash-proxy` relays both. No GraphQL API sends trailers today; relaying them here means moving the forward onto response streaming, which is the buffered copy this command exists for.

**A trailer on the request is the same difference pointing at your API, and it is the sharpest reason not to deploy this build.** A client sending `Transfer-Encoding: chunked` with `X-Hop` after the last chunk gets it delivered to the API as an ordinary header. `gqlhash-proxy` keeps it a trailer, which Go puts in `Request.Trailer` and never in `Request.Header`, so nothing reading headers can be fooled.

It cannot be closed here: `parseTrailer` appends every trailer field to the ordinary header set before the handler runs, declared in `Trailer:` or not, so afterwards nothing can tell one from a header sent in the head. Stripping the declared names closes only the case an attacker would not use. The one sound fix is refusing chunked request bodies outright, on both commands, which costs every client that streams a POST body — including on the command that was never affected.

So: never run this build in front of an API that trusts a header its ingress is supposed to set (`X-Auth-Request-User`, `X-Forwarded-*` beyond what the proxy itself writes, anything an oauth2 sidecar injects). `TestRequestTrailerDoesNotChangeTheDecision` pins the part both commands keep.

`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` are honored by `gqlhash-proxy` and ignored by the fasthttp client. Deploy this command where the API is reached directly.

### HTTP/2

fasthttp is HTTP/1.1 on both sides. `gqlhash-proxy-fhttp` refuses `-upstream.http2`:

```
-upstream.http2 can't be served by gqlhash-proxy-fhttp, which speaks HTTP/1.1 only.
Terminate HTTP/2 ahead of this proxy, at the load balancer or the ingress,
and let it reach the upstream over HTTP/1.1. Set -upstream.http2=false to run
with this command.
```

Unset, the flag defaults to true and is forced off, so the startup log reports what is served.

The listening side differs too, once `-server.tls.cert` and `-server.tls.key` are given: `gqlhash-proxy` negotiates HTTP/2 over ALPN and falls back to HTTP/1.1, where `gqlhash-proxy-fhttp` serves HTTP/1.1 whatever the client offers. Without those flags both serve plain HTTP/1.1, since h2 is never negotiated without TLS.

### Protocol upgrades

Not forwarded, under either command. `Upgrade` and the `Connection` token naming it are dropped with the other hop-by-hop headers on the way out, so the API never learns an upgrade was offered and the request is decided like any other. An upstream that answers `101` anyway is a `502` and an `upstream_errors_total`: nothing offered an upgrade, so relaying it would hand the client a channel to the API on the strength of that answer alone — one hashed document buying an unhashed tunnel.

`gqlhash-proxy-fhttp` had `Upgrade` in its hop-by-hop list from the start, so no offer ever left it; what it did do was relay an unasked-for `101`, which is the same rule from the other side.

### Buffered bodies

fasthttp reads the whole body before the handler runs, so `-server.max-body` is a memory bound rather than a limit applied to a stream. The proxy buffers every body to hash it under either command, so the bound is the same size either way. Bodies past it are refused before the handler; `ErrorHandler` answers with the same 413 and error envelope `gqlhash-proxy` gives.

### An informational answer from the API

`gqlhash-proxy` relays a `1xx` — a `103 Early Hints`, say — and goes on to read the answer behind it. The fasthttp client reads the `1xx` **as** the answer and stops, so the client is left with a hint and no final answer, and a stream behind one is never seen as a stream.

The client cannot be made to read past it from here: the connection belongs to it. No GraphQL API sends early hints today, and `TestStreamAfterAnEarlyHint` pins what both keep — a hint is never served as a complete answer.

### Event streams

An answer that is `text/event-stream` is the one this command does not buffer, so GraphQL over SSE works under both. It costs a second `HostClient` with `StreamResponseBody` and a connection pool of its own, which only a request naming `text/event-stream` in `Accept` draws from; everything else takes the buffered path unchanged.

Two things follow from fasthttp having no per-request deadline that stops at the headers. The streaming client carries no read deadline at all, because fasthttp computes one before reading the headers and it covers the body too — so the wait for the headers is bounded by a timer in `server.within` instead, and a forward that outlives it is *abandoned* rather than cancelled: nothing interrupts a fasthttp client mid-request, so the 504 is answered without it and its request and answer are released when the goroutine ends. And `-server.write-timeout` is replaced per request through the `HeaderReceived` hook, which takes a positive duration only, so "not bounded" is spelled as a bound no process outlives.

The rule both commands keep: an answer whose `Content-Type` is `text/event-stream` is relayed as it arrives, and the deadlines that bound an exchange don't apply to it — `-upstream.timeout` still bounds the wait for the answer's headers, but not the stream, and `-server.write-timeout` doesn't reach it at all. What the client asks for decides how the forward is carried, since an implementation must choose before there is an answer to look at; what the API answers decides how it's relayed, so a client that asks for a stream and receives JSON gets an ordinary, bounded exchange. A stream is timed to its headers rather than to its end, so an hour-long subscription isn't an hour of proxy latency in `request_duration_seconds`.

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

`gqlhash-proxy-fhttp` for throughput or a small memory footprint. It is faster on both paths at every connection count measured, so there is no longer a workload that picks `gqlhash-proxy` on performance.

`gqlhash-proxy` for everything else, which is most of it: when a client hanging up should stop upstream work, when parser agreement with something in front matters, when a request trailer must not arrive as a header, when `HTTP_PROXY` or HTTP/2 upstreams are in play, or when an API may answer `1xx`. It is also the default: its parser has two decades of adversarial attention on it. The costs listed above are the reason to run it, and none of them got cheaper because the other build got faster.

Switching is running the other binary with the same flags.
