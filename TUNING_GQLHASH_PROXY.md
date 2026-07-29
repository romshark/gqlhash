# Tuning gqlhash-proxy

[gqlhash-proxy](README.md#usage-proxy) has two paths: a document on the allowlist is forwarded upstream, anything else is rejected without leaving the proxy.

- [PERFORMANCE.md](PERFORMANCE.md) — where the CPU goes, and why this is the list.
- [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) — the fasthttp build, which moves more than any setting here.

## Measured

`go run ./internal/cmd/loadtest -duration 20s -connections 200` on a Xeon w5-2455X, 24 cores, loopback, 58-byte upstream answer. Three runs within 2%:

| | rejected | forwarded |
| --- | --- | --- |
| req/s | ~515,000 | ~163,000 |
| median | 163 µs | 1.10 ms |
| p90 | 0.93 ms | 2.9 ms |
| p99 | 1.68 ms | 4.9 ms |
| CPU per request | ~27 µs | ~79 µs |
| cores held | 13.9 of 24 | 12.8 of 24 |
| RSS peak / mean | 49 / 40 MB | 51 / 50 MB |

A rejection costs a third of a forward and answers in a seventh of the time: it never opens the upstream connection. Both figures are the machine's, not the proxy's — wrk holds cores beside it.

## Settings

Only the forward is worth tuning. The rejection already outruns the upstream behind it.

**`-upstream.max-idle-conns-per-host`** — at or above peak concurrency. It caps connections kept, not opened; past the cap the surplus is redialed per request. The default of 64 is a cliff. Raise `-upstream.max-idle-conns` with it.

**`GOGC=400`**, with `GOMEMLIMIT` behind it. Forwarding allocates per request, so collection is most of what the path spends. The memory traded away is steady-state, not a spike: under sustained forwarding the proxy lives at its heap ceiling.

Same command, which sizes the pool to the connections it drives:

| | forwarded req/s | p99 | RSS peak / mean |
| --- | --- | --- | --- |
| `GOGC=100`, default | ~160,000 | 5.2 ms | 51 / 49 MB |
| `GOGC=400` | ~207,000 | 4.4 ms | 102 / 95 MB |
| `GOGC=800` | ~210,000 | 4.4 ms | 166 / 156 MB |

- The return flattens at 400. 800 buys 1% for another 64 MB.
- The rejected path rises from ~516,000 to ~630,000 over the same range.
- Peak and mean part company on the rejected path only: 159 vs 106 MB at `GOGC=800`, against 6% apart when forwarding. A rejection allocates little enough to sit well under the collector's target.

The pool is the other half and the load test cannot show it: it always sizes the pool to its connections, so reproducing the default means running the proxy by hand. Left at 64 against 200 connections, the forwarded path drops to roughly two thirds of the first row.

The table above is 200 connections. **Size `GOMEMLIMIT` against the connections you accept, not the rate you serve** — RSS follows concurrency, and `GOGC` multiplies whatever that comes to. `GOGC=800` costs 161 MB at 200 connections and 570 MB at 1,000; the default costs 50 MB and 153 MB over the same pair, and reaches 1.2 GB at 10,000. Throughput peaks near 1,000 connections and falls off past it, so there is nothing to buy by sizing for more than you will hold. [PERFORMANCE.md](PERFORMANCE.md#connections) has the sweep.

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

- **Generator cost.** A generator costing several times what the proxy costs per request reports a fraction of the throughput while the proxy waits on it, and nothing in the output says so. Hence wrk, and a second machine for anything published.
- **The connection count.** Too few and the number is the concurrency: 50 cannot pass ~50,000 req/s against a millisecond forward, whatever the proxy does. Too many and it is the queue: at 10,000 the latency is queue depth and the proxy holds fewer cores than it does at 1,000. 200 to 1,000 is where this proxy is the thing being measured, see [PERFORMANCE.md](PERFORMANCE.md#connections).
- **Closed-loop latency.** wrk stops sending while the proxy is stalled and never records the stall. Read its distribution as the shape of a healthy run, not a service level. A constant-arrival-rate generator such as oha or vegeta is what a real latency figure needs.
