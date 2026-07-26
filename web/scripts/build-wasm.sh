#!/bin/sh
# Compiles ../wasm to WebAssembly and copies the matching Go JS support file
# next to it. Both artifacts are generated, hence git-ignored.
set -eu

cd "$(dirname "$0")/.."

GOROOT="$(go env GOROOT)"

mkdir -p src/wasm src/vendor

echo "building src/wasm/gqlhash.wasm ($(go version))"
GOOS=js GOARCH=wasm go build \
	-trimpath \
	-ldflags="-s -w" \
	-o src/wasm/gqlhash.wasm \
	./wasm

# wasm_exec.js is tied to the Go version that built the binary, so it's always
# taken from the toolchain in use instead of being vendored into git.
cp "$GOROOT/lib/wasm/wasm_exec.js" src/vendor/wasm_exec.js

ls -l src/wasm/gqlhash.wasm
