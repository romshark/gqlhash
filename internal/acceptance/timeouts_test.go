package acceptance

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// The -server.* durations, observed at runtime rather than at startup. Only
// -server.read-header-timeout was, and it has no fasthttp equivalent, so the
// flags below were the ones an implementation could accept and then ignore.

// TestReadTimeoutBoundsTheBody covers a client that completes its headers and
// then dribbles its body.
//
// The half of the pair that has to hold on both: -server.read-header-timeout
// bounds only the headers and fasthttp has no equivalent, so an implementation
// could pass every other test with -server.read-timeout wired to nothing.
// A slow body holds a connection open, and a few thousand make that an outage.
func TestReadTimeoutBoundsTheBody(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// The header timeout has to sit at or below the read timeout, which
		// startup enforces. It's set well below it here so that the two can be
		// told apart: the headers below arrive at once and satisfy it,
		// leaving the read timeout as the only thing that can end the request.
		e := newEnv(t, tgt, []string{allowedDoc},
			"-server.read-header-timeout", "200ms",
			"-server.read-timeout", "1s")

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// Complete headers, so the header timeout is satisfied and out of the way,
		// and then a body that declares far more than it sends.
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n", len(docAllowed)+400); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		go func() {
			for range 200 {
				if _, err := conn.Write([]byte("x")); err != nil {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()

		// Whatever it answers, it stops waiting. What must not happen is the
		// body being taken for as long as the client cares to send it.
		if _, err := conn.Read(make([]byte, 64)); err != nil &&
			!strings.Contains(err.Error(), "EOF") &&
			!strings.Contains(err.Error(), "reset") {
			t.Fatalf("expected it given up on; received %v", err)
		}
		took := time.Since(start)
		if took > 5*time.Second {
			t.Errorf("expected the body bounded by the flag; it ran for %s", took)
		}
		// And bounded by that flag rather than by the header one, which the
		// completed headers already satisfied.
		if took < 400*time.Millisecond {
			t.Errorf("expected it held for -server.read-timeout;"+
				" it ended after %s, which is the header timeout", took)
		}
	})
}

// TestIdleTimeoutClosesAnIdleConnection covers a keep-alive connection nobody is using.
// A proxy that never closes one and a proxy that closes every connection at once
// both passed before this: the flag had no test anywhere,
// and it's operational contract against the idle timeout of whatever load
// balancer sits in front.
func TestIdleTimeoutClosesAnIdleConnection(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-server.idle-timeout", "500ms")

		conn, err := net.DialTimeout("tcp", e.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}

		// One complete exchange, so the connection is idle rather than new.
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}
		if status, _ := readAnswer(t, conn); !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Fatalf("expected the first request served; received %q", status)
		}

		// Now hold it and read nothing. The proxy closes it, and does so
		// because of the flag rather than at once: an implementation that closed
		// every connection after one request would serve no keep-alive at all.
		start := time.Now()
		if _, err := conn.Read(make([]byte, 64)); err == nil {
			t.Fatal("expected the idle connection closed; it sent something")
		}
		took := time.Since(start)
		if took > 5*time.Second {
			t.Errorf("expected it closed within the timeout; it took %s", took)
		}
		if took < 200*time.Millisecond {
			t.Errorf("expected it held for the timeout; it closed after %s", took)
		}
	})
}

// TestWriteTimeoutCutsOffAClientThatStopsReading covers the flag from the
// other side: a client that asks for a large answer and then stops reading it.
//
// Without a bound the proxy holds the connection, the buffers and the upstream
// answer for as long as that client likes, which is the cheapest way to tie up
// a proxy from outside.
func TestWriteTimeoutCutsOffAClientThatStopsReading(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// An answer far larger than any socket buffer, so writing it can't
		// complete while nobody reads.
		big := strings.Repeat("a", 8<<20)
		url := rawUpstream(t, func(conn net.Conn) {
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\n"+
				"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
				len(big), big)
		})
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "500ms", "-server.write-timeout", "1s")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}

		// One read to get the answer started, and then nothing: reading on,
		// however slowly, is draining the socket and is not what a client that
		// walked away does. The proxy's write blocks once the buffers fill,
		// and -server.write-timeout is what ends it.
		buf := make([]byte, 4096)
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("expected the answer to start; received %v", err)
		}
		time.Sleep(3 * time.Second)

		// Whatever was buffered arrives, and then the connection is gone.
		var read int
		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			n, err := conn.Read(buf)
			read += n
			if err != nil {
				break
			}
			if read >= len(big) {
				break
			}
		}
		if read >= len(big) {
			t.Errorf("expected the answer cut off;"+
				" the whole %d bytes arrived to a client that stopped reading", read)
		}
	})
}

// TestWriteTimeoutFollowsTheUpstreamTimeout covers the derived default.
//
// Unset, -server.write-timeout is -upstream.timeout plus a margin, so that the
// proxy never cuts off an answer the upstream is still allowed to be sending.
// The relation is checked at startup; that it's also *applied* is what this
// pins, by raising -upstream.timeout past what the default write timeout would
// have been and seeing a slow answer still arrive whole.
func TestWriteTimeoutFollowsTheUpstreamTimeout(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// An answer that takes about two seconds to arrive, which is past the
		// default write timeout of a 1s upstream timeout but inside that of a 12s one.
		url := rawUpstream(t, func(conn net.Conn) {
			body := `{"data":{"user":{"name":"Ada"}}}`
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\n"+
				"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n",
				len(body))
			time.Sleep(2 * time.Second)
			_, _ = conn.Write([]byte(body))
		})
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// -server.write-timeout unset, so it follows -upstream.timeout.
		s := serve(t, tgt, "-upstream.url", url, "-allowlist", dir,
			"-upstream.timeout", "12s")

		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}

		status, body := readAnswer(t, conn)
		if !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Fatalf("expected the slow answer served; received %q: %s",
				status, s.log)
		}
		if body != `{"data":{"user":{"name":"Ada"}}}` {
			t.Errorf("expected the answer whole; received %q", body)
		}
	})
}
