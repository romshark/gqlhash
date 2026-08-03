package acceptance

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tlsUpstream starts an API over TLS and answers with the file its certificate
// was written to, for a proxy that is meant to trust it.
func tlsUpstream(t *testing.T) (url, certFile string) {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, upstreamAnswer)
		}))
	t.Cleanup(upstream.Close)
	return upstream.URL + "/graphql", writeCert(t, upstream)
}

// writeCert writes the certificate of upstream where -upstream.tls.ca can name it.
func writeCert(t *testing.T, upstream *httptest.Server) (certFile string) {
	t.Helper()
	certFile = filepath.Join(t.TempDir(), "ca.pem")
	encoded := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw,
	})
	if err := os.WriteFile(certFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile
}

// h2Upstream starts an API over TLS that offers HTTP/2 over ALPN,
// and reports the protocol the last forwarded request arrived under.
//
// TLS, because that's the only way HTTP/2 is reached here: neither command speaks
// h2c, so an http upstream is HTTP/1.1 whatever -upstream.http2 says.
func h2Upstream(t *testing.T) (url, certFile string, proto func() string) {
	t.Helper()
	var mu sync.Mutex
	var last string
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			last = r.Proto
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, upstreamAnswer)
		}))
	// What makes the listener offer h2 in its ALPN, which is what the flag asks
	// the proxy to accept. Without it the upstream answers HTTP/1.1 either way and
	// the test would pass on a proxy that never tried.
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	t.Cleanup(upstream.Close)

	return upstream.URL + "/graphql", writeCert(t, upstream), func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// TestUpstreamHTTP2 covers -upstream.http2 at a running server, which is what the
// config tests can't see: whether the flag reaches the transport that carries a forward,
// or is taken and dropped.
//
// The one flag whose meaning depends on the command, so this is the one test that
// asks which one it's running. gqlhash-proxy negotiates h2 with an upstream that
// offers it, and reaches the same one over HTTP/1.1 with the flag off.
// gqlhash-proxy-fhttp speaks HTTP/1.1 on both sides, so it refuses the flag at
// startup rather than serving something other than what it was asked for — pinned
// here against the running command, not only in the parsing.
func TestUpstreamHTTP2(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		url, certFile, proto := h2Upstream(t)
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		if tgt.name == experimental {
			code, _, stderr := run(t, tgt, "-upstream.url", url, "-allowlist", dir,
				"-upstream.tls.ca", certFile, "-upstream.http2")
			if code != exitBadArgs {
				t.Errorf("expected exit %d for a flag it can't serve; received %d: %s",
					exitBadArgs, code, stderr)
			}
			if !strings.Contains(stderr, "-upstream.http2 can't be served by") {
				t.Errorf("expected the reason named; received %s", stderr)
			}

			// Off, it serves the same upstream over HTTP/1.1.
			s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
				"-upstream.tls.ca", certFile, "-upstream.http2=false")
			if code, answer := post(t, s, docAllowed); code != http.StatusOK {
				t.Fatalf("expected it serving; received %d: %s", code, answer)
			}
			if got := proto(); got != "HTTP/1.1" {
				t.Errorf("expected the forward over HTTP/1.1; received %s", got)
			}
			return
		}

		// On by default, so an upstream offering h2 is reached over it. A CA of its
		// own is set here too, which is where net/http would otherwise leave h2 off:
		// a custom TLSClientConfig disables it unless the transport forces it.
		on := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.tls.ca", certFile)
		if code, answer := post(t, on, docAllowed); code != http.StatusOK {
			t.Fatalf("expected it serving; received %d: %s", code, answer)
		}
		if got := proto(); got != "HTTP/2.0" {
			t.Errorf("expected the forward over HTTP/2; received %s", got)
		}

		// Off, the same upstream is reached over HTTP/1.1,
		// which is the half a deployment sets the flag for.
		off := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.tls.ca", certFile, "-upstream.http2=false")
		if code, answer := post(t, off, docAllowed); code != http.StatusOK {
			t.Fatalf("expected it serving; received %d: %s", code, answer)
		}
		if got := proto(); got != "HTTP/1.1" {
			t.Errorf("expected the forward over HTTP/1.1; received %s", got)
		}
	})
}

// TestUpstreamTLSVerified covers an https upstream: the certificate is checked,
// and a proxy that skips the check is one a machine on the path can stand in for.
// Every document then goes to whoever answered.
//
// The trusted half names the CA with -upstream.tls.ca rather than SSL_CERT_FILE,
// which crypto/x509 reads on every unix but macOS, where the platform verifier
// takes over. The flag is what an operator behind a private CA has either way.
func TestUpstreamTLSVerified(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		url, certFile := tlsUpstream(t)
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		// Nothing trusts this certificate, so nothing is forwarded to it.
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir)
		code, answer := post(t, s, docAllowed)
		if code != http.StatusBadGateway {
			t.Fatalf("expected 502 for a certificate nothing trusts; received %d: %s",
				code, answer)
		}
		if !strings.Contains(answer, "UPSTREAM_UNAVAILABLE") {
			t.Errorf("expected an upstream error; received %s", answer)
		}

		// Trusted, it's an upstream like any other.
		trusted := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.tls.ca", certFile)
		if code, answer := post(t, trusted, docAllowed); code != http.StatusOK {
			t.Errorf("expected the trusted upstream served; received %d: %s",
				code, answer)
		}
	})
}

// TestUpstreamTLSHostname covers what -upstream.tls.ca doesn't relax:
// trusting the signer is not trusting the name. The certificate served here is the one
// the trusted CA signed, reached over a name it doesn't carry, and the forward fails —
// a proxy that took the CA as permission to skip the rest would serve it.
func TestUpstreamTLSHostname(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		url, certFile := tlsUpstream(t)
		// The certificate httptest serves carries 127.0.0.1, ::1 and example.com.
		// localhost resolves to the same listener and is on none of them.
		wrongName := strings.Replace(url, "127.0.0.1", "localhost", 1)
		if wrongName == url {
			t.Fatalf("expected a loopback upstream to rename; received %s", url)
		}
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)

		s := serve(t, tgt, "-upstream.url", wrongName, "-allowlist", dir,
			"-upstream.tls.ca", certFile)
		code, answer := post(t, s, docAllowed)
		if code != http.StatusBadGateway {
			t.Fatalf("expected 502 for a name the certificate doesn't carry; received %d: %s",
				code, answer)
		}
		if !strings.Contains(answer, "UPSTREAM_UNAVAILABLE") {
			t.Errorf("expected an upstream error; received %s", answer)
		}
	})
}
