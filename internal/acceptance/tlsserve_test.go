package acceptance

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serverKeyPair writes a self-signed certificate for 127.0.0.1 and its key,
// and returns the file names along with a pool that trusts it.
func serverKeyPair(t *testing.T) (certFile, keyFile string, trust *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gqlhash-proxy acceptance"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "s.pem"), filepath.Join(dir, "s.key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "PRIVATE KEY", keyDER)

	trust = x509.NewCertPool()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	trust.AddCert(cert)
	return certFile, keyFile, trust
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// postTLS is [post] over https, trusting only the proxy's own certificate.
func postTLS(t *testing.T, s *server, trust *x509.CertPool, body string) (
	code int, answer, proto string,
) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: trust, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost,
		"https://"+s.address+"/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("https request: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b), res.Proto
}

// TestServeTLS covers -server.tls.cert and -server.tls.key: the proxy decides
// the same way over https as it does over http, and the port really is TLS.
func TestServeTLS(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		certFile, keyFile, trust := serverKeyPair(t)
		e := newEnv(t, tgt, []string{allowedDoc},
			"-server.tls.cert", certFile, "-server.tls.key", keyFile)

		// An allowed document is forwarded, a rejected one refused, exactly as
		// over plain HTTP.
		code, answer, proto := postTLS(t, e.server, trust, docAllowed)
		if code != http.StatusOK {
			t.Errorf("expected the allowed document served over https; received %d: %s",
				code, answer)
		}
		t.Logf("%s served over %s", tgt.name, proto)

		if code, answer, _ := postTLS(t, e.server, trust, docRejected); code !=
			http.StatusForbidden {
			t.Errorf("expected 403 for a document that isn't on the list; received %d: %s",
				code, answer)
		}

		// The listener is TLS and nothing else, which is what proves the flags
		// took effect. The two implementations say so differently: net/http answers
		// 400 "Client sent an HTTP request to an HTTPS server", fasthttp drops
		// the connection. Neither decides the document.
		res, err := http.Post( //nolint:noctx // the answer is the assertion
			"http://"+e.address+"/graphql", "application/json",
			strings.NewReader(docAllowed))
		if err == nil {
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("expected plain HTTP to be refused by a TLS listener; received %d",
					res.StatusCode)
			}
		}
	})
}

// TestServeTLSUntrusted covers the other side of it: a client that doesn't
// trust the certificate doesn't get an answer, so the proxy isn't serving
// something that only looks like TLS.
func TestServeTLSUntrusted(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		certFile, keyFile, _ := serverKeyPair(t)
		e := newEnv(t, tgt, []string{allowedDoc},
			"-server.tls.cert", certFile, "-server.tls.key", keyFile)

		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}}
		defer client.CloseIdleConnections()
		res, err := client.Post( //nolint:noctx // the failure is the assertion
			"https://"+e.address+"/graphql", "application/json",
			strings.NewReader(docAllowed))
		if err == nil {
			_ = res.Body.Close()
			t.Errorf("expected an untrusted certificate to be refused; received %d",
				res.StatusCode)
		}
	})
}
