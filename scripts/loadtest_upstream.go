//go:build ignore

// Command loadtest_upstream is the GraphQL API the load test puts behind the
// proxy. It answers a fixed body as cheaply as it can, so a load test measures
// the proxy and the generator rather than an API.
//
// Run it with "go run scripts/loadtest_upstream.go -listen 127.0.0.1:14001".
package main

import (
	"flag"
	"io"
	"log"
	"net/http"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:14001", "Address to listen on")
	flag.Parse()

	answer := []byte(`{"data":{"user":{"name":"Ada","email":"ada@example.com"}}}`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(answer)
	})

	server := &http.Server{Addr: *listen, Handler: handler}
	log.Printf("upstream listening on %s", *listen)
	log.Fatal(server.ListenAndServe())
}
