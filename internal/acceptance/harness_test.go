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

// withFHTTP says whether the experimental fasthttp build is one of the servers
// under test. It only applies to the commands this repository builds:
// -proxy.bin names the targets outright and replaces them.
var withFHTTP = true

func init() {
	flag.Var(&binaries, "proxy.bin",
		"path to a proxy binary to run the acceptance tests against, repeatable")
	flag.BoolVar(&withFHTTP, "proxy.fhttp", true,
		"also run every test against gqlhash-proxy-fhttp, the experimental\n"+
			"fasthttp build. -proxy.fhttp=false leaves it out, which halves the\n"+
			"suite: it's not a build to deploy, and every rule it keeps is a rule\n"+
			"gqlhash-proxy keeps too. Ignored where -proxy.bin names the targets.")
}

type paths []string

func (p *paths) String() string     { return strings.Join(*p, ",") }
func (p *paths) Set(v string) error { *p = append(*p, v); return nil }

// target is one server under test.
type target struct{ name, path string }

// targets is what every test in this package runs against.
var targets []target

// commands are the ones built when no -proxy.bin is given. The second is the
// experimental fasthttp build, which -proxy.fhttp=false leaves out.
var commands = []string{"gqlhash-proxy", "gqlhash-proxy-fhttp"}

// experimental is the command -proxy.fhttp governs.
const experimental = "gqlhash-proxy-fhttp"

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
	stopShared()
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
		if name == experimental && !withFHTTP {
			continue
		}
		path := filepath.Join(dir, name)
		args := []string{"build", "-o", path}
		if os.Getenv("GOCOVERDIR") != "" {
			// atomic rather than the default set: the servers are hit concurrently,
			// and the mode has to be the one the in-process run uses,
			// or go tool covdata merge refuses the two halves. See ci.yml.
			args = append(args, "-cover", "-covermode=atomic",
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
	keep    bool // A shared server, which outlives the test that started it.
}

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
//
// The client's idle connections go first, so that a stop isn't racing this
// process's own connection pool: a pooled connection is one the client may
// still write a request onto, and a request that arrives during the drain is
// one the server is entitled to wait for. An idle connection by itself doesn't
// delay anything — see TestIdleConnectionDoesNotDelayTheDrain.
func (s *server) stop(t *testing.T) int {
	t.Helper()
	http.DefaultClient.CloseIdleConnections()
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

// run executes tgt to completion and answers what it exited with and wrote.
// Nothing waits for a listener and nothing is retried:
// a start that fails is the subject here rather than an accident.
func run(t *testing.T, tgt target, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runWithEnv(t, tgt, nil, args...)
}

// runWithEnv is [run] with variables added to the environment of the command.
func runWithEnv(
	t *testing.T, tgt target, env []string, args ...string,
) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	cmd := exec.Command(tgt.path, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", tgt.path, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err), out.String(), errOut.String()
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s didn't exit: %s", tgt.name, errOut.String())
	}
	return 0, "", ""
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
		if s.keep || !s.running() {
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

// recorded is one request as the API received it,
// which is what a test reads to see what the proxy forwarded.
type recorded struct {
	method   string
	path     string
	rawQuery string
	host     string
	body     string
	header   http.Header
}

// spy is an upstream that records every request that reached it, whole.
// Every one of them, rather than the last: a proxy that answers a request with
// another's body is the worst defect this has, and only the whole list shows it.
type spy struct {
	mu       sync.Mutex
	requests []recorded
}

func (s *spy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, recorded{
		method:   r.Method,
		path:     r.URL.Path,
		rawQuery: r.URL.RawQuery,
		host:     r.Host,
		body:     string(body),
		header:   r.Header.Clone(),
	})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, upstreamAnswer)
}

// reset forgets what it recorded, for the next test on a shared server.
func (s *spy) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *spy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *spy) last(t *testing.T) recorded {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("the upstream saw no request")
	}
	return s.requests[len(s.requests)-1]
}

// all is every request the API received, in the order they arrived.
func (s *spy) all() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.requests...)
}

// env is one test's world: an API, the allowlist in front of it and a proxy
// serving that allowlist.
type env struct {
	*server
	api *spy
	dir string // The allowlist, for a test that changes what's on it.

	// upstream is the API behind the proxy. A test's own is closed with the
	// test; a shared one when the run ends.
	upstream *httptest.Server
}

// newEnv starts an API and a proxy of a test's own, allowing documents, named
// a.graphql onwards. A test that needs neither a flag nor the counters at zero
// takes [shared] instead.
func newEnv(t *testing.T, tgt target, documents []string, args ...string) *env {
	t.Helper()
	return newEnvFor(t, tgt, false, documents, args...)
}

func newEnvFor(
	t *testing.T, tgt target, keep bool, documents []string, args ...string,
) *env {
	t.Helper()
	dir := t.TempDir()
	if keep {
		// A shared server outlives the test that started it,
		// and so does the directory it reads. stopShared takes both down.
		var err error
		if dir, err = os.MkdirTemp("", "gqlhash-allowlist"); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range documents {
		writeDoc(t, dir, string(rune('a'+i))+".graphql", d)
	}
	api := new(spy)
	mux := http.NewServeMux()
	mux.Handle("/graphql", api)
	upstream := httptest.NewServer(mux)
	if !keep {
		t.Cleanup(upstream.Close)
	}

	s := serve(t, tgt, append([]string{
		"-upstream.url", upstream.URL + "/graphql", "-allowlist", dir,
	}, args...)...)
	s.keep = keep
	return &env{server: s, api: api, dir: dir, upstream: upstream}
}

// shared is the server a test runs against where it needs no flags of its own:
// one per target, started when the first test asks for it and stopped when the
// run ends. The tests here are serial, so the one server is each test's in turn.
//
// A test needing its own documents publishes them with [env.allow]. One needing a flag,
// an upstream of its own, or the counters at zero calls [newEnv].
func shared(t *testing.T, tgt target) *env {
	t.Helper()
	if e, ok := sharedEnvs[tgt.name]; ok {
		return e
	}
	e := newEnvFor(t, tgt, true, nil)
	sharedEnvs[tgt.name] = e
	return e
}

// sharedEnvs is one env per target. The tests are serial, so no lock guards it.
var sharedEnvs = map[string]*env{}

// stopShared stops what [shared] started. TestMain calls it: a cleanup would
// belong to whichever test asked for it first.
func stopShared() {
	http.DefaultClient.CloseIdleConnections()
	for _, e := range sharedEnvs {
		e.interrupt()
		<-e.exited
		e.upstream.Close()
		_ = os.RemoveAll(e.dir)
	}
}

// allow makes documents the allowlist of e and publishes them through the
// control server, which is how a test gets the documents it needs on a server it shares.
// It answers with the state a test starts from: these documents allowed,
// and nothing forwarded yet.
func (e *env) allow(t *testing.T, documents ...string) {
	t.Helper()
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		// RemoveAll: a test before this one may have left directories behind.
		if err := os.RemoveAll(filepath.Join(e.dir, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range documents {
		writeDoc(t, e.dir, string(rune('a'+i))+".graphql", d)
	}

	code, body := control(t, e.server, http.MethodPost, "/reload", "")
	if code != http.StatusOK {
		t.Fatalf("publishing the allowlist: %d: %s", code, body)
	}
	if answer := reloadAnswer(t, body); answer.Documents.Total != len(documents) {
		t.Fatalf("expected %d documents published; received %s",
			len(documents), body)
	}
	e.api.reset()
}

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

// newAPI starts an upstream that answers like the one every test has,
// for a test that starts a server of its own rather than taking [shared].
func newAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/graphql", new(spy))
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	return upstream
}

// writeDocAt writes a document at a path inside an allowlist directory,
// creating the directories it names.
func writeDocAt(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
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
