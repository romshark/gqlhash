# gqlhash-proxy performance

Where the time goes, measured on the running proxy rather than in a benchmark. For the settings that move it, see [GQLHASH_PROXY_TUNING.md](GQLHASH_PROXY_TUNING.md).

## Where the CPU goes

A CPU profile of the proxy under wrk, 200 connections, 504,000 rejections a second, on a Xeon w5-2455X of 24 cores:

| | cumulative |
| --- | --- |
| `net/http.(*conn).serve` | 74.2% |
| — `Syscall6`, reading and writing the sockets | 25.5% flat |
| — `(*response).finishRequest` | 27.9% |
| — `(*conn).readRequest` | 21.1% |
| `(*proxy).ServeHTTP`, all of it | 10.3% |
| — `check`, the JSON scan, the parse, the hash and the lookup | 5.0% |
| — `reject`, writing the error | 3.5% |
| — `metrics.Observe` | 1.0% |
| garbage collection | 4.4% |

The proxy spends about a tenth of its CPU on its own work and the rest in net/http and the kernel. Hashing the document, which is the thing it exists to do, is 5%.

That bounds the handler: however good it gets, nine tenths of the cost is somewhere else. What's cheap to take there has been taken.

## What a benchmark says instead

`BenchmarkProxyHandler` runs the handler through an `httptest.ResponseRecorder`, which allocates where a socket doesn't and doesn't syscall where a socket does. It puts `metrics.Observe` at 6% of a rejection where the running proxy puts it at 1%, and it counts allocations the recorder itself makes. Read it for the direction of a change and not for its size: 10% off the handler was 0.8% on the wire.

Capturing a profile of the running proxy means adding `net/http/pprof` to the control mux for the occasion. It isn't served by default: the control port carries `/metrics`, `/status` and `/reload`, and a profile handler is not something to leave listening.

## What was taken

Two changes on the rejected path, together −10.7% of the handler (p=0.004) and one allocation out of ten:

- The duration histogram of each decision is resolved once at startup instead of looked up by its label per request, which hashed the label under a lock. −5.4% (p=0.008).
- The `Content-Type` of an error answer is a shared slice rather than the one `Header.Set` builds per call. One allocation, on the path a flood takes.

Neither is visible end to end, for the reason above.

One that wasn't taken: reading `Content-Type` out of the header map directly, skipping the canonicalization `Header.Get` does on a constant key per call. The profile blamed that line for 3.9% and the measurement found nothing (p=0.619) — the cost was the string comparison beside it, not the lookup.

## What's left

- **Syscalls, 25.5%.** One read and one write per request, and `net/http.Server` exposes no buffer sizes. Only a different server moves this.
- **The forwarded path.** 104 of its 106 allocations belong to `httputil.ReverseProxy` and `http.Transport`. Replacing them with a hand-rolled upstream client is the one real lever, worth perhaps 1.5–2x, against owning chunked encoding, trailers and 100-continue.
- **Garbage collection, 4.4%**, which `GOGC` already answers.

The other two of those 106 are the proxy's own: `io.NopCloser(bytes.NewReader(...))` wraps the buffered body for the upstream request. The pooled state could carry both and hand out a pointer, but the transport may still be writing that body after `ServeHTTP` has returned, and a reader handed back to the pool would then be reset under it. Two allocations of a hundred and six aren't worth a body that belongs to another request.

Not worth looking at: `ServeHTTP` allocates nothing of its own on the rejected and malformed paths, and the hash function doesn't show, `sha2` and `blake3` landing within 0.3% of each other on both paths.
