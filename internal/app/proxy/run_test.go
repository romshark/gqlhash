package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/app/config"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRunProxyFlagErrors(t *testing.T) {
	f := func(t *testing.T, expectCode int, args ...string) {
		t.Helper()
		var out, errOut strings.Builder
		code := Run(context.Background(), "gqlhash-proxy", "dev",
			append([]string{"gqlhash-proxy", "-control.listen", "127.0.0.1:0"}, args...),
			&out, &errOut)
		if code != expectCode {
			t.Errorf("expected code %d; received %d; args %v; stderr: %s",
				expectCode, code, args, errOut.String())
		}
	}

	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ foo }")

	// The flags are validated by the config package, which tests every value.
	// What's left here is what only a run can fail at.
	f(t, 2, "-upstream.url", "http://x", "-allowlist", dir, "-log.level", "nope")

	// A directory that can't be read stops the start. An empty one, or one holding
	// only documents that don't parse, doesn't: it serves and rejects everything,
	// which the allowlist tests cover.
	f(t, 1, "-upstream.url", "http://x", "-allowlist", filepath.Join(dir, "nope"))

	// An address already in use is a start failure, not a panic.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	f(t, 1, "-upstream.url", "http://x", "-allowlist", dir,
		"-server.listen", held.Addr().String())

	var out, errOut strings.Builder
	if code := Run(context.Background(), "gqlhash-proxy", "1.2.3-test",
		[]string{"gqlhash-proxy", "-version"}, &out, &errOut); code != 0 {
		t.Errorf("expected code 0; received %d", code)
	}
	if !strings.Contains(out.String(), "gqlhash-proxy v1.2.3-test") {
		t.Errorf("expected a version; received %q", out.String())
	}
}

// syncBuffer collects log output while the server writes to it from its own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// findLogged returns a field of the first log event with the given message.
func findLogged(logs, message, field string) string {
	for line := range strings.SplitSeq(logs, "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event["message"] != message {
			continue
		}
		if v, ok := event[field].(string); ok {
			return v
		}
	}
	return ""
}

// upstreamServer answers like a GraphQL API and records what it received.
func upstreamServer(t *testing.T) (*url.URL, *upstreamSpy) {
	t.Helper()
	spy := new(upstreamSpy)
	mux := http.NewServeMux()
	mux.Handle("/graphql", spy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return mustURL(t, server.URL+"/graphql"), spy
}

// TestRunProxyControlAddressInUse makes sure a control address that can't be
// bound stops the start instead of serving without it.
func TestRunProxyControlAddressInUse(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	var out, errOut strings.Builder
	code := Run(context.Background(), "gqlhash-proxy", "dev", []string{"gqlhash-proxy",
		"-server.listen", "127.0.0.1:0",
		"-upstream.url", "http://127.0.0.1:1/graphql",
		"-allowlist", dir,
		"-control.listen", held.Addr().String(),
	}, &out, &errOut)
	if code != 1 {
		t.Errorf("expected code 1; received %d; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "listening for the control server") {
		t.Errorf("expected the control bind to be reported; received %s",
			errOut.String())
	}
}

// TestBuildWiring asserts that every field of a config reaches the component
// it configures. A swapped field is what this catches,
// which no behavioural test would show as anything but a puzzling result.
func TestBuildWiring(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")

	// Every value differs from its default,
	// so a field that reads the wrong source is visible.
	cfg := config.Proxy{
		AllowlistDir:   dir,
		HashFunc:       config.HashFunctionBLAKE3,
		Ignore:         gqlhash.IgnoreInputs,
		AllowBatch:     true,
		OpaqueErrors:   true,
		TrustForwarded: true,
		Server: config.ProxyServer{
			Listen:          "127.0.0.1:0",
			MaxBody:         4096,
			ShutdownTimeout: 13 * time.Second,
			// Every listener timeout differs from the others,
			// so a field that reads the wrong one is visible.
			ReadHeaderTimeout: 3 * time.Second,
			ReadTimeout:       17 * time.Second,
			WriteTimeout:      23 * time.Second,
			IdleTimeout:       29 * time.Second,
		},
		Upstream: config.ProxyUpstream{
			URL:                 mustURL(t, "http://upstream.example:4000/graphql"),
			Timeout:             11 * time.Second,
			MaxIdleConnsPerHost: 7,
			MaxIdleConns:        9,
			HTTP2:               false,
		},
		Control: config.ProxyControl{Address: "127.0.0.1:0", Token: "s3cret"},
		Log: config.ProxyLog{
			Level: "debug", JSON: true, Requests: true,
		},
	}

	log, ok := newLogger(cfg, io.Discard)
	if !ok {
		t.Fatal("expected the log level to parse")
	}
	c, err := build(cfg, log, ServerImpl{})
	if err != nil {
		t.Fatal(err)
	}

	// The handler.
	p := c.proxy
	if p.options.Ignore != cfg.Ignore {
		t.Errorf("Ignore: expected %v; received %v", cfg.Ignore, p.options.Ignore)
	}
	// -log.requests only logs where the level keeps a debug event.
	if !p.logRequests || !p.debug {
		t.Errorf("expected debug logging of requests; debug %t requests %t",
			p.debug, p.logRequests)
	}
	// The allowlist. A hit for a document of cfg.AllowlistDir under the key of
	// cfg.HashFunc and cfg.Ignore covers the directory, the hash function and
	// the ignore mode in one assertion.
	h, _ := config.NewHasher(cfg.HashFunc)
	sum, errHash := gqlhash.AppendHash(nil, h,
		gqlhash.Options{Ignore: cfg.Ignore}, "{ a }")
	if errHash.IsErr() {
		t.Fatal(errHash)
	}
	if !c.allowlist.Allowed(sum) {
		t.Error("expected the allowlist to be keyed by -hash and -ignore")
	}

	// The servers. net/http is the default implementation, so these are its fields.
	under, okU := c.dataPlane.(netHTTPServer)
	if !okU {
		t.Fatalf("expected the net/http server by default; received %T", c.dataPlane)
	}
	srv := under.server
	if srv.Addr != cfg.Server.Listen {
		t.Errorf("Listen: expected %q; received %q", cfg.Server.Listen, srv.Addr)
	}
	if srv.ReadHeaderTimeout != cfg.Server.ReadHeaderTimeout ||
		srv.ReadTimeout != cfg.Server.ReadTimeout ||
		srv.WriteTimeout != cfg.Server.WriteTimeout ||
		srv.IdleTimeout != cfg.Server.IdleTimeout {
		t.Errorf("a listener timeout didn't reach the server: %+v", srv)
	}
	if c.control == nil {
		t.Fatal("expected a control server, -control.listen names an address")
	}
	if c.control.Addr != cfg.Control.Address {
		t.Errorf("Control: expected %q; received %q",
			cfg.Control.Address, c.control.Addr)
	}
	if c.control.Addr == srv.Addr && cfg.Control.Address != cfg.Server.Listen {
		t.Error("expected the control server on its own address")
	}
	transport, okT := c.proxy.upstream.Transport.(*http.Transport)
	if !okT {
		t.Fatalf("expected an *http.Transport; received %T",
			c.proxy.upstream.Transport)
	}
	if transport.ResponseHeaderTimeout != cfg.Upstream.Timeout {
		t.Errorf("Timeout: expected %v upstream; received %v",
			cfg.Upstream.Timeout, transport.ResponseHeaderTimeout)
	}
	if transport.MaxIdleConnsPerHost != cfg.Upstream.MaxIdleConnsPerHost ||
		transport.MaxIdleConns != cfg.Upstream.MaxIdleConns ||
		transport.ForceAttemptHTTP2 != cfg.Upstream.HTTP2 {
		t.Errorf("the upstream connection settings didn't reach the transport: "+
			"%+v", transport)
	}

}

// TestNewLogger covers the flags the logger is built from.
func TestNewLogger(t *testing.T) {
	var out strings.Builder
	log, ok := newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "info", JSON: true},
	}, &out)
	if !ok {
		t.Fatal("expected info to parse")
	}
	log.Info().Str("k", "v").Msg("hello")
	if !strings.Contains(out.String(), `"message":"hello"`) {
		t.Errorf("expected JSON; received %q", out.String())
	}

	// The readable format is no JSON.
	out.Reset()
	log, ok = newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "info", JSON: false},
	}, &out)
	if !ok {
		t.Fatal("expected info to parse")
	}
	log.Info().Msg("hello")
	if strings.Contains(out.String(), `"message"`) {
		t.Errorf("expected text; received %q", out.String())
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("expected the message; received %q", out.String())
	}

	// The level drops what's below it.
	out.Reset()
	log, ok = newLogger(config.Proxy{
		Log: config.ProxyLog{Level: "error", JSON: true},
	}, &out)
	if !ok {
		t.Fatal("expected error to parse")
	}
	log.Info().Msg("dropped")
	log.Error().Msg("kept")
	if strings.Contains(out.String(), "dropped") {
		t.Errorf("expected info to be dropped; received %q", out.String())
	}
	if !strings.Contains(out.String(), "kept") {
		t.Errorf("expected the error; received %q", out.String())
	}

	// An unknown level is reported to stderr, not guessed at.
	out.Reset()
	if _, ok := newLogger(config.Proxy{Log: config.ProxyLog{Level: "shout"}}, &out); ok {
		t.Error("expected an unknown level to fail")
	}
	if !strings.Contains(out.String(), `unsupported log level "shout"`) {
		t.Errorf("expected the level in the message; received %q", out.String())
	}
}

// TestRunProxyServeFails covers the data-plane server failing while it runs, which
// is neither a bind failure nor a shutdown: the listener goes away under it.
// That's the one way out of a run that isn't asked for,
// so it has to be reported and it has to leave a nonzero exit code behind.
func TestRunProxyServeFails(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "a.graphql", "{ a }")
	upstream, _ := upstreamServer(t)

	logs := new(syncBuffer)
	log := zerolog.New(logs)
	cfg := config.Proxy{
		AllowlistDir: dir,
		HashFunc:     config.HashFunctionSHA2,
		Server: config.ProxyServer{
			Listen:          "127.0.0.1:0",
			MaxBody:         1 << 20,
			ShutdownTimeout: 5 * time.Second,
		},
		Upstream: config.ProxyUpstream{URL: upstream, Timeout: 5 * time.Second},
	}
	c, err := build(cfg, log, ServerImpl{})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// Nothing cancels the context: the run ends because serving ended.
	code := make(chan int, 1)
	go func() {
		code <- c.serveOn(context.Background(), listener, cfg, log)
	}()

	// The listener is taken away from under a server that is already serving,
	// which is what a serving failure looks like: Serve stops with an error that
	// is no ErrServerClosed.
	waitFor(t, func() bool {
		return findLogged(logs.String(), "listening", "address") != ""
	}, "the server to report listening")
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case received := <-code:
		if received != 1 {
			t.Errorf("expected the run to fail; received %d", received)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run didn't end")
	}

	if !strings.Contains(logs.String(), `"message":"serving"`) {
		t.Errorf("expected the failure to be logged; received %s", logs.String())
	}
	// It isn't a shutdown, so nothing says it was one.
	if strings.Contains(logs.String(), "shutting down") {
		t.Errorf("expected no shutdown; received %s", logs.String())
	}
}
