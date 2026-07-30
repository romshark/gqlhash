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
		t.Setenv("SSL_CERT_FILE", certFile)
		trusted := serve(t, tgt, "-upstream.url", url, "-allowlist", dir)
		if code, answer := post(t, trusted, docAllowed); code != http.StatusOK {
			t.Errorf("expected the trusted upstream served; received %d: %s",
				code, answer)
		}
	})
}
