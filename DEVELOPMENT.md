# Development

Building, testing and covering gqlhash, and running the web playground. What it
does and how to use it is in the [README](README.md).

## Testing

```sh
make                               # lint, run all tests, report coverage
make test                          # tests alone
make acceptance                    # both proxy binaries, over real HTTP
make acceptance FHTTP=0            # without the experimental fasthttp build, ~2x faster
make acceptance PROXY=./my-proxy   # any implementation of the same contract
```

`./internal/acceptance` starts a real server process and drives it over HTTP, which is what lets a server written in another language be tested the same way. The contract is documented in `internal/acceptance/doc.go`.

The tests that need no flags of their own share one server per binary and load the allowlist they need through the control plane, so `make` runs them with `-shuffle=on`: a test that depends on running after another fails, and the seed to reproduce it is printed.

`FHTTP=0` leaves out `gqlhash-proxy-fhttp`, the experimental fasthttp build, and roughly halves the runtime. Every rule that build keeps is one `gqlhash-proxy` keeps too, so such a run still covers every rule — what it stops covering is whether the two **agree**, which is the reason both are here. Leave it out while working on a rule; leave it in before committing one. It has no effect with `PROXY=`, which names the target outright.

## Coverage

`make` reports it, `make cover` on its own. Two runs, since the acceptance suite drives the proxy as a separate process that `-coverprofile` can't see: `cover-unit` reports what the tests reach in process, `cover-servers` what the running servers reach. The second needs an absolute `GOCOVERDIR` and no `-cover` flag, or it silently collects nothing; the Makefile handles both. `make cover-profile` converts the servers' counters into a profile.

## Web playground

[web/](web/README.md) is the page published at https://romshark.github.io/gqlhash/: the hasher compiled to WebAssembly, driving two tabbed editors that check operations against an allowlist. It needs Go (the version in [go.mod](go.mod)) and Node 20+ with [pnpm](https://pnpm.io). Nothing in `make` reaches it — it has its own toolchain and its own checks.

```sh
cd web
pnpm install
pnpm dev      # build the wasm binary, then serve with hot reloading
pnpm run ci   # what CI checks: biome ci . plus tsc --noEmit
pnpm build    # wasm, typecheck and bundle to web/dist
```

`pnpm dev` watches the Go sources the binary is compiled from — `web/wasm`, the root package and `parser` — rebuilds `web/src/wasm/gqlhash.wasm` when one of them changes, and the page reloads once the build finishes. A build that fails prints the compile error to the terminal running it and leaves the binary from before it in place, so the page keeps serving until the error is fixed.

That binary and `web/src/vendor/wasm_exec.js` are build outputs, hence git-ignored. `wasm_exec.js` is copied from the toolchain in use rather than vendored, since it has to match the compiler that produced the binary.

The page's components come from [Morpheus](https://github.com/romshark/morpheus), whose shippable bundle is committed under `web/src/vendor/morpheus` (with the icons it fetches in `web/public/icons`) rather than installed from a registry. Nothing builds it: updating the kit is copying two files, as [its README there](web/src/vendor/morpheus/README.md) records.

[.github/workflows/pages.yml](.github/workflows/pages.yml) lints, builds and publishes `web/dist` on every push to `main` touching `web/`, `parser/` or the root package, so a change to the hasher redeploys the page that demonstrates it.

The layout of the sources, the palette, the icons and what the controls mean are in [web/README.md](web/README.md). It's a different thing from [playground/](playground/README.md), which is the Docker Compose demo of `gqlhash-proxy` in front of a real API.
