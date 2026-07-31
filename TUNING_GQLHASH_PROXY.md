# Tuning gqlhash-proxy

[gqlhash-proxy](cmd/gqlhash-proxy/README.md) has two paths: a document on the allowlist is forwarded upstream, anything else is rejected without leaving the proxy.

- [PERFORMANCE.md](PERFORMANCE.md) — where the CPU goes, and why this is the list.
- [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) — the fasthttp build, which moves more than any setting here, and which is **experimental and not for production use**.

## Measured

`go run ./internal/cmd/loadtest -duration 20s -connections 200` on a Xeon w5-2455X — 12 physical cores, 24 hardware threads — loopback, 58-byte upstream answer. Three runs within 1%:

| | rejected | forwarded |
| --- | --- | --- |
| req/s | ~630,000 | ~160,000 |
| median | 170 µs | 1.14 ms |
| p90 | 1.23 ms | 2.93 ms |
| p99 | 2.43 ms | 4.95 ms |
| CPU per request | ~25 µs | ~81 µs |
| cores held | 15.8 of 24 | 13.0 of 24 |
| RSS peak / mean | 52 / 41 MB | 52 / 50 MB |

A rejection costs a third of a forward and answers in a seventh of the time: it never opens the upstream connection. Both figures are the machine's, not the proxy's — wrk holds cores beside it, and the upstream holds more than wrk does on the forwarded row. The run prints all three; read it as the proxy's number only while the balance it reports leaves the machine something.

## Settings

The forward is what needs tuning. `GOGC` moves the rejected path further in percentage terms, but a rejection already outruns the upstream behind it by two orders of magnitude, so the headroom it buys is headroom you had.

**`-upstream.max-idle-conns-per-host`** — at or above peak concurrency. It caps connections kept, not opened; past the cap the surplus is redialed per request. The default of 64 is a cliff. Raise `-upstream.max-idle-conns` with it.

**`GOGC=400`**, with `GOMEMLIMIT` behind it. Forwarding allocates per request, so collection is most of what the path spends. The memory traded away is steady-state, not a spike: under sustained forwarding the proxy lives at its heap ceiling.

Same command, which sizes the pool to the connections it drives:

| | forwarded req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `GOGC=100`, default | ~160,000 | 4.95 ms | 52 / 50 MB |
| `GOGC=400` | ~201,000 | 4.62 ms | 103 / 98 MB |
| `GOGC=800` | ~211,000 | 4.27 ms | 168 / 160 MB |

- The return flattens at 400. 800 buys 5% for another 65 MB.
- **The rejected path gains more than the forwarded one does**, rising from ~630,000 to ~928,000 over the same range — 47%, against 31% forwarding. Collection is a fifth of that path too, and none of it is the handler's; see [PERFORMANCE.md](PERFORMANCE.md#where-the-cpu-goes).
- Peak and mean part company on the rejected path only: 202 vs 111 MB at `GOGC=800`, against 5% apart when forwarding. A rejection allocates little enough to sit well under the collector's target.

The pool is the other half and the load test cannot show it: it always sizes the pool to its connections, so reproducing the default means running the proxy by hand. Left at 64 against 200 connections, the forwarded path drops to roughly two thirds of the first row.

The table above is 200 connections. **Size `GOMEMLIMIT` against the connections you accept, not the rate you serve** — RSS follows concurrency, and `GOGC` multiplies whatever that comes to. `GOGC=800` costs ~170 MB at 200 connections and ~590 MB at 1,000; the default costs 52 MB and 153 MB over the same pair, and reaches 1.25 GB at 10,000. Throughput peaks near 1,000 connections and falls off past it, so there is nothing to buy by sizing for more than you will hold. [PERFORMANCE.md](PERFORMANCE.md#connections) has the sweep.

Not worth tuning: `-hash`, where `sha2` and `blake3` land within 0.3% on both paths, and `-upstream.http2`, which an `http` upstream ignores.

## One proxy, many backends

A GraphQL API doing real resolution serves 1,000–20,000 req/s. One proxy fronts tens of them and is mostly idle on the forwarded path.

**Forwarded throughput is rarely the constraint.** A second proxy is for redundancy, not capacity. The proxy's speed pays off on the *rejected* path, where a flood is absorbed and never reaches an API that could serve a fraction of it. Tail latency and memory are what matter there.

**A pool that never turns over pins itself.** Several backends behind one name — a Kubernetes Service, a DNS name with several A records — are balanced per connection, not per request. Under sustained load connections never go idle, so a backend added afterwards takes no traffic. A large `-upstream.max-idle-conns-per-host` makes it worse.

`-upstream.max-conn-lifetime` fixes that. Off by default, since a single upstream has nothing to rebalance onto:

```sh
gqlhash-proxy -upstream.max-conn-lifetime 5m ...
```

The two commands reach it differently:

- `gqlhash-proxy-fhttp` retires a connection by its own age, once the request using it finishes.
- `gqlhash-proxy` has no such setting in `http.Transport`, so it closes *idle* connections on that interval instead. Never one in use; what is closed is redialed, which resolves the name afresh.

Both turn the pool over. Only the fasthttp one is a strict per-connection lifetime. Cost is one handshake per connection per interval — at 512 connections and 5m, under two dials a second.

## Measuring

`go run ./internal/cmd/loadtest` drives both paths end to end and reports, per path, wrk's throughput and latency beside the CPU the proxy spent and the memory it held. Needs [wrk](https://github.com/wg/wrk). `go test -bench BenchmarkProxy ./internal/app/proxy` measures the same paths without a generator.

Memory is sampled per run, so the two paths are comparable. The mean is what to size against; the peak is the highest sample, so headroom rather than a guaranteed ceiling. Every run is checked against the proxy's decision counters and fails if anything was answered the wrong way.

Three things get a number like this wrong more often than the proxy does:

- **Generator cost.** A generator too small to saturate the proxy reports a fraction of the throughput while the proxy waits on it. This is the one that bites hardest, and every number on this page was once wrong because of it: at four wrk threads the rejected path read ~562,000 req/s against the ~722,000 it holds, and the fasthttp build ~653,000 against ~1,358,000. **The faster the thing measured, the more it is understated**, which is exactly backwards from what a comparison needs. `-threads` defaults to a third of the machine now, and every run prints what the proxy, the generator and the upstream each held so a starved run is visible in its own output.
- **The connection count.** Too few and the number is the concurrency: 50 cannot pass ~50,000 req/s against a millisecond forward, whatever the proxy does. Too many and it is the queue: at 10,000 the latency is queue depth and the proxy holds fewer cores than it does at 1,000. 200 to 1,000 is where this proxy is the thing being measured, see [PERFORMANCE.md](PERFORMANCE.md#connections).
- **Closed-loop latency.** wrk stops sending while the proxy is stalled and never records the stall. Read its distribution as the shape of a healthy run, not a service level. A constant-arrival-rate generator such as oha or vegeta is what a real latency figure needs.
