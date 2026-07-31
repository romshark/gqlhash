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
go run ./internal/cmd/loadtest -duration 20s -connections 1000
go run ./internal/cmd/loadtest -duration 20s -connections 1000 -threads 10 -command gqlhash-proxy-fhttp
GOGC=800 go run ./internal/cmd/loadtest -duration 20s
```

Reports per path: wrk's throughput and latency, the cores the proxy held, its RSS peak and mean, and what the three processes came to against the machine. Divide cores by req/s for the CPU one request costs, which is the figure that ports across machines.

**`-threads` is the flag that decides whether any of this measures the proxy.** It defaults to a third of the machine. Below that the generator is the answer: at four threads on 24 hardware threads the rejected path reports ~562,000/s where it can do ~722,000, and the fasthttp build ~653,000 where it can do ~1,358,000. The faster command is understated more, because a generator too small to saturate the slower one is nowhere near saturating it. Every run ends with the accounting that shows this:

```
proxy: 339.2s of CPU over 20s, 16.9 cores, 154 MB peak, 103 MB mean
generator: 99.0s of CPU over 20s, 4.9 cores
upstream: 0.1s of CPU over 20s, 0.0 cores
balance: 21.9 of 24 cores held, 91% of the machine (proxy 77%, generator 23%, upstream 0%)
```

Read a run as the proxy's number only while the balance leaves the machine something. Past that the three take cores from each other and the proxy is the one starved, since it is the only one the kernel can't leave idle.

- `-duration` takes whole seconds, `-connections` at least `-threads`. Both are wrk's limits.
- RSS is sampled every 50 ms, so the peak is the highest sample rather than a true high-water mark.
- CPU and RSS come from procfs on Linux and `ps` elsewhere, and are reported unavailable where neither answers. wrk's own CPU comes from `wait4`, which is exact.
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
curl -s "http://CONTROL/debug/pprof/profile?seconds=20" -o cpu.pprof
go tool pprof -top -cum cpu.pprof
```

Absolute numbers differ per machine. The ratios between paths and between commands should hold. A generator on the machine it measures understates the proxy under all of these.

## The machine

Xeon w5-2455X: **12 physical cores, 24 hardware threads**, one NUMA node, loopback, 58-byte upstream answer. Cores below are hardware threads, so a proxy holding 16.9 of them is already past every physical core the machine has. The generator and the upstream sit on the same 24.

## Where the CPU goes

20 seconds of the rejected path under wrk, 200 connections, default `GOGC`, each command driven by the thread count that saturates it. Untuned on purpose: what `GOGC` moves is most of what follows. Shares are cumulative and overlap, since allocation happens inside the rows above it.

`gqlhash-proxy`, ~630,000 rejections/s, 15.8 cores held:

| | cumulative |
| --- | --- |
| `net/http.(*conn).serve` | 80.7% |
| — `Syscall6`, reading and writing sockets | 27.7% flat |
| — `(*response).finishRequest` | 30.9% |
| — `(*conn).readRequest` | 22.0% |
| `(*proxy).ServeHTTP` | 12.1% |
| — `check`, reading the body and deciding on it | 5.7% |
| — `decide`, the JSON scan, the parse and the lookup | 3.2% |
| — `reject`, writing the error | 4.1% |
| — `metrics.Observe` | 1.3% |
| `runtime.mallocgc`, none of it the handler's | 16.8% |
| — `gcAssistAlloc`, allocators paying off collector debt | 10.2% |
| `gcBgMarkWorker`, background marking | 4.4% |

net/http and the kernel are ~88% of it. Deciding on the document, which is what the proxy exists to do, is under 4%. Whatever the handler does, nine tenths of the cost is elsewhere.

Collection is ~15%, not the 4.4% the background workers show: most of the marking is done by the goroutines allocating, charged to them as assist. Every one of those allocations belongs to net/http, since `ServeHTTP` allocates nothing on this path.

`gqlhash-proxy-fhttp`, ~1,320,000 rejections/s, 14.4 cores held:

| | cumulative |
| --- | --- |
| `fasthttp.(*Server).serveConn` | 91.7% |
| — `Syscall6`, reading and writing sockets | 52.8% flat |
| — `(*server).handle` | 14.2% |
| —— `Core.Decide`, the same decision as above | 7.4% |
| —— `Core.Observe` | 3.0% |
| `runtime.schedule` | 7.3% |
| — `findRunnable` | 6.4% |
| —— `runtime.lock2`, waiting on `sched.lock` | 1.3% |
| `runtime.netpoll` | 3.4% |
| allocation and collection | 0.0% |

Not one `mallocgc` sample in 288 seconds of CPU: the rejected path on fasthttp allocates nothing, so the collector never runs. The 21% net/http spends allocating and collecting is gone outright, and syscalls rise to over half because what's left is almost entirely them.

**The scheduler is not the story it looked like.** An earlier profile of this path put `runtime.schedule` at 44% and `sched.lock` at 22%, and read it as fasthttp's cost. It was the generator's: four wrk threads could not keep 200 connections busy, so fasthttp's goroutines spent their time looking for work that hadn't arrived. Driven hard enough to saturate, the same path spends 7.3% in the scheduler and 1.3% on that lock, and answers more than twice as many requests. Spinning in `findRunnable` is what an idle Go server looks like, not a contended one.

The forwarded path, where the two differ most:

| | `gqlhash-proxy` | `gqlhash-proxy-fhttp` |
| --- | --- | --- |
| serving the client | 51.1% `(*conn).serve` | 81.9% `serveConn` |
| carrying it upstream | 22.2% `ReverseProxy` | 44.9% `(*server).forward` |
| — the client under it | 11.6% `Transport.RoundTrip` | 33.2% `HostClient.Do` |
| `Syscall6` | 23.4% flat | 42.3% flat |
| `runtime.mallocgc` | 16.4% | 0.0% |
| collection, assist and background | 13.3% | 0.0% |

A forward is two round trips of syscalls and nothing else worth naming. fasthttp spends 42% of its CPU there against net/http's 23%, having freed the 21% net/http gives to allocating and collecting the request, the response and the headers of both. That is the whole of the difference.

## Connections

Both commands at `-connections 200`, `1,000` and `10,000`, default `GOGC`, three runs each, `-threads 8` for `gqlhash-proxy` and `-threads 10` for `gqlhash-proxy-fhttp`. Rejection throughput held within 0.5%; forwarding at 10,000 varied by 10% under `gqlhash-proxy` and by 50% under the fasthttp build, and both are the median.

`gqlhash-proxy`:

| connections | 200 | 1,000 | 10,000 |
| --- | --- | --- | --- |
| rejected req/s | ~630,000 | **~718,000** | ~622,000 |
| rejected, median / p99 | 170 µs / 2.43 ms | 823 µs / 6.43 ms | 8.61 ms / 28.0 ms |
| forwarded req/s | ~160,000 | **~167,000** | ~123,000 |
| forwarded, median / p99 | 1.14 ms / 4.95 ms | 7.34 ms / 33.4 ms | 81.6 ms / 336 ms |
| cores held, rejecting | 15.8 | 16.8 | 15.9 |
| RSS peak | 52 MB | 153 MB | 1,250 MB |

`gqlhash-proxy-fhttp`:

| connections | 200 | 1,000 | 10,000 |
| --- | --- | --- | --- |
| rejected req/s | ~1,320,000 | **~1,357,000** | ~891,000 |
| rejected, median / p99 | 110 µs / 3.04 ms | 530 µs / 3.56 ms | 5.50 ms / 11.6 ms |
| forwarded req/s | ~309,000 | **~372,000** | ~238,000 |
| forwarded, median / p99 | 550 µs / 2.30 ms | 2.30 ms / 8.93 ms | 54.9 ms / 262 ms |
| cores held, rejecting | 14.4 | 14.5 | 12.5 |
| RSS peak | 49 MB | 122 MB | 406 MB |

**Both peak near 1,000 and fall off hard by 10,000.** Past the peak the proxy is no longer the limit: it holds *fewer* cores at 10,000 than at 1,000 under both commands, which is a proxy being starved rather than saturated. Eight to ten wrk threads driving 10,000 connections on the same machine is most of that, so read the 10,000 column as what the harness does rather than as a ceiling.

**The latency past 200 connections is queueing, not service time.** Concurrency divided by throughput gives the mean wait almost exactly at every point — 1,000 / 718,000 = 1.4 ms, 10,000 / 622,000 = 16 ms — which is what a closed-loop generator measures once the server is the bottleneck. It says how deep the queue is, not how long the proxy took. The CPU per request above is the figure that doesn't move with the count.

**Memory follows connections, not rate.** `gqlhash-proxy` holds ~1.3× the RSS of the fasthttp build at 1,000 connections and ~3× at 10,000, where it reaches 1.2 GB against 406 MB. That is what `GOMEMLIMIT` is sized against, see [TUNING_GQLHASH_PROXY.md](TUNING_GQLHASH_PROXY.md).

**`gqlhash-proxy-fhttp` leads on both paths at every count.** It rejects 2.1× as many requests at 200 connections and 1.9× at 1,000, and forwards 1.9× to 2.2× as many, on a third to a fifth of the memory. The one thing it does not win is the rejected tail at 200 connections — 3.04 ms against 2.43 ms — while serving 2.1× the rate. [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) has the rest of that comparison and what it gives up to get there.

## Benchmarks read differently

`BenchmarkProxyHandler` runs through an `httptest.ResponseRecorder`: it allocates where a socket does not, and does not syscall where a socket does. It puts `metrics.Observe` at 6% of a rejection where the live profile puts it at 1.3%, and counts allocations the recorder itself makes. Use it for the direction of a change, not the size: at a 12% share, a 10% handler improvement is around 1% on the wire.

## Remaining

- **Syscalls, 27.7%.** One read and one write per request. `net/http.Server` exposes no buffer sizes.
- **Forwarded path.** 104 of its 106 allocations belong to `httputil.ReverseProxy` and `http.Transport`. `gqlhash-proxy-fhttp` replaces both and forwards ~92% more at the same `GOGC`, ~85% more with each at its best; [GQLHASH_PROXY_FHTTP.md](GQLHASH_PROXY_FHTTP.md) has what it gives up.
- **Memory, ~21% rejecting and ~21% forwarding**, allocation and collection together. All of it net/http's own. `GOGC` buys most of it back — the rejected path rises 47% from default to `GOGC=800` — and only the fasthttp build removes it.

The other 2 of those 106 are `io.NopCloser(bytes.NewReader(...))`, wrapping the buffered body for the upstream request. The pooled state could carry both, but the transport may still be writing that body after `ServeHTTP` returns, and a pooled reader would be reset under it. Two allocations of 106 are not worth a body belonging to another request.

Dead ends, measured:

- Reading `Content-Type` straight from the header map instead of through `Header.Get`, which canonicalizes a constant key per call. The profile attributes 3.9% to that line; the change measures nothing (p=0.619). The cost is the string comparison beside it.
- `-hash`: `sha2` and `blake3` land within 0.3% of each other on both paths.

`ServeHTTP` allocates nothing of its own on the rejected and malformed paths.
