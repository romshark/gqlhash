# Pre-v2 review

Review of the v2 surface before release: package API, both CLIs, config, proxy core,
both server implementations, allowlist, docs. Everything below was measured against
the built binaries and a real upstream, not inferred from reading.

`go test ./...` is green, acceptance suite included.

Status: **finding 1 is fixed** (see the commit that carries this file's review).
Everything else is open.

## Real bugs

### ✅ ~~1. `-upstream.timeout=0` silently broke every forward — FIXED~~

`flags.go` derived `write-timeout = upstream.timeout + 10s`, so an upstream timeout
of "off" produced a 10s write deadline. Measured against an upstream answering at 14s:

| config | result |
| --- | --- |
| `-upstream.timeout 0` | empty reply (status 000) after 14.00s |
| `-upstream.timeout 0 -server.write-timeout 0` | 200 after 14.00s |
| `-upstream.timeout 30s` | 200 after 14.00s |

The client waited the full upstream latency and then got a dropped connection — not
even a 504, because the write deadline had already passed by the time the handler had
something to write. This is precisely what the "write timeout must exceed
`-upstream.timeout`" rule exists to refuse; `0` walked around it, since `x <= 0` is
false for every positive `x`. The proxy README's "`0` leaves any of the timeouts off"
was false for this one flag.

Fixed: an unset write timeout is now left off where `-upstream.timeout` is off, rather
than derived. Verified end to end — the same config now relays the 14s answer as a 200.
`TestParseProxy` pins it.

**One case remains open by design.** An *explicitly* set finite write timeout with an
unbounded upstream still cuts the answer, and the guard doesn't catch it:

```
-upstream.timeout 0 -server.write-timeout 5s   → empty reply after 14.00s
```

By the rule's own logic any finite write timeout is below an unbounded upstream, so this
could be refused at startup — at the cost of making "no upstream bound, but a hard cap
on total answer time" inexpressible. That trade is a call worth making deliberately
rather than as part of this fix.

### ✅ ~~2. `-upstream.timeout` and `-server.max-body` accept negatives and zero~~ — FIXED

Every neighbouring flag validated: the four server timeouts were checked for negatives,
and so was `-upstream.max-conn-lifetime`. These two were not. Measured:

- `-upstream.timeout -5s` — accepted, started, and produced the same silent breakage as
  finding 1 (that fix covered the symptom, not the validation gap).
- `-server.max-body -1` — accepted; every POST answered `413 REQUEST_TOO_LARGE`,
  including one with an empty body, while GETs kept working.
- `-server.max-body 0` — accepted; no POST could ever carry a document.

Fixed: `-upstream.timeout` joined the loop that refuses a negative duration
(`must be 0 or more`, 0 still being off), and `-server.max-body` is now refused below 1
(`must be 1 or more`). Both verified on both binaries, exit 2 with the reason named.

`TestMaxBodyZeroRefusesEveryBody` had pinned the old `-server.max-body 0` behaviour as
"an unguarded comparison rather than a design, pinned so changing it is a decision
somebody took" — that decision is now taken, and the test pins the startup refusal
instead as `TestMaxBodyBelowOneIsRefused`.

Still unvalidated, not covered here: `-server.shutdown-timeout` takes negatives, and
both `0` and a negative mean "wait for nothing", so an in-flight request is abandoned
and the process exits 1. The README's "`0` leaves any of the timeouts off" reads as
"wait forever" for this one.

### ✅ ~~3. The startup log misreports the effective depth limit~~ — FIXED

`-depth-limit -5` logged `depth_limit=-5`, but `parser.Parse` normalizes anything below 1
to `DefaultDepthLimit` (128). Same for `0`. The log claimed a limit that wasn't in force.

Fixed: config applies the rule itself, so it carries the limit that's in force rather
than the number that was typed. `-depth-limit -5` and `-depth-limit 0` now both log
`depth_limit=128`, verified on the binary; `4` and `128` log themselves. No decision
changes — the parser applied the same rule already, and `TestDepthLimitFlag` pins that
`0` is the default rather than no limit at all. Both parsers normalize, so the hashing
command reports the same way.

### ✅ ~~4. fhttp inverts finding 1 rather than sharing it~~ — FIXED

`gqlhash-proxy-fhttp -upstream.timeout 0` issued `DoTimeout(…, 0)` and answered
**504 immediately, on every request**. Same config file, opposite failure from the
primary binary.

Fixed in `proxyfast` rather than by refusing `0` in config, which is what the report
first suggested: finding 1 makes `0` a working way to run without an upstream bound, so
the two commands should agree on it instead of neither having it. fasthttp has no
duration meaning "no limit" — `DoTimeout(…, 0)` is a deadline already past, and
`MaxConnWaitTimeout` at `0` waits for no connection at all — so both now take a bound no
process outlives, the idiom the file already used for `-server.write-timeout` on a stream.

`TestUpstreamTimeoutOff` pins it across both commands, and it has teeth: reverted, it
fails on the fhttp target with the 504 and passes on net/http.

### ✅ ~~5. `-upstream.url` credentials are logged, then silently not used~~ — FIXED

```
-upstream.url 'http://user:pass@host/graphql?apikey=secret#frag'
```

started fine and logged, at info level:

```
upstream=http://user:pass@127.0.0.1:1/graphql?apikey=secret#frag
```

The password landed in the log. Meanwhile the `Rewrite` hook set only scheme, host and
path and replaced `RawQuery` with the client's, so the userinfo, the query and the
fragment reached the API in no form at all.

Fixed as three separate cases, since they aren't the same problem:

- **The query is now kept and merged**, not dropped: the endpoint's parameters lead and
  the client's follow, so an API reached as `…/graphql?env=staging` gets `env=staging` on
  every forwarded request. A hosted endpoint that takes a key or an environment is
  serveable now, where before half its URL was silently discarded.
- **An endpoint naming `query` is refused at startup**, through the same `hasQueryParam`
  a request goes through, so `;`-separated and `quer%79` spellings are caught too. It
  would otherwise arrive beside the client's document and the API would choose between
  two the allowlist saw one of.
- **Credentials are refused**, not implemented: nothing forwards them (the userinfo→
  `Authorization` conversion lives in `http.Client`, which a reverse proxy never goes
  through), and taking them as a flag would contradict what keeps
  `GQLHASH_PROXY_CONTROL_TOKEN` off the command line — a process argument is readable by
  anyone on the host. The refusal prints `URL.Redacted()`, so it doesn't echo the
  password back. If upstream auth is ever wanted, it belongs in the environment.
- **A fragment is left alone**: no HTTP client sends one, so there's nothing to refuse.

Cost: nothing measurable. The merge returns the client's string untouched where the
endpoint has no query, which is every deployment naming a plain URL —
`BenchmarkProxyHandler/allowed` is 106 allocs/op before and after, timings overlapping,
and `TestMergeQuery` pins zero allocations on that path and at most one where an endpoint
query is configured. `TestUpstreamEndpointQuery` covers the merge and all three refusal
shapes on both binaries.

## Flags that don't play well together

### ✅ ~~6. `-allow-batch` has no document cap, and the metics hide it~~ — FIXED

Measured: one request at the default 1 MiB `-server.max-body` carried **47,662** allowed
operations, was forwarded to the API as a single request, and appears in `/status` as
`"allowed":1`.

The flag quietly converted "one request = one operation" into "one request = ~47k
operations", and a dashboard watching request counters saw nothing unusual. Every document
was on the allowlist — that part was never bypassed; what had no bound was how many of
them ran per request, and the only lever was `-server.max-body` in bytes.

Fixed by replacing the flag: `-allow-batch` is gone and `-server.max-batch <n>` takes its place (see finding 29 for the name).

- `0` is the default and refuses a batch outright, so the default behaviour is unchanged.
- `n ≥ 1` accepts an array of up to `n` documents, every one of which must be allowed.
- Past the cap: `413` with `BATCH_TOO_LARGE`, counted as its own decision — `batch_too_large`
  in `/status` and in `gqlhash_proxy_requests_total`, so a client batching more than you
  allow is visible rather than filed under malformed.
- Negative: refused at startup, `-max-batch must be 0 or more`.
- A lone request object is one document whatever the cap says, so the flag never stands
  between a client and an ordinary request.

The cap also bounds the work of checking it: `extractJSON` stops at the document that
breaks it, so the 47,662-document body costs the cap plus one rather than the megabyte.
Verified on the binary — a batch of 10 under `-max-batch 10` is 200, 11 is 413, and the
47,662 one is 413 with `"batch_too_large":2` in `/status`.

`TestMaxBatch` covers it on both commands, `TestExtractJSONBatchCap` covers the cap and
the early stop, and `TestDecisionsAreCounted` now asserts the new series alongside the
other five.

### ✅ ~~7. The allowlist doesn't constrain the HTTP method~~ — FIXED

Measured: `PUT`, `DELETE` and `PATCH` carrying `{"query":"<allowed>"}` were all forwarded
with the method preserved. GraphQL over HTTP defines GET and POST only, so anything else
reached the API as a shape nobody registered.

Fixed: the method is now part of the shared decision. `Request` carries a `Method` — GET,
POST, or `MethodOther`, which is the zero value so a request built without one is refused
rather than served — and anything that isn't GET or POST is answered `405` with
`Allow: GET, POST` before the document is read. Under `-opaque-errors` it's a `403` with
no `Allow`, since the header follows the status as RFC 9110 has it.

Counted apart as `method_not_allowed`, in `/status` and in
`gqlhash_proxy_requests_total`. On the net/http side the body of a refused method is
never read at all.

Verified on both binaries: POST and GET 200; PUT, DELETE, PATCH, OPTIONS 405 with the
`Allow` header; `"method_not_allowed":4`. Two tests that had pinned the old behaviour are
rewritten — `TestMethods` (whose comment said a server that started refusing methods
"would be a change made on purpose", which this is) and `TestHEADWithQueryString`, where a
health checker's HEAD now lands in its own series instead of tripping the `ambiguous`
alert meant for somebody probing.

The proxy README's claim that GraphQL-over-SSE single-connection mode's `PUT`/`DELETE`
are answered 400 held only because those requests carry no document; both are now 405 and
the docs say so.

### 8. `-ignore=inputs|variables` weakens the allowlist, and the proxy docs don't say so

Under `-ignore=inputs`, an allowlisted `products(first: 10)` also admits
`products(first: 100000)`: inline literal values stop being part of a document's
identity. The main README makes exactly this point for the cache-key use case ("Keep the
default `-ignore=nothing`"), but the proxy README's `-ignore` row only links to the
hashing section, where the framing is about which documents hash alike rather than about
what an allowlist then stops guaranteeing.

Worth a warning in the proxy README, and a `warn`-level startup log when a mode other
than `nothing` runs in front of an allowlist.

### 9. Nothing warns when the control server is exposed without a token

`-control.listen 0.0.0.0:9090` with no `GQLHASH_PROXY_CONTROL_TOKEN` starts silently.
Scraping metrics from another host is the usual reason to move off loopback, and doing
so also hands `POST /reload` — which rereads and reparses every document — to anything
that can reach the port. A `warn` at startup when the address isn't loopback and no
token is set costs nothing.

## Polish

### ✅ ~~10. The two binaries answer `-version` in different shapes~~ — FIXED

`gqlhash -version` printed 29 lines: the name, the licence, and the full
`debug.BuildInfo`. `gqlhash-proxy -version` printed `gqlhash-proxy vdev`. v1 was the
verbose one, so aligning them was a v2-only opportunity.

Fixed: all three commands now answer with the same three lines — the name and the
version, then the copyright and the licence — from one place,
`internal/app/versioninfo`, so the notice is written once and the commands can't drift:

```
gqlhash v2.0.0
Copyright (c) 2026 Roman Scharkov (github.com/romshark/gqlhash)
MIT License
```

The version stays alone on the first line, which is what a script reading
`-version | head -1` takes.

Surveyed twelve installed CLIs before picking: 9 of 12 print one line by default
(`git version …`, `go version …`, `Docker version …`, `curl 8.7.1 …`), and every long
form sits behind something extra — `openssl version -a`, `docker version` against
`docker --version`, `kubectl version -o json`. The GNU Coding Standards' five-line form
is where "version output carries the licence" comes from; it asks for one line per item
and says nothing about dependency dumps.

The dropped `debug.BuildInfo` is no loss: `go version -m $(command -v gqlhash)` prints
23 lines for this binary — the Go version, the module, every dependency and its hash —
for any Go binary, with no code in the program. The README now points at it, and `LICENSE`
still carries the terms in full; the notice names them.

`TestRunVersion` pins the shape and that the version is alone on line 1;
`TestCLIVersionAndHelp` pins the same for both proxy binaries.

### ✅ ~~11. `gqlhash` writes the hash with no trailing newline~~ — FIXED

As v1 did, so this was inherited rather than new. Measured on the old binary, writing
`gqlhash > h.txt`:

| | before |
| --- | --- |
| `wc -l < h.txt` | **0** lines, though the hash is in there |
| `read -r h < h.txt` | **exit 1** — reads the hash *and* reports failure |
| `while read -r l; …; done < h.txt` | **0** iterations, the unterminated line skipped |
| two hashes appended to one file | run together: `102fe…2f1f329c9d…6e01` |
| `v=$(gqlhash …)` | fine — command substitution strips it either way |

The first three are the reason to act: they look like "no data" to a script while the
hash is sitting right there, with no error to notice. Every tool of this kind writes the
newline — `shasum`, `md5`, `git hash-object`, `openssl dgst` all end in `0a`.

Fixed: one write of the hash and the newline. All five rows above now behave, verified on
the binary, and `$(gqlhash …)` is unchanged. `TestRunEndsTheHashWithANewline` pins it
across all four formats, including that it's one write and one newline.

The cost is a migration note, which MIGRATION.md now carries: a comparison of the bytes —
a golden file, or `gqlhash | cmp - expected` — needs the newline added. Anything reading
the output as a string is unaffected.

---

## Found since this report

The findings above were the first pass over config and the proxy. These came out of
acting on it and reviewing the rest of the v2 surface — parser, docs, release config
and the test suite — measured the same way.

### ✅ ~~12. Two different string values hashed identically — FIXED~~

The escape a string value is re-encoded with wasn't injective. A control byte took
`\` + b+0x40, spanning 0x40–0x5F, and the backslash took `\` + `\` — but 0x5C is both
a backslash and 0x1C+0x40, so the two shared a sequence:

```
{ f(path: "\u001C") }  value 1c  →  307850deb900e37cf602931b8f2552be97aa3968
{ f(path: "\\") }      value 5c  →  307850deb900e37cf602931b8f2552be97aa3968
```

`vektah/gqlparser` reports the arguments as different; gqlhash reported the same hash.
For an allowlist that's a bypass: an entry holding a backslash in a string value is
matched by a document carrying U+001C in its place.

Fixed in `9aa2e77` — the backslash now takes `|`, outside the range the control bytes
occupy. `TestParseStringEscapesAreDistinct` sweeps every escapable byte through both
string forms; reverting the fix fails all three of its subtests. **This changes hashes**
for any document with a backslash in a string value, which is what MIGRATION.md leads on.

### ✅ ~~13. Six parser benchmarks measured nothing — FIXED~~

`BenchmarkParse`, `BenchmarkParseBytes` and `BenchmarkParsePooled` passed `Options{}`
for every document, but `testdata/nesting-attack.graphql` nests to **129** — one past
`DefaultDepthLimit`. All six nesting sub-benchmarks were calling `b.Fatal("too deep")`
instead of measuring. The root package gets this right (`DepthLimit: 10_000`); the
parser package never did. Invisible because neither `make` nor CI runs benchmarks.

### ✅ ~~14. Four exported `Core` methods were dead — FIXED~~

`Core.MaxBody`, `Core.Log`, `Core.Write` and `Core.Draining` are exported so an
implementation in another package can reach them. `proxyfast` calls none of them — it
receives `cfg` and `log` as parameters to `New`. Found by measuring what the running
servers actually reach (`make cover-servers`), where all four sat at 0%. Deleted;
everything still builds and passes, which is the proof.

## Docs that had stopped being true

### ✅ ~~15. Comments carrying numbers that no longer reproduced — FIXED~~

Two, both re-measured rather than re-read:

- `parser/write.go` claimed token-by-token writing "measures 38% slower … and no
  different with `io.Discard`, so the Write calls themselves are not the cost." Built
  both variants: SHA-1 **2613 vs 5720 ns** (2.2×, not 38%) and `io.Discard` **2033 vs
  2551 ns** (25% apart, not "no different"). The second claim was the dangerous one — it
  would have talked a contributor out of the buffering.
- `internal/app/proxy/proxy.go` claimed padded vs packed counters measure "~10.7ns
  against ~8.6ns per raise". Measured ~0.93 vs ~3.6. Nothing in the repo reproduces
  either figure.

Both sets of figures dropped, mechanisms kept.

### ✅ ~~16. The README headline quoted superseded numbers — FIXED~~

`dbf723c` updated the benchmark table but not the prose above it, which still read
"~631,000 … 155 µs … ~213,000" against the table's 928,000 / 140 µs / 211,000, and
"three-fold" where the ratio is 4.4×. Worse, `~631,000` had since become TUNING's
`GOGC=100` figure, so the two documents read as disagreeing about the tuned case.

### ✅ ~~17. `parser.Parse`'s doc duplicated the package doc, and had drifted — FIXED~~

Both listed what the canonical form leaves out. The package doc named **commas**;
`Parse`'s copy didn't, though commas are ignored. The duplicate wasn't just redundant,
it was wrong. Deleted; the package doc owns that description.

### ✅ ~~18. `-depth-limit` was undocumented — FIXED~~

Defined on both commands, present in neither README — under a proxy table introduced
with "Every flag of the proxy, with its default". The same README documented the
`too_deep` counter without ever naming the flag that sets it.

## Release surface

### ✅ ~~19. Homebrew had never shipped, and the binaries are unsigned — FIXED~~

`homebrew_casks` was added on this branch and sits on no tag; v1.2.5 had no brew section
at all. So the README's `brew install gqlhash` documented an install that had never run,
and v2 would be its first outing — with unsigned binaries, which a *cask* install leaves
Gatekeeper-quarantined. Added goreleaser's `postflight` hook, plus caveats carrying the
migration warning, since `brew upgrade` is how a v1 user would meet v2.

### ✅ ~~20. `go.mod` required Go 1.26.5 — FIXED~~

Nothing in the code needs 1.26. Built with the directive lowered: **Go 1.25.0 builds,
vets and passes the parser suite**. The real floor is `golang.org/x/crypto v0.54.0`.
A patch-level directive also means consumers need exactly ≥1.26.5.

### ✅ ~~21. No v1→v2 migration notes — FIXED~~

13 breaking changes since v1.2.5, documented only in commit messages. Verified against a
v1.2.5 build from git history: documents hash identically **unless a string value holds
an escape sequence**, and three classes v1 accepted are now refused (a lone surrogate,
a variable in a const position, nesting past 128). In the proxy the latter surface as
*skipped allowlist entries* — logged, not fatal. MIGRATION.md now carries this.

## Test and contract gaps

### ✅ ~~22. The acceptance contract understated itself — FIXED~~

`doc.go` said four flags make an implementation testable. The suite passes ~26, and an
unknown flag makes a Go `FlagSet` exit 2 — so a third-party `-proxy.bin` would have died
at startup and reported that instead of the rule under test.

### ✅ ~~23. The TLS validations weren't in the acceptance contract — FIXED~~

Six rejections (`-server.tls.*` pairing, an absent cert, a mismatched pair,
`-upstream.tls.ca` over http, a file with no PEM) were pinned in the config unit tests
alone. Every other flag validation is held at the acceptance layer, so a third-party
implementation could accept `-server.tls.cert` alone and bind a plaintext port while
claiming conformance.

## Still open at the time of writing

### ✅ ~~24. CI doesn't enforce what it could~~ — FIXED

- `go vet` no longer runs with `continue-on-error: true`. It was clean, so blocking
  cost nothing.
- `gqlhash-proxy-fhttp` is built in CI, so a break in it is a build failure rather
  than a confusing acceptance failure.
- `-race` runs in CI and as `make race`, in `all`. Clean on every package it covers.

The race bullet is closed only for the code the tests reach **in process**. `-race`
instruments the test binary, and `resolve()` builds the acceptance suite's servers as
separate processes without it — so the suite is excluded, and the concurrent code in a
*running* proxy is still unchecked. My earlier "I ran it across every package including
the acceptance suite: clean" was a weaker result than it sounded: it exercised the
harness, which locks its own state already, not the servers.

Covering the servers takes three things, not a flag:

- `-race` on the builds `resolve()` does, via a `//go:build race` const so it follows
  the test binary.
- `GORACE=halt_on_error=1` in the servers' environment.
- Something that fails on exit code 66. Tests calling `wait`/`stop` and asserting 0
  would catch it; a test that ignores the code passes green with the report sitting
  unread in captured stderr.

Without the third, the suite finds races and doesn't say so. Worth its own item if
it's wanted — the cost is the full suite's runtime, instrumented.

### ✅ ~~25. `-upstream.http2` is the one flag never handed to a running server~~ — FIXED

`TestFlagsThatAreOnlyEverParsed` exists for exactly this class of hole and excluded it
on purpose: one target refuses the flag and the other takes it, and `each()` has no
per-target predicate. Its *refusal* by the fasthttp build was pinned only in the config
unit tests.

Fixed with `TestUpstreamHTTP2`, the one test that asks which command it runs — because
this is the one flag whose meaning depends on that, so a single shared assertion can only
be weaker than the truth:

- **gqlhash-proxy**: an upstream offering h2 over ALPN is reached over `HTTP/2.0` with the
  flag at its default, and the same upstream over `HTTP/1.1` with `-upstream.http2=false`.
  The upstream is `httptest.NewUnstartedServer` with `EnableHTTP2`, so a proxy that never
  tried can't pass; `-upstream.tls.ca` is set too, which is where net/http would otherwise
  leave h2 off — a custom `TLSClientConfig` disables it unless the transport forces it.
- **gqlhash-proxy-fhttp**: `-upstream.http2` is refused at startup with the reason named,
  and `-upstream.http2=false` serves the same upstream over `HTTP/1.1` — the refusal now
  pinned against the running command rather than only in the parsing.

It has teeth: with `ForceAttemptHTTP2` hard-wired to false — the flag accepted and dropped
on the floor, which is what this class of hole is — the net/http subtest fails with
`expected the forward over HTTP/2; received HTTP/1.1`.

`TestFlagsThatAreOnlyEverParsed` now points at it instead of explaining why it can't.

### ✅ ~~26. `govulncheck` is asserted but unverified~~

GQLHASH_PROXY_FHTTP.md states "`govulncheck` reports nothing against it" for fasthttp
v1.73.0. It isn't installed here, so that's the one release claim in the docs I couldn't
check. Worth a run before tagging.

----------------

## NEW
A review of the contracts v2 freezes, rather than of behaviour that can be fixed in a
patch release: the Go API, both CLIs, the hash itself, the proxy's flag names, its wire
answers, the control endpoints and the metrics. Each finding says what makes it hard to
change after tagging, since that's the only thing that makes it urgent.

### ✅ ~~27. `gqlhash -hash` defaults to sha1 — the function the proxy refuses~~ — FIXED

The proxy takes only collision-resistant functions and rejects sha1 as broken, while the
CLI whose flagship use case is trusted documents produced sha1 by default. A CLI's default
output can't change in a patch release — every unflagged invocation changes — so v2 was
the only chance.

Fixed: the default is `sha2`, the proxy's default too, so a hash built with either command
matches the other. `sha1` and `md5` stay on offer for a cache key or a bucket, where a
collision costs a miss rather than an execution.

The cost is a migration note, since this changes output for every document rather than
only for those carrying escapes: MIGRATION.md now leads with it and gives `-hash sha1` as
the way to keep what a v1 pipeline printed, and the Homebrew caveat says the same. Every
hash the README quotes was recomputed from the binary and checked back against it. The
`hashFunctions` table is reordered so the help reads worst-last — the default first, then
the rest of the collision-resistant ones, which makes the proxy's set the front of the
list, then the broken ones, then the collidable ones.

### 28. `-max-batch` counts documents, not elements, so the cap is bypassable

Measured with `-max-batch 1`:

```
[{"query":"<allowed>"}, 7, 7, … ×20000]          → 200, forwarded (39 KiB, 20,001 elements)
[{"query":"<allowed>"}, {"query":"<allowed>"}]   → 413 BATCH_TOO_LARGE
```

An element carrying no `query` member is neither counted nor refused: `[allowed, 7]`,
`[allowed, {}]` and `[allowed, null]` are all 200 and forwarded whole. So finding 6 is
half closed — operations are bounded, elements are not, and the API still parses and
answers 20,001 of them.

Turning that 200 into a 400 after release changes an answer a client may rely on, so it
belongs in v2. Two ways: refuse a batch element that carries no document, or count
elements rather than documents against the cap. I'd refuse the element — "every document
must be allowed" reads as "every element carries one", and it's the fail-closed half.

### ✅ ~~29. `-max-batch` is the only request-shape limit outside `-server.`~~ — FIXED

`-server.max-body` bounds what a request may carry in bytes and `-max-batch` bounded it in
documents, one namespaced and one not. `-depth-limit`, `-ignore` and `-hash` are bare
because the hashing command shares them; the batch cap has no counterpart there. Flags
can't be renamed after v2, and this one was new in this branch.

Fixed: it's `-server.max-batch`, and `Proxy.MaxBatch` moved into `ProxyServer` beside
`MaxBody`, so the config mirrors the flag namespace rather than crossing it. The
environment form follows for free — verified: `GQLHASH_PROXY_SERVER_MAX_BATCH=3` refuses a
batch of four with 413, `-server.max-batch -1` is refused at startup, and `-max-batch` is
no longer a flag at all.

### 30. The traffic port serves every path, forever

`GET /` and `GET /healthz` are 400, `HEAD /` is 405 — correct today, and it means no path
can ever be carved out for the proxy itself. Adding `/healthz` on the data plane in v2.1
would steal a path a client may be posting GraphQL to. Either reserve one now, or state in
the docs that the traffic port belongs entirely to the API and health checks go to the
control port. Only the first is impossible later.

### 31. The canonical form's choices are permanent — worth confirming, not just documenting

Order significance (fields, arguments, operations, input-object fields), fragments not
inlined, **numbers hashed as written** (`1.0` ≠ `1` ≠ `1e0`), descriptions left out. Each
is now unchangeable: any of them would invalidate every stored hash and every allowlist.
The README lists them under Known Limitations, which is the right place; what's worth
saying out loud before tagging is that they're wanted. My read is that they are —
normalizing numbers would cost a parse of every literal on the hashing path, and
`-ignore=inputs` already covers the case where values shouldn't matter.

### WONTFIX ~~32. `parser.HPref*` are public and are the hash~~

Their values can never change. The good news is the encoding leaves room: `0x0`, `0x10`
and everything above `0x1f` are free, so a construct a future GraphQL spec adds can take a
prefix without touching the hash of any document that doesn't use it. That property is
worth documenting beside the "no 0x9, 0xA or 0xD" rule, so a later edit doesn't renumber
the table for tidiness and silently rehash the world.

### WONTFIX ~~33. `Ignore` is an ordered ladder, so a future axis can't join it~~

`IgnoreNothing < IgnoreInputs < IgnoreVariables`, documented as each leaving out what the
one before it leaves out "and more". A mode that isn't a superset — ignore aliases, ignore
operation names, ignore variable definitions but keep literal values — can never be an
`Ignore` value; it has to arrive as another `Options` field, leaving two knobs for one
question forever. The alternative is a bitmask now (`IgnoreInputs|IgnoreVariables`), which
is strictly more expressive at the cost of a wider test matrix and a comma-list `-ignore`.
I'd keep the ladder, since it matches the CLI and the cases people ask for — but as a
decision, with the doc saying new axes will be new fields.

### WONTFIX ~~34. Two smaller Go API commitments~~

`Options` will gain fields, which breaks anyone using an unkeyed literal
(`Options{IgnoreInputs, 128}`) — worth a doc line asking for keyed ones. And
`parser.Parse` promises the canonical form "in a single Write", which commits permanently
to buffering a whole document instead of streaming it into the hash. That's right for the
proxy, where `-server.max-body` bounds it, but a library caller has no such bound.

### 35. Refusals answer `application/json`, never `application/graphql-response+json`

Verified with `Accept: application/graphql-response+json`: the answer is
`application/json; charset=utf-8`. GraphQL over HTTP treats `application/json` as the
legacy watershed, so a conformant client still parses it. If the modern media type is
wanted on the proxy's own error envelopes, choosing it now is cheaper than changing the
content type of every refusal later.

### ✅ ~~36. `-opaque-errors` hides why, not whether~~

With the upstream unreachable, an allowed document answers `502 UPSTREAM_UNAVAILABLE`
while a rejected one answers `403 OPERATION_NOT_ALLOWED`. That's coherent — the flag
covers rejections, and an allowed document reveals itself through the API's own answer
regardless — but the name invites the stronger reading. One sentence in the flag's
documentation settles it permanently.

### Surfaces that look right

Status codes and extension codes (`OPERATION_NOT_ALLOWED`, `BAD_REQUEST`,
`REQUEST_TOO_LARGE`, `BATCH_TOO_LARGE`, `METHOD_NOT_ALLOWED`, `UPSTREAM_UNAVAILABLE`), the
`/status` keys, and the metric names, which follow Prometheus conventions —
`_total` on counters, `_seconds` on the histogram, a timestamp gauge in seconds. `/status`
and the metrics are additive-friendly, so little is locked there: a `build_info` metric or
a health endpoint can arrive whenever. `gqlhash-proxy-fhttp` is absent from
`.goreleaser.yml`, so shipping the experimental build isn't a commitment v2 makes.
