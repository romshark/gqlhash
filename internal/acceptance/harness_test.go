package acceptance

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// binaries names the servers under test, one -proxy.bin each. Without any,
// the commands of this repository are built and used,
// which is what a plain go test runs.
var binaries paths

func init() {
	flag.Var(&binaries, "proxy.bin",
		"path to a proxy binary to run the acceptance tests against, repeatable")
}

type paths []string

func (p *paths) String() string     { return strings.Join(*p, ",") }
func (p *paths) Set(v string) error { *p = append(*p, v); return nil }

// target is one server under test.
type target struct{ name, path string }

// targets is what every test in this package runs against.
var targets []target

// commands are the ones built when no -proxy.bin is given.
var commands = []string{"gqlhash-proxy", "gqlhash-proxy-fhttp"}

func TestMain(m *testing.M) {
	flag.Parse()

	dir, err := os.MkdirTemp("", "gqlhash-acceptance")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if targets, err = resolve(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// resolve is the list of servers to test: what -proxy.bin named,
// or the commands of this repository built into dir.
//
// With GOCOVERDIR set they're built -cover, since a binary reached through an
// exec reports nothing to go test -cover on its own:
//
//	GOCOVERDIR=$PWD/covdata go test ./internal/acceptance
//	go tool covdata percent -i=$PWD/covdata
func resolve(dir string) ([]target, error) {
	if len(binaries) > 0 {
		out := make([]target, 0, len(binaries))
		for _, p := range binaries {
			abs, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			out = append(out, target{name: filepath.Base(abs), path: abs})
		}
		return out, nil
	}

	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(commands))
	for _, name := range commands {
		path := filepath.Join(dir, name)
		args := []string{"build", "-o", path}
		if os.Getenv("GOCOVERDIR") != "" {
			args = append(args, "-cover",
				"-coverpkg=github.com/romshark/gqlhash/v2/...")
		}
		build := exec.Command("go", append(args, "./cmd/"+name)...)
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("building %s: %w: %s", name, err, output)
		}
		out = append(out, target{name: name, path: path})
	}
	return out, nil
}

func moduleRoot() (string, error) {
	output, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("locating the module: %w", err)
	}
	gomod := strings.TrimSpace(string(output))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("no module to build the commands from")
	}
	return filepath.Dir(gomod), nil
}

// each runs test against every target. A test written this way states a rule
// every implementation has to hold to, which is the point of this package.
func each(t *testing.T, test func(t *testing.T, tgt target)) {
	t.Helper()
	for _, tgt := range targets {
		t.Run(tgt.name, func(t *testing.T) { test(t, tgt) })
	}
}

// server is a running proxy: the addresses it was told to listen on,
// the process serving them and what it wrote to stderr.
type server struct {
	address string // The data plane, where the documents go.
	control string // /metrics and /reload.
	log     *logs  // Its stderr, for a failure to say what the run was doing.

	cmd     *exec.Cmd
	exited  chan error
	stopped bool // Set once its exit code has been read, so it's read once.
	code    int
}

// running reports whether the process is still up.
func (s *server) running() bool {
	if s.stopped {
		return false
	}
	select {
	case err := <-s.exited:
		s.stopped, s.code = true, exitCode(err)
		return false
	default:
		return true
	}
}

// signal sends sig and returns without waiting,
// which is what a test covering the shutdown itself needs.
func (s *server) signal(sig os.Signal) {
	if !s.stopped {
		_ = s.cmd.Process.Signal(sig)
	}
}

// interrupt is [server.signal] with SIGINT, the signal a terminal sends.
func (s *server) interrupt() { s.signal(os.Interrupt) }

// wait waits for the process to end and answers its exit code.
func (s *server) wait(t *testing.T) int {
	t.Helper()
	if s.stopped {
		return s.code
	}
	select {
	case err := <-s.exited:
		s.stopped, s.code = true, exitCode(err)
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
		s.stopped, s.code = true, -1
		t.Fatalf("the proxy didn't stop: %s", s.log)
	}
	return s.code
}

// stop interrupts the process and waits for it.
func (s *server) stop(t *testing.T) int {
	t.Helper()
	s.interrupt()
	return s.wait(t)
}

// exitCode is what a process ended with. A signal or a failure to start it
// answers -1, which is no exit code a proxy returns.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// serve starts tgt on ports of its own and returns it once it accepts
// connections on both.
//
// The ports are taken by binding them and letting them go, so one is only known
// to be free the moment it's assigned. A server that fails to bind exits,
// which is why this tries again rather than failing the test on the first attempt.
func serve(t *testing.T, tgt target, args ...string) *server {
	t.Helper()
	const attempts = 3
	for attempt := range attempts {
		s, out, err := start(t, tgt, args)
		if err == nil {
			return s
		}
		if attempt == attempts-1 {
			t.Fatalf("starting %s: %v: %s", tgt.name, err, out)
		}
	}
	return nil
}

// start runs tgt once and waits for it to serve.
func start(t *testing.T, tgt target, args []string) (*server, *logs, error) {
	t.Helper()
	address, control := freePort(t), freePort(t)
	out := new(logs)
	cmd := exec.Command(tgt.path, append([]string{
		"-server.listen", address,
		"-control.listen", control,
	}, args...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, out, err
	}

	s := &server{
		address: address, control: control, log: out,
		cmd: cmd, exited: make(chan error, 1),
	}
	go func() { s.exited <- cmd.Wait() }()

	// A clean stop is part of what's under test: the commands shut down on
	// SIGINT, so a crash on the way out is a failure of the run. A test that
	// stops the server itself has read the code already, and this leaves it be.
	t.Cleanup(func() {
		if !s.running() {
			return
		}
		if code := s.stop(t); code != 0 {
			t.Errorf("expected a clean stop; received %d: %s", code, out)
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !s.running() {
			// A port taken in between, or a flag it wouldn't take.
			// Which one it was is in out, which reaches the test where
			// this runs out of attempts.
			return nil, out, fmt.Errorf("it exited with %d before it served", s.code)
		}
		if accepting(address) && accepting(control) {
			return s, out, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil, out, fmt.Errorf("it didn't serve within the deadline")
}

// freePort is an address free at the moment it's returned,
// taken by binding it and letting it go.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// accepting reports whether something takes a connection on address.
func accepting(address string) bool {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// logs collects a process's stderr,
// which the goroutine copying it writes to while the test reads it.
type logs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func send(t *testing.T, req *http.Request) (code int, body string) {
	t.Helper()
	code, body, _ = sendFor(t, req)
	return code, body
}

// sendFor is [send] where the headers of the answer are part of what's under
// test, which a refusal that names how to get past it makes them.
func sendFor(t *testing.T, req *http.Request) (
	code int, body string, header http.Header,
) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b), res.Header
}

// post sends a GraphQL request to the data plane of s.
func post(t *testing.T, s *server, body string) (code int, answer string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		"http://"+s.address+"/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return send(t, req)
}

// get sends a GraphQL request carrying the document in the query string,
// which is passed raw so an encoding under test survives the way out.
func get(t *testing.T, s *server, rawQuery string) (code int, answer string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		"http://"+s.address+"/graphql?"+rawQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	return send(t, req)
}

// raw sends bytes over a socket and returns the status line,
// so the framing under test isn't normalized by an HTTP client on the way out.
func raw(t *testing.T, address, request string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\r\n")
	return line
}

const (
	upstreamAnswer = `{"data":{"user":{"name":"Ada"}}}`

	// allowedDoc is the document on the allowlist every test starts from,
	// and allowedText the same document as a request carries it.
	// rejectedText is one that isn't on it.
	allowedDoc   = "query GetUser {\n  user(id: 1) {\n    name\n  }\n}"
	allowedText  = `query GetUser{user(id:1){name}}`
	rejectedText = `query GetUser{user(id:1){secret}}`

	docAllowed  = `{"query":"` + allowedText + `"}`
	docRejected = `{"query":"` + rejectedText + `"}`

	// controlTokenEnv is the only way to give the control server a token:
	// it has no flag, so one can't end up in a process listing.
	controlTokenEnv = "GQLHASH_PROXY_CONTROL_TOKEN"
)

// spy is an upstream that records what reached it, headers included.
type spy struct {
	mu       sync.Mutex
	requests int
	body     string
	path     string
	header   http.Header
}

func (s *spy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests++
	s.body, s.path = string(body), r.URL.Path
	s.header = r.Header.Clone()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

func (s *spy) snapshot() (requests int, body, path string, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.body, s.path, s.header
}

func (s *spy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// env is one test's world: an API, the allowlist in front of it and a proxy
// serving that allowlist.
type env struct {
	*server
	api *spy
	dir string // The allowlist, for a test that changes what's on it.
}

// newEnv starts an API and a proxy allowing documents, named a.graphql onwards.
func newEnv(t *testing.T, tgt target, documents []string, args ...string) *env {
	t.Helper()
	dir := t.TempDir()
	for i, d := range documents {
		writeDoc(t, dir, string(rune('a'+i))+".graphql", d)
	}
	api := new(spy)
	mux := http.NewServeMux()
	mux.Handle("/graphql", api)
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	s := serve(t, tgt, append([]string{
		"-upstream.url", upstream.URL + "/graphql", "-allowlist", dir,
	}, args...)...)
	return &env{server: s, api: api, dir: dir}
}

// writeDoc writes a document into an allowlist directory.
func writeDoc(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// jsonRequest is document as a GraphQL request body. It's marshaled rather than quoted:
// a control character is written \x00 in Go and JSON has no reading for that.
func jsonRequest(document string) (string, error) {
	body, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: document})
	return string(body), err
}

// control sends a request to the control server,
// with a bearer token where one is given.
func control(
	t *testing.T, s *server, method, path, token string,
) (code int, body string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+s.control+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(t, req)
}

// controlFor is [control] where the headers of the answer matter.
func controlFor(
	t *testing.T, s *server, method, path, token string,
) (code int, body string, header http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+s.control+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return sendFor(t, req)
}
