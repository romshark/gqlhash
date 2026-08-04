# Development

How to build, test and measure coverage for gqlhash, and how to run the web
playground. What gqlhash does and how to use it is in the [README](README.md).

## Testing

```sh
make                               # lint, run all tests, report coverage
make test                          # tests alone
make fmt                           # format the sources the way lint wants them
make acceptance                    # both proxy binaries, over real HTTP
make acceptance FHTTP=0            # without the experimental fasthttp build, ~2x faster
make acceptance PROXY=./my-proxy   # any implementation of the same contract
```

`./internal/acceptance` starts a real server process and drives it over HTTP. That is why it can test a server written in another language just as well. The contract is documented in `internal/acceptance/doc.go`.

Tests that need no flags of their own share one server per binary and load their allowlist through the control plane. Because they share, `make` runs them with `-shuffle=on`: a test that only passes when it runs after another one fails, and the run prints the seed to reproduce it.

`FHTTP=0` leaves out `gqlhash-proxy-fhttp`, the experimental fasthttp build, and roughly halves the runtime. It enforces no rule that `gqlhash-proxy` doesn't, so a run without it still covers every rule. What you lose is the check that the two **agree**, which is the reason both are here. Leave it out while working on a rule, put it back before you commit one. It does nothing with `PROXY=`, which names the target outright.

## Formatting

The formatter is [gofumpt](https://github.com/mvdan/gofumpt): gofmt plus a few rules gofmt was too conservative to take. The one you notice is that a composite literal spanning several lines gets its braces on lines of their own. [.golangci.yml](.golangci.yml) enables it, so `golangci-lint run` reports an unformatted file the way it reports a linter finding — in `make lint` and in [.github/workflows/golangci-lint.yml](.github/workflows/golangci-lint.yml) alike. `make fmt` rewrites the files.

`make fmt` uses `golangci-lint` if it is installed and `gofumpt` if not. Either works, and it prints the install command if neither is there. Without `golangci-lint`, `make lint` checks `gofmt` instead. That is a floor, not the rule: `gofmt` accepts files the real check still refuses.

## Coverage

`make` reports it, `make cover` does it alone. It takes two runs, because the acceptance suite drives the proxy as a separate process that `-coverprofile` cannot see. `cover-unit` reports what the tests reach in process, `cover-servers` what the running servers reach. The second needs an absolute `GOCOVERDIR` and no `-cover` flag, or it silently collects nothing; the Makefile handles both. `make cover-profile` turns the servers' counters into a profile.

## Web playground

[web/](web/README.md) is the page published at https://romshark.github.io/gqlhash/: the hasher compiled to WebAssembly, driving two tabbed editors that check operations against an allowlist. It needs Go (the version in [go.mod](go.mod)) and Node 20+ with [pnpm](https://pnpm.io). Nothing in `make` reaches it — it has its own toolchain and its own checks.

```sh
cd web
pnpm install
pnpm dev      # build the wasm binary, then serve with hot reloading
pnpm run ci   # what CI checks: biome ci . plus tsc --noEmit
pnpm build    # wasm, typecheck and bundle to web/dist
```

`pnpm dev` watches the Go sources the binary is compiled from — `web/wasm`, the root package and `parser` — and rebuilds `web/src/wasm/gqlhash.wasm` when one of them changes. The page reloads once the build finishes. If a build fails, it prints the compile error to the terminal running it and keeps the previous binary, so the page keeps serving until you fix the error.

That binary and `web/src/vendor/wasm_exec.js` are build outputs, so both are git-ignored. `wasm_exec.js` is copied from the toolchain in use instead of vendored, because it has to match the compiler that produced the binary.

The page's components come from [Morpheus](https://github.com/romshark/morpheus). Its shippable bundle is committed under `web/src/vendor/morpheus` (with the icons it fetches in `web/public/icons`) instead of installed from a registry. Nothing builds it: updating the kit means copying two files, as [its README there](web/src/vendor/morpheus/README.md) explains.

[.github/workflows/pages.yml](.github/workflows/pages.yml) lints, builds and publishes `web/dist` on every push to `main` touching `web/`, `parser/` or the root package, so a change to the hasher redeploys the page that demonstrates it.

The layout of the sources, the palette, the icons and what the controls mean are in [web/README.md](web/README.md). Do not confuse it with [playground/](playground/README.md), the Docker Compose demo of `gqlhash-proxy` in front of a real API.
