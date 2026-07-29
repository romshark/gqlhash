package acceptance

import (
	"io"
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
		// The socket goes with the answer: a connection left idle here is one
		// a shutdown waits for, which would make a stop look slow.
		defer client.CloseIdleConnections()
		answered <- res.StatusCode
	}()
	return answered
}

// TestGracefulShutdown asserts that a request in flight when the shutdown
// starts is answered instead of being cut off. Which server draws that out is
// the underlay's own code, so it's asserted of each of them.
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

		_ = postAsync(s.address, docAllowed)
		<-entered
		s.interrupt()

		if code := s.wait(t); code != 1 {
			t.Errorf("expected code 1 for an exceeded timeout; received %d: %s",
				code, s.log)
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
		// The drain restores the default handling of the signal, which the
		// second one below needs: sent in the same instant it would still reach
		// the handler that started the drain and do nothing.
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
