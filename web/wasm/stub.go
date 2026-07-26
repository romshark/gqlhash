//go:build !(js && wasm)

// This package only does something when built for WebAssembly. The stub keeps
// `go build ./...` and `go vet ./...` happy on every other platform.
package main

func main() {}
