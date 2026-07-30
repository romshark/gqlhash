# gqlhash-proxy performance

> [!WARNING]
> **`gqlhash-proxy-fhttp` is experimental and is not for production use.**
> Run `gqlhash-proxy` in front of anything that matters.
>
> `gqlhash-proxy-fhttp` exists only as a pure performance experiment and isn't
> recommended to be used for production.

Where the CPU goes, measured on the running proxy rather than in a benchmark. Settings that move it: [TUNING_GQLHASH_PROXY.md](TUNING_GQLHASH_PROXY.md).

## Reproducing

Needs Go and [wrk](https://github.com/wg/wrk).

```sh
go run ./internal/cmd/loadtest -duration 20s -connections 200
go run ./internal/cmd/loadtest -duration 20s -connections 200 -command gqlhash-proxy-fhttp
GOGC=800 go run ./internal/cmd/loadtest -duration 20s
```

Reports per path: wrk's throughput and latency, the cores the proxy held, its RSS peak and mean. Divide cores by req/s for the CPU one request costs, which is the figure that ports across machines.

- `-duration` takes whole seconds, `-connections` at least four. Both are wrk's limits.
- RSS is sampled every 50 ms, so the peak is the highest sample rather than a true high-water mark.
- CPU and RSS come from procfs on Linux and `ps` elsewhere, and are reported unavailable where neither answers.
- Each run is checked against the proxy's decision counters and fails if any request was answered other than the way that path expects.

Handler timing and allocations, without a network:

```sh
go test -run '^$' -bench BenchmarkProxy -benchmem -count 6 ./internal/app/proxy/
```

Write two runs to files and compare with `benchstat old.txt new.txt`. A single pair of numbers says nothing.

CPU profile. The control mux serves no pprof handler by default; add one to `internal/app/proxy/run.go` for the occasion:

```go
controlMux.HandleFunc("/debug/pprof/profile", pprof.Profile) // net/http/pprof
```

Then, during a run, against the control address it printed at startup:

```sh
curl -s "http://CONTROL/debug/pprof/profile?seconds=15" -o cpu.pprof
go tool pprof -top -cum gqlhash-proxy cpu.pprof
```

Binary size, as released:

```sh
go build -trimpath -ldflags "-s -w" -o /tmp/gqlhash-proxy ./cmd/gqlhash-proxy
```

Absolute numbers differ per machine. The ratios between paths and between commands should hold. A generator on the machine it measures understates the proxy under all of these.

## Where the CPU goes

15 seconds of the rejected path under wrk, 200 connections, default `GOGC`, Xeon w5-2455X, 24 cores. Untuned on purpose: what `GOGC` moves is most of what follows. Shares are cumulative and overlap, since allocation happens inside the rows above it.

`gqlhash-proxy`, ~529,000 rejections/s, 14.1 cores held:

| | cumulative |
| --- | --- |
| `net/http.(*conn).serve` | 71.5% |
| — `Syscall6`, reading and writing sockets | 24.8% flat |
| — `(*response).finishRequest` | 26.6% |
| — `(*conn).readRequest` | 20.4% |
| `(*proxy).ServeHTTP` | 10.2% |
| — `check`, reading the body and deciding on it | 5.0% |
| — `decide`, the JSON scan, the parse and the lookup | 2.8% |
| — `reject`, writing the error | 3.3% |
| — `metrics.Observe` | 1.2% |
| `runtime.mallocgc`, none of it the handler's | 16.6% |
| — `gcAssistAlloc`, allocators paying off collector debt | 10.3% |
| `gcBgMarkWorker`, background marking | 5.5% |

net/http and the kernel are ~90% of it. Deciding on the document, which is what the proxy exists to do, is under 3%. Whatever the handler does, nine tenths of the cost is elsewhere.

Collection is ~16%, not the 5.5% the background workers show: most of the marking is done by the goroutines allocating, charged to them as assist. Every one of those allocations belongs to net/http, since `ServeHTTP` allocates nothing on this path.

`gqlhash-proxy-fhttp`, ~604,000 rejections/s, 11.4 cores held:

| | cumulative |
| --- | --- |
| `fasthttp.(*Server).serveConn` | 55.0% |
| — `Syscall6`, reading and writing sockets | 32.4% flat |
| — `(*server).handle` | 8.4% |
| —— `Core.Decide`, the same decision as above | 4.5% |
| —— `Core.Observe` | 1.9% |
| `runtime.schedule` | 44.4% |
| — `findRunnable` | 43.6% |
| —— `runtime.lock2`, waiting on `sched.lock` | 22.7% |
| ——— `procyieldAsm`, spinning for it | 9.1% flat |
| ——— `futex`, sleeping on it | 10.8% flat |
| allocation and collection | 0.0% |

Not one `mallocgc` sample in 170 seconds of CPU: the rejected path on fasthttp allocates nothing, so the collector never runs. The 22% net/http spends allocating and collecting is gone outright, and syscalls rise to a third because what's left is mostly them.

What replaced it is the Go scheduler, at 44%. 200 connections park and wake a worker goroutine each per request, and 38% of `findRunnable` is spent taking one global lock, half of that spinning. This is contention, not work: the proxy holds 11.4 cores where net/http holds 14.1, and answers 14% more requests doing it.

The forwarded path, where the two differ most:

| | `gqlhash-proxy` | `gqlhash-proxy-fhttp` |
| --- | --- | --- |
| serving the client | 49.9% `(*conn).serve` | 81.1% `serveConn` |
| carrying it upstream | 22.3% `ReverseProxy` | 45.7% `(*server).forward` |
| — the client under it | 12.3% `Transport.RoundTrip` | 34.0% `HostClient.Do` |
| `Syscall6` | 23.3% flat | 43.2% flat |
| `runtime.mallocgc` | 16.2% | 0.3% |
| collection, assist and background | 13.4% | 0.03% |

A forward is two round trips of syscalls and nothing else worth naming. fasthttp spends 43% of its CPU there against net/http's 23%, having freed the 21% net/http gives to allocating and collecting the request, the response and the headers of both. That is the whole of the difference.

## Connections

Both commands at `-connections 200`, `1000` and `10000`, default `GOGC`, three runs each. Rejection throughput held within 0.5%; forwarding at 10,000 varied by 10% and is the median.

`gqlhash-proxy`:

| connections | 200 | 1,000 | 10,000 |
| --- | --- | --- | --- |
| rejected req/s | ~515,000 | **~560,000** | ~382,000 |
| rejected, median / p99 | 163 µs / 1.68 ms | 823 µs / 4.91 ms | 13.1 ms / 28.8 ms |
| forwarded req/s | ~163,000 | **~166,000** | ~115,000 |
| forwarded, median / p99 | 1.10 ms / 4.93 ms | 7.45 ms / 34.2 ms | 82.0 ms / 339 ms |
| cores held, rejecting | 13.9 | 14.6 | 11.5 |
| RSS peak | 50 MB | 153 MB | 1,253 MB |

`gqlhash-proxy-fhttp`:

| connections | 200 | 1,000 | 10,000 |
| --- | --- | --- | --- |
| rejected req/s | ~601,000 | **~653,000** | ~431,000 |
| rejected, median / p99 | 170 µs / 0.56 ms | 880 µs / 1.91 ms | 11.8 ms / 16.3 ms |
| forwarded req/s | ~313,000 | **~375,000** | ~186,000 |
| forwarded, median / p99 | 490 µs / 2.34 ms | 1.92 ms / 8.57 ms | 58.8 ms / 506 ms |
| cores held, rejecting | 11.4 | 10.0 | 8.0 |
| RSS peak | 35 MB | 76 MB | 357 MB |

**Both peak near 1,000 and fall off hard by 10,000.** Past the peak the proxy is no longer the limit: it holds *fewer* cores at 10,000 than at 1,000 under both commands, which is a proxy being starved rather than saturated. Four wrk threads driving 10,000 connections on the same machine is most of that, so read the 10,000 column as what the harness does rather than as a ceiling.

**The latency past 200 connections is queueing, not service time.** Concurrency divided by throughput gives the mean wait almost exactly at every point — 1,000 / 560,000 = 1.8 ms, 10,000 / 382,000 = 26 ms — which is what a closed-loop generator measures once the server is the bottleneck. It says how deep the queue is, not how long the proxy took. The CPU per request above is the figure that doesn't move with the count.

**Memory follows connections, not rate.** `gqlhash-proxy` holds ~3.5× the RSS of the fasthttp build at every count, and reaches 1.2 GB at 10,000 where fasthttp holds 357 MB. That is what `GOMEMLIMIT` is sized against, see [TUNING_GQLHASH_PROXY.md](TUNING_GQLHASH_PROXY.md).

**Which command leads depends on the count.** `gqlhash-proxy` rejects faster at 200 connections and only there — at 1,000 and above the fasthttp build leads on both paths and every percentile. [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) has the rest of that comparison.

## Benchmarks read differently

`BenchmarkProxyHandler` runs through an `httptest.ResponseRecorder`: it allocates where a socket does not, and does not syscall where a socket does. It puts `metrics.Observe` at 6% of a rejection where the live profile puts it at 1%, and counts allocations the recorder itself makes. Use it for the direction of a change, not the size: at a 10% share, a 10% handler improvement is under 1% on the wire.

## Remaining

- **Syscalls, 24.8%.** One read and one write per request. `net/http.Server` exposes no buffer sizes.
- **Forwarded path.** 104 of its 106 allocations belong to `httputil.ReverseProxy` and `http.Transport`. `gqlhash-proxy-fhttp` replaces both and forwards ~93% more at the same `GOGC`, ~86% more with each at its best; [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) has what it gives up.
- **Memory, ~22% rejecting and ~21% forwarding**, allocation and collection together. All of it net/http's own. `GOGC` buys most of it back for RSS, and only the fasthttp build removes it.

The other 2 of those 106 are `io.NopCloser(bytes.NewReader(...))`, wrapping the buffered body for the upstream request. The pooled state could carry both, but the transport may still be writing that body after `ServeHTTP` returns, and a pooled reader would be reset under it. Two allocations of 106 are not worth a body belonging to another request.

Dead ends, measured:

- Reading `Content-Type` straight from the header map instead of through `Header.Get`, which canonicalizes a constant key per call. The profile attributes 3.9% to that line; the change measures nothing (p=0.619). The cost is the string comparison beside it.
- `-hash`: `sha2` and `blake3` land within 0.3% of each other on both paths.

`ServeHTTP` allocates nothing of its own on the rejected and malformed paths.
