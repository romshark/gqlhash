# v2 pre-release review (branch `new-api`)

Reviewed the whole branch: parser, package API, both proxies, config, and all five
docs. Everything builds, `go vet` and the full test suite pass, `gofmt` is clean,
and every hash in the README reproduces. Below is what to fix first.

## Bugs

### 1. Allowlist bypass via a JSON-escaped `query` key (high)

`internal/app/proxy/extract.go:159` (`isQueryKey`) compares the raw key bytes,
case-insensitively, against exactly `"query"` — 7 bytes. A key written with a JSON
escape doesn't match:

```json
{"query":"{a}","quer\u0079":"{b}"}
```

(the second key is "quer\u0079" — \u0079 is `y`, so it spells `query` too)

Both halves confirmed:

- `extractJSON` finds **one** span, `{a}` — the decoy. Put an allowed document
  there and the request is forwarded.
- Go's `encoding/json` decodes that body to `Query: "{b}"` — the escaped key
  unescapes to `query` and the last duplicate wins. (Same for `JSON.parse`.)

So `{b}` reaches the API without ever being hashed. The comment above `isQueryKey`
reasons carefully about exactly this attack for the `"queRY"` spelling but stops at
case, not escapes.

**Fix:** if a key contains `\`, unescape it before comparing (`unescapeJSON` already
exists), so it becomes another `query` member that must also be allowed.

### 2. fasthttp turns `-upstream.max-idle-conns-per-host` into a hard concurrency cap (high)

`internal/app/proxyfast/proxyfast.go:77` maps it to `fasthttp.HostClient.MaxConns`,
which caps connections *opened*, not kept, and with `MaxConnWaitTimeout` unset the
surplus request fails immediately with `ErrNoFreeConns` → 502 plus an
upstream-error count. Test with the cap at 2 and 8 concurrent allowed requests:

```
nethttp:  [200 200 200 200 200 200 200 200]
fasthttp: [502 502 200 502 200 502 502 502]
```

At the default of 64 this is a cliff any deployment above 64 concurrent forwards
falls off. It also directly contradicts `TUNING_GQLHASH_PROXY.md:28` ("caps
connections kept, not opened; past the cap the surplus is redialed per request"),
which is written as if it described both commands. The load test hides it —
`internal/cmd/loadtest/main.go:458` sizes the pool to the connection count.

**Fix:** raise `MaxConns` well above the flag, or set `MaxConnWaitTimeout` so
requests queue instead of failing.

### 3. Flags set through the environment are invisible to `isSet` (medium)

`internal/app/config/flags.go:491` calls `f.Value.Set(...)` directly, which doesn't
record the flag in the flag set's `actual` map, so `isSet`
(`internal/app/config/flags.go:480`) never sees it — despite the comment claiming
"on the command line or through the environment, which applyEnv sets the same way."

`GQLHASH_PROXY_SERVER_WRITE_TIMEOUT=5s` → parsed value 40s. The operator's value is
silently discarded, and the guard that rejects a write timeout below
`-upstream.timeout` never runs. Same for `upstream.http2` on the fhttp binary: the
env form is accepted silently instead of refused.

**Fix:** `cli.Set(f.Name, value)`.

### 4. fasthttp rejects GET documents over ~4 KB (medium)

`fasthttp.Server.ReadBufferSize` isn't set, so it defaults to 4096 and the request
line is the limit for a document carried in the query string:

```
url len 3090:  nethttp 200   fasthttp 200
url len 8090:  nethttp 200   fasthttp 400
url len 60090: nethttp 200   fasthttp 400
```

`-server.max-body` doesn't cover this and nothing documents it.

### 5. `-help` names an environment variable that doesn't exist

`internal/app/config/flags.go:320` prints `GQLHASH_PROXY_MAX_BODY`; the flag is
`server.max-body`, so the real name is `GQLHASH_PROXY_SERVER_MAX_BODY` (which the
README gets right). The same sentence says "dashes as underscores" and omits dots.

## Docs

- `parser/parser.go:78` — `Error.Err` is documented as "`ErrUnexpectedEOF`,
  `ErrUnexpectedToken`, one of the errors wrapping `ErrUnexpectedToken`, or the
  error of the `io.Writer`". `ErrTooDeep` is none of those and isn't listed.
- `parser/parser.go:228` — `DepthLimit`: "0 takes `DefaultDepthLimit`" — the code is
  `< 1`, so negatives do too, and there's no way to turn the limit off.
- **README anchors**: `](#gqlhash-proxy)` at lines 106, 164 and 286 resolves to the
  `### gqlhash-proxy` brew snippet under Installation, not to `## Usage: Proxy`
  (`#usage-proxy`). That install subsection (lines 154–159) also just repeats lines
  128–132.
- **playground/README.md**: `../README.md#proxy` is a dead anchor, and it quotes a
  log line, `skipping a document that doesn't parse`, that the code doesn't emit —
  `internal/app/proxy/run.go:339` logs `skipping a document`.
- **Known Limitations is missing numeric spelling.** Values aren't normalized:
  `1.0`, `1.00`, `1e2` and `100.0` all hash differently. Strings are normalized,
  which makes the asymmetry worth a line — a client whose serializer emits `1.00`
  breaks its allowlist entry.
- **`GQLHASH_PROXY_FHTTP.md` "Costs" is incomplete** given #2 and #4:
  `-upstream.max-idle-conns` is ignored, `-upstream.max-idle-conns-per-host` changes
  meaning, `-server.read-header-timeout` has no fasthttp equivalent. "Two commands,
  same flags" is currently stronger than the code.
- **Number drift**: README says `~632,000` rejected where `GQLHASH_PROXY_FHTTP.md`
  measures 629,780, and 0.56 ms p99 against that file's 0.57.
- `internal/app/proxy/run.go:236` — "It stays empty where the control server is off"
  is stale; `-control.listen` can't be empty.
- `.goreleaser.yml` archives `gqlhash-proxy-fhttp` but publishes no Homebrew cask
  for it, while the other two get one.

## API

- **`gqlhash.ErrTooDeep` isn't re-exported.** `gqlhash.go:19-28` forwards all seven
  other sentinels, so `errors.Is(err, gqlhash.ErrTooDeep)` forces a `parser` import
  for one error. `DefaultDepthLimit` and `DefaultBufferSize` are likewise
  parser-only, and `DefaultDepthLimit` is the value a caller needs to reason about
  `Options.DepthLimit`. This is the one inconsistency worth calling a release
  blocker for the API surface — adding it later is fine, but the asymmetry reads as
  an oversight.
- **Neither command exposes the depth limit.** 128 is hard-wired for `gqlhash` and
  `gqlhash-proxy`, so a legitimately deep document can't be hashed or allowlisted at
  all. A `-depth-limit` flag on both is the obvious companion to the new option.
- **`NewHasher` can't set the parser buffer size** — it hardcodes
  `parser.NewParser[S](0)`, so the one knob `DefaultBufferSize` documents is
  unreachable from the top-level package.

## One spec question worth settling

The parser accepts descriptions where the spec's grammar has none, and vektah
disagrees on all three:

```
"a description" query Q { x }        gqlhash: ok    vektah: Unexpected String
"d" fragment F on T { x } { ...F }   gqlhash: ok    vektah: Unexpected String
query Q("desc" $x: Int) { f(a:$x) }  gqlhash: ok    vektah: Expected $, found String
```

Not a bypass — descriptions don't affect execution, so the hash is unchanged — but
the proxy forwards documents the API will answer with a syntax error, and
`gqlhash.go:1` claims conformance with September 2025. Either it's deliberate
leniency and belongs in Known Limitations, or the `DEFINITION`/`VARDEF` description
branches (`parser/parse.go:96`, `parser/parse.go:235`) should go. Confirm whether
September 2025 actually added `Description` to `VariableDefinition` before deciding.
