# Development

Building, testing and covering gqlhash. What it does and how to use it is in
the [README](README.md).

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
