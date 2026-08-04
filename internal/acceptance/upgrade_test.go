package acceptance

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// upgradeAPI answers 101 to a request offering an upgrade and records whatever
// arrives after, which is what a tunnel through the proxy looks like from behind it.
//
// Raw rather than an [http.Server] because what matters is what reached it byte
// for byte: net/http hands over a header already parsed and folded,
// and the bytes after a 101 are no HTTP message at all.
type upgradeAPI struct {
	url string

	// always101 answers 101 to every request, upgrade offered or not,
	// which is the broken API the proxy must not relay.
	always101 bool

	mu       sync.Mutex
	heads    []string // The request head of everything that reached it.
	tunneled []string // Bytes that arrived after it answered 101.
}

// newUpgradeAPI starts one. With always101 it answers 101 to every request;
// without, only to one that offered an upgrade, like an API that speaks
// WebSocket beside GraphQL.
func newUpgradeAPI(t *testing.T, always101 bool) *upgradeAPI {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	api := &upgradeAPI{
		url:       "http://" + listener.Addr().String() + "/graphql",
		always101: always101,
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go api.handle(conn)
		}
	}()
	return api
}

func (a *upgradeAPI) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)

	var head strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		head.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	received := head.String()
	a.mu.Lock()
	a.heads = append(a.heads, received)
	a.mu.Unlock()

	if !a.always101 &&
		!strings.Contains(strings.ToLower(received), "upgrade:") {
		// One answer per connection, so what reached this one stays readable.
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"+
			"Content-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(upstreamAnswer), upstreamAnswer)
		return
	}

	protocol := "websocket"
	if strings.Contains(strings.ToLower(received), "h2c") {
		protocol = "h2c"
	}
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n", protocol)

	// Whatever the client sends once it believes it has a tunnel.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	if n, _ := reader.Read(buf); n > 0 {
		a.mu.Lock()
		a.tunneled = append(a.tunneled, string(buf[:n]))
		a.mu.Unlock()
	}
}

// forwarded is every request that reached the API, whole.
func (a *upgradeAPI) forwarded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.heads...)
}

// smuggled is every run of bytes that reached the API after it answered 101.
func (a *upgradeAPI) smuggled() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.tunneled...)
}

// TestProtocolUpgrade covers a client offering to leave HTTP behind.
//
// The offer turns one allowlisted document into an unhashed channel:
// the request carrying it is hashed and allowed, the API answers 101,
// and everything written from then on reaches the API without passing the allowlist
// again. One allowed document buys a subscription to anything.
//
// So the upgrade stops here under both commands: the offer isn't forwarded and
// the request is decided like any other.
func TestProtocolUpgrade(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newUpgradeAPI(t, false)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir)

		allowed := "query=" + url.QueryEscape(allowedText)
		body := docAllowed

		for _, tc := range []struct {
			name     string
			request  string
			status   string
			decision string
			forwards int
		}{
			{
				// The handshake RFC 6455 defines, carrying a document that is
				// on the list, which is how the channel is paid for.
				name: "a websocket handshake carrying an allowed document",
				request: "GET /graphql?" + allowed + " HTTP/1.1\r\nHost: x\r\n" +
					"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
					"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
					"Sec-WebSocket-Version: 13\r\n\r\n",
				status: "HTTP/1.1 200", decision: "allowed", forwards: 1,
			},
			{
				// What curl --http2 sends in cleartext, so this one arrives
				// without anybody meaning to attack anything.
				name: "an h2c upgrade offer carrying an allowed document",
				request: "GET /graphql?" + allowed + " HTTP/1.1\r\nHost: x\r\n" +
					"Upgrade: h2c\r\nConnection: Upgrade, HTTP2-Settings\r\n" +
					"HTTP2-Settings: AAMAAABkAARAAAAAAAIAAAAA\r\n\r\n",
				status: "HTTP/1.1 200", decision: "allowed", forwards: 1,
			},
			{
				name: "a POST carrying an allowed body and an upgrade offer",
				request: fmt.Sprintf("POST /graphql HTTP/1.1\r\nHost: x\r\n"+
					"Content-Type: application/json\r\n"+
					"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
					"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
					"Sec-WebSocket-Version: 13\r\n"+
					"Content-Length: %d\r\n\r\n%s", len(body), body),
				status: "HTTP/1.1 200", decision: "allowed", forwards: 1,
			},
			{
				// The offer alone names no document, so it's refused like any
				// other request that names none: an upgrade is no way in.
				name: "a handshake naming no document",
				request: "GET /graphql HTTP/1.1\r\nHost: x\r\n" +
					"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
					"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
					"Sec-WebSocket-Version: 13\r\n\r\n",
				status: "HTTP/1.1 400", decision: "malformed", forwards: 0,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				series := fmt.Sprintf(
					`gqlhash_proxy_requests_total{decision=%q}`, tc.decision)
				before := decisionCount(t, s, series)
				forwardsBefore := len(api.forwarded())

				conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = conn.Close() }()
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.Write([]byte(tc.request)); err != nil {
					t.Fatal(err)
				}

				status, _ := readAnswer(t, conn)
				if !strings.HasPrefix(status, tc.status) {
					t.Fatalf("expected %s; received %q: %s", tc.status, status, s.log)
				}

				// Read before the client writes anything else: what follows is
				// no HTTP message, and a request that produced a status of its
				// own would be counted too.
				if after := decisionCount(t, s, series); after != before+1 {
					t.Errorf("expected %s to move by one; %v to %v",
						series, before, after)
				}

				// The bytes a client sends believing it has a tunnel.
				_, _ = conn.Write([]byte(`{"query":"subscription{secrets{token}}"}`))
				time.Sleep(300 * time.Millisecond)

				if smuggled := api.smuggled(); len(smuggled) > 0 {
					t.Errorf("expected nothing tunnelled to the API; received %q",
						smuggled)
				}
				received := api.forwarded()
				if got := len(received) - forwardsBefore; got != tc.forwards {
					t.Errorf("expected %d requests forwarded; received %d: %q",
						tc.forwards, got, received[forwardsBefore:])
				}
				for _, head := range received[forwardsBefore:] {
					// The offer itself, and the headers Connection named with
					// it: an API that would have answered 101 never learns
					// there was an upgrade to answer.
					if strings.Contains(strings.ToLower(head), "upgrade") {
						t.Errorf("expected the offer stopped here; the API received:\n%s",
							head)
					}
					if strings.Contains(strings.ToLower(head), "http2-settings") {
						t.Errorf("expected the headers Connection named dropped;"+
							" the API received:\n%s", head)
					}
				}
			})
		}
	})
}

// TestUpstreamAnswering101 covers the other half of the rule: an API that
// answers 101 to a request that never offered an upgrade.
//
// Relaying it would splice the two connections together on the strength of the
// API's answer alone, so a single misbehaving or compromised API turns every
// allowed request into a channel. It's an upstream failure instead,
// which is what a 101 nobody asked for is.
func TestUpstreamAnswering101(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		api := newUpgradeAPI(t, true)
		s := serve(t, tgt, "-upstream.url", api.url, "-allowlist", dir)

		// A GET, so nothing of the client's own follows the head:
		// what reaches the API after its 101 came through the proxy or not at all.
		code, answer := get(t, s, "query="+url.QueryEscape(allowedText))
		if code != http.StatusBadGateway {
			t.Errorf("expected 502; received %d: %s", code, answer)
		}

		// Whatever the client sends next reaches nothing.
		time.Sleep(300 * time.Millisecond)
		if smuggled := api.smuggled(); len(smuggled) > 0 {
			t.Errorf("expected nothing tunnelled to the API; received %q", smuggled)
		}

		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition,
			"gqlhash_proxy_upstream_errors_total"); got != 1 {
			t.Errorf("expected one upstream error; received %v", got)
		}
		if got := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="allowed"}`); got != 1 {
			t.Errorf("expected the request counted as allowed; received %v", got)
		}
	})
}

// decisionCount reads one decision counter off the control server.
func decisionCount(t *testing.T, s *server, series string) float64 {
	t.Helper()
	_, exposition := control(t, s, http.MethodGet, "/metrics", "")
	return metricValue(t, exposition, series)
}
