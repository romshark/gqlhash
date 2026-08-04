# gqlhash-proxy-fhttp

> [!WARNING]
> **`gqlhash-proxy-fhttp` is experimental and is not for production use.**
> Run `gqlhash-proxy` in front of anything that matters.
>
> `gqlhash-proxy-fhttp` exists only as a pure performance experiment and isn't
> recommended to be used for production.

The same proxy as [`gqlhash-proxy`](../gqlhash-proxy/README.md), served with [valyala/fasthttp](https://github.com/valyala/fasthttp) instead of Go's `net/http`.

**Read [cmd/gqlhash-proxy/README.md](../gqlhash-proxy/README.md) first.** Same flags, same environment variables, same allowlist, same control server, same decision on every request. Both commands drive the same `proxy.Core`, and one acceptance suite runs every rule against both, so anything written there holds here unless this file says otherwise.

```sh
gqlhash-proxy-fhttp \
  -server.listen :8080 \
  -upstream.url http://api:4000/graphql \
  -allowlist ./queries \
  -control.listen 127.0.0.1:9090
```

## What it is for

Benchmarking, and proving the acceptance suite is a contract rather than a description of one implementation. Every rule in the suite runs against both binaries, so a rule that only `net/http` happened to keep shows up as a failure here.

The speed is real: forwarding is around 59% faster on a third of the memory — ~391,000 forwarded requests a second against ~210,000, at a p99 of 1.90 ms against 4.40 ms, with 55 MB peak RSS against 166 MB. On the rejected path a GC-tuned `gqlhash-proxy` is 4% faster on throughput, and this one still wins the tail and the memory.

**None of that is a reason to deploy it.** This proxy exists to stand between the open internet and an API, and the thing on that boundary should be the implementation with two decades of adversarial attention behind its parser. If forwarded volume is genuinely your problem, raise `GOGC` on `gqlhash-proxy` first — that alone is worth ~30% — and read [TUNING_GQLHASH_PROXY.md](../../TUNING_GQLHASH_PROXY.md) before reaching for a different HTTP stack.

## What differs

- **HTTP/1.1 only, on both sides.** `-upstream.http2` is refused at startup with exit 2 rather than accepted and ignored. Terminate HTTP/2 at your ingress. Neither command serves HTTP/2 to clients.
- **No cancellation on client disconnect.** `RequestCtx.Done()` closes only on server shutdown, so a client that hangs up still costs the API a full round trip. `-upstream.timeout` still bounds the forward.
- **A GET's document is capped at 64 KiB**, the size of the per-connection buffer the request line and headers are read into, where `net/http` grows its own to a 1 MiB header limit. A document that large belongs in a POST body under either command.
- **`-server.read-header-timeout` has no equivalent** and is accepted without effect; `-server.read-timeout` covers the whole request here.
- **Framing is normalized.** A chunked request body reaches the API with a `Content-Length`.
- **`User-Agent: fasthttp`** is added where the request carried none.
- **Request trailers reach the API as ordinary headers**, and answer trailers likewise. This is the sharpest of the reasons above not to run this in production: a client can hand your API a header your ingress is supposed to own, and it cannot be fixed on the fasthttp side.
- **`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` are ignored.** Deploy this command where the API is reached directly.
- **`-upstream.max-idle-conns` is ignored**, and `-upstream.max-idle-conns-per-host` caps connections opened as well as kept: the surplus waits for a free one, bounded by `-upstream.timeout`, rather than being redialed.

Event streams are *not* on this list. GraphQL over SSE works identically under both commands — see [Subscriptions](../gqlhash-proxy/README.md#subscriptions) — even though every other answer is buffered here.

[GQLHASH_PROXY_FHTTP.md](../../GQLHASH_PROXY_FHTTP.md) has the whole trade with the measurements behind it, including the binary-size accounting and fasthttp's HTTP/1.1 conformance record.
