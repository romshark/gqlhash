package acceptance

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// blockingUpstream is an API that holds a request until it's released,
// so a test can be sure one is in flight when the shutdown starts.
func blockingUpstream(t *testing.T, answer bool) (url string, entered, release chan struct{}) {
	t.Helper()
	entered, release = make(chan struct{}), make(chan struct{})
	var once bool
	upstream := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if !once {
				once = true
				close(entered)
			}
			<-release
			if answer {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, upstreamAnswer)
			}
		}))
	// The handler is released before the server is closed: Close waits for the
	// requests in flight, so the other order deadlocks.
	t.Cleanup(upstream.Close)
	return upstream.URL + "/graphql", entered, release
}

// postAsync sends a request from a goroutine, where calling t.Fatal isn't
// allowed, and answers 0 where the request itself failed.
func postAsync(address, body string) <-chan int {
	answered := make(chan int, 1)
	go func() {
		client := &http.Client{Timeout: 20 * time.Second}
		res, err := client.Post("http://"+address+"/graphql", "application/json",
			strings.NewReader(body))
		if err != nil {
			answered <- 0
			return
		}
		defer func() { _ = res.Body.Close() }()
		_, _ = io.Copy(io.Discard, res.Body)
		// The socket goes with the answer, so a test that stops the server next
		// isn't racing its own connection pool. An idle connection wouldn't
		// delay the drain — TestIdleConnectionDoesNotDelayTheDrain pins that —
		// but a pooled one this process may still write to is a request in
		// flight as far as the proxy knows.
		defer client.CloseIdleConnections()
		answered <- res.StatusCode
	}()
	return answered
}

// TestGracefulShutdown asserts that a request in flight when the shutdown
// starts is answered instead of being cut off. Which server draws that out is
// the implementation's own code, so it's asserted of each of them.
func TestGracefulShutdown(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, true)
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "10s")

		answered := postAsync(s.address, docAllowed)
		<-entered // The request reached the API.
		s.interrupt()

		// The proxy waits for the request instead of dropping it.
		time.Sleep(200 * time.Millisecond)
		if !s.running() {
			t.Fatalf("the proxy stopped before the request was answered: %s", s.log)
		}

		close(release)
		if code := <-answered; code != http.StatusOK {
			t.Errorf("expected the request to be answered with 200; received %d", code)
		}
		if code := s.wait(t); code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, s.log)
		}
	})
}

// TestDrainStopsAccepting covers the moment a shutdown begins: both listeners
// stop taking connections, so a client arriving during the drain is turned away
// rather than served by a process on its way out. A drain that only waits,
// with its accept loop still running, keeps taking work while it tries to finish.
func TestDrainStopsAccepting(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, true)
		letGo := sync.OnceFunc(func() { close(release) })
		defer letGo()
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "10s")

		// A request in flight, so the drain has something to wait for.
		answered := postAsync(s.address, docAllowed)
		<-entered
		s.interrupt()

		// Both addresses stop taking connections, and promptly.
		for _, address := range []string{s.address, s.control} {
			deadline := time.Now().Add(3 * time.Second)
			for accepting(address) {
				if time.Now().After(deadline) {
					t.Errorf("expected %s to stop accepting during the drain", address)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}

		// What was in flight is still finished.
		letGo()
		if code := <-answered; code != http.StatusOK {
			t.Errorf("expected the request in flight answered; received %d", code)
		}
		if code := s.wait(t); code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, s.log)
		}
	})
}

// TestShutdownTimeout asserts that the wait is bounded and reported:
// past -server.shutdown-timeout whatever is still running is abandoned,
// and the command says so by the code it exits with.
func TestShutdownTimeout(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, false)
		defer close(release)

		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "100ms")

		answered := postAsync(s.address, docAllowed)
		<-entered
		start := time.Now()
		s.interrupt()

		if code := s.wait(t); code != 1 {
			t.Errorf("expected code 1 for an exceeded timeout; received %d: %s",
				code, s.log)
		}
		// The configured timeout is what bounded the wait. 2s would have let
		// any hardwired sub-2s drain pass, which is most of them; 100ms was asked for,
		// so the bound has to be near it to mean anything.
		if took := time.Since(start); took > 700*time.Millisecond {
			t.Errorf("expected the configured 100ms to bound the drain; took %s",
				took)
		}
		// The abandoned client is answered by nothing.
		// A status synthesized here would be an answer the API never gave.
		if code := <-answered; code == http.StatusOK {
			t.Error("expected the abandoned request not to be answered with 200")
		}
	})
}

// TestShutdownOnSIGTERM covers the other signal a supervisor stops a process
// with. A container runtime sends SIGTERM, so a proxy that only handled SIGINT
// would be killed rather than drained.
func TestShutdownOnSIGTERM(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, true)
		// Released once, whether the test reaches the release below or leaves
		// before it: closing the upstream waits for the request in flight.
		letGo := sync.OnceFunc(func() { close(release) })
		defer letGo()
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "10s")

		answered := postAsync(s.address, docAllowed)
		<-entered
		s.signal(syscall.SIGTERM)

		// The request in flight is answered, and the process ends cleanly.
		letGo()
		if code := <-answered; code != http.StatusOK {
			t.Errorf("expected the request answered; received %d", code)
		}
		if code := s.wait(t); code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, s.log)
		}
	})
}

// TestSecondSignalStopsAtOnce covers a shutdown that's taking too long for
// whoever asked for it: the first signal starts the drain and restores the
// default handling, so a second one ends the process rather than waiting out
// -server.shutdown-timeout.
func TestSecondSignalStopsAtOnce(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, true)
		defer close(release)
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		// A timeout long enough that waiting it out would fail this test.
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "60s")

		_ = postAsync(s.address, docAllowed)
		<-entered
		s.interrupt()
		// The drain restores the default handling of the signal,
		// which the second one below needs: sent in the same instant it would
		// still reach the handler that started the drain and do nothing.
		time.Sleep(250 * time.Millisecond)
		if !s.running() {
			t.Fatal("the proxy stopped before the drain began")
		}
		s.interrupt()

		start := time.Now()
		code := s.wait(t)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("expected the second signal to end it; it took %s", took)
		}
		// Killed by the default handling, which is no clean stop.
		if code == 0 {
			t.Errorf("expected it not to report a clean stop; received %d", code)
		}
	})
}

// TestShutdownTimeoutIsHonored covers the configured value being the one that
// bounds the drain, rather than some fixed wait that happens to be shorter.
//
// Two runs of the same request, one with a short timeout and one with a long one:
// the short one gives up and the long one waits. An implementation with a
// hardwired drain passes either test alone and neither of them together.
func TestShutdownTimeoutIsHonored(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		// The upstream is held for about a second, so a 200ms drain has to
		// abandon the request and a 5s drain has to outlast it.
		for _, tc := range []struct {
			name    string
			timeout string
			code    int
			atLeast time.Duration
			atMost  time.Duration
		}{
			{"a drain shorter than the request", "200ms", 1, 0, 2 * time.Second},
			{
				"a drain longer than it", "5s", 0, 500 * time.Millisecond,
				4 * time.Second,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				entered := make(chan struct{})
				var once sync.Once
				upstream := httptest.NewServer(http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) {
						once.Do(func() { close(entered) })
						time.Sleep(time.Second)
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, upstreamAnswer)
					}))
				defer upstream.Close()

				dir := t.TempDir()
				writeDoc(t, dir, "a.graphql", allowedDoc)
				s := serve(t, tgt, "-upstream.url", upstream.URL+"/graphql",
					"-allowlist", dir, "-server.shutdown-timeout", tc.timeout)

				answered := postAsync(s.address, docAllowed)
				<-entered
				start := time.Now()
				s.interrupt()
				code := s.wait(t)
				took := time.Since(start)

				if code != tc.code {
					t.Errorf("expected exit %d; received %d: %s",
						tc.code, code, s.log)
				}
				if took < tc.atLeast {
					t.Errorf("expected it to wait at least %s; took %s",
						tc.atLeast, took)
				}
				if took > tc.atMost {
					t.Errorf("expected it bounded by %s; took %s",
						tc.atMost, took)
				}
				// A drain that outlasted the request answered it.
				if got := <-answered; tc.code == 0 && got != http.StatusOK {
					t.Errorf("expected the request in flight answered;"+
						" received %d", got)
				}
			})
		}
	})
}

// TestShutdownTimeoutZeroAbandonsAtOnce covers the value every sibling duration
// flag reads as "no bound".
//
// Here it means the opposite: nothing is waited for, so a request in flight is
// abandoned the moment the signal arrives and the command exits 1.
// Worth pinning because every other duration's help invites the other reading,
// and taking 0 for "wait forever" hangs every deploy instead of dropping a request.
func TestShutdownTimeoutZeroAbandonsAtOnce(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		upstream, entered, release := blockingUpstream(t, false)
		defer close(release)

		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", upstream, "-allowlist", dir,
			"-server.shutdown-timeout", "0")

		answered := postAsync(s.address, docAllowed)
		<-entered
		start := time.Now()
		s.interrupt()

		if code := s.wait(t); code != 1 {
			t.Errorf("expected exit 1 for a request abandoned; received %d: %s",
				code, s.log)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("expected it to give up at once; took %s", took)
		}
		if code := <-answered; code == http.StatusOK {
			t.Error("expected the abandoned request not to be answered with 200")
		}
	})
}

// TestIdleConnectionDoesNotDelayTheDrain covers a keep-alive connection that is
// open and carrying nothing when the signal arrives.
//
// A shutdown waits for the requests in flight, and an idle connection is not
// one. An implementation that waited for every open connection would sit out
// the whole -server.shutdown-timeout on every deploy and then exit 1,
// which reads as a proxy that can't be stopped cleanly.
func TestIdleConnectionDoesNotDelayTheDrain(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		api := newAPI(t)
		dir := t.TempDir()
		writeDoc(t, dir, "a.graphql", allowedDoc)
		s := serve(t, tgt, "-upstream.url", api.URL+"/graphql", "-allowlist", dir,
			"-server.shutdown-timeout", "10s")

		// A connection that has served a request and is now idle,
		// held open by this test for the whole shutdown.
		conn, err := net.DialTimeout("tcp", s.address, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn,
			"POST /graphql HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n%s", len(docAllowed), docAllowed,
		); err != nil {
			t.Fatal(err)
		}
		if status, _ := readAnswer(t, conn); !strings.HasPrefix(status, "HTTP/1.1 200") {
			t.Fatalf("expected the request served; received %q", status)
		}

		start := time.Now()
		s.interrupt()
		if code := s.wait(t); code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, s.log)
		}
		// Well inside the 10s drain it was given: an implementation waiting for
		// the connection would have taken all of it.
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("expected the idle connection not to delay the drain;"+
				" the stop took %s", took)
		}
	})
}
