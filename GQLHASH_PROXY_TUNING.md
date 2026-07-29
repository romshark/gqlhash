# Tuning gqlhash-proxy

[gqlhash-proxy](README.md#usage-proxy) has two paths: a document on the allowlist is forwarded upstream, anything else is rejected without leaving the proxy. [PERFORMANCE.md](PERFORMANCE.md) has where the time goes and why this is the list of what's worth changing.

## Measured

`scripts/loadtest.sh wrk 20s 200` on a Xeon w5-2455X of 24 cores, over loopback, against an upstream answering a fixed 58 bytes. Three runs within 2%:

| | rejected by the proxy | forwarded upstream |
| --- | --- | --- |
| req/s | ~514,000 | ~161,000 |
| median | 163 µs | 1.10 ms |
| p90 | 0.93 ms | 2.9 ms |
| p99 | 1.67 ms | 5.0 ms |
| CPU per request | ~27 µs | ~80 µs |
| cores the proxy held | 14.1 of 24 | 12.9 of 24 |

A rejection costs a third of a forward and answers in a seventh of the time: it never opens the upstream connection. Both figures are the machine's rather than the proxy's, since wrk holds cores beside it.

## Tuning

Only the forward is worth tuning; the rejection already outruns the upstream behind it. Two settings move it.

**`-upstream.max-idle-conns-per-host` belongs at or above peak concurrency.** It caps connections kept, not opened: past the cap the surplus is redialed per request. The default of 64 is a cliff. Raise `-upstream.max-idle-conns` with it.

**`GOGC=400`, with `GOMEMLIMIT` behind it.** Forwarding allocates per request, so collection is most of what the path spends.

Same machine, 200-byte answers, cumulative:

| | forwarded req/s | p99 | peak RSS |
| --- | --- | --- | --- |
| defaults | ~110,000 | 10.9 ms | 50 MB |
| `-upstream.max-idle-conns-per-host 512` | ~160,000 | 5.2 ms | 49 MB |
| `GOGC=400` | ~189,000 | 4.4 ms | 96 MB |
| `GOGC=800` | ~197,000 | 4.0 ms | 156 MB |

The return flattens at 400: 800 buys 4% for 60 MB. The rejected path rises from ~470,000 to ~610,000 over the same range.

The first row is the proxy's default pool, not the load test's — `scripts/loadtest.sh` sizes the pool to the connections it drives, so the figures above are its second row.

Not worth tuning: `-hash`, where `sha2` and `blake3` land within 0.3% on both paths, and `-upstream.http2`, which an `http` upstream ignores.

## Measuring

`scripts/loadtest.sh [generator] [duration] [connections]` drives both paths end to end — an upstream API, the proxy in front of it — and reports what the proxy spent beside the throughput. It takes wrk, oha, h2load or vegeta, whichever is installed. `go test -bench BenchmarkProxy ./internal/app/proxy` measures the same paths without a generator.

Two things get a number like this wrong more often than the proxy does. **What the generator spends, the proxy doesn't get**: wrk costs 6.2 µs of CPU per request and leaves the proxy 13.8 of 24 cores, k6 costs 61.5 µs, leaves 5.6, and reports 244,000 req/s where wrk reports 608,000 — same proxy, same 22.6 µs per request either way. Hence no k6, and a second machine for anything published. **Too few connections measure the connections**: 50 cannot pass ~50,000 req/s against a millisecond forward, whatever the proxy does.
