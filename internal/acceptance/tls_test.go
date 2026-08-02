package acceptance

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	certFile = filepath.Join(t.TempDir(), "ca.pem")
	encoded := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw,
	})
	if err := os.WriteFile(certFile, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return upstream.URL + "/graphql", certFile
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
