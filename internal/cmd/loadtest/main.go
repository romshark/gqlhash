// Command loadtest measures a gqlhash proxy end to end: it serves a GraphQL API,
// puts the proxy in front of it, drives both the forwarded and the rejected path
// with wrk, and reports what the proxy spent holding the rate.
//
//	go run ./internal/cmd/loadtest [flags]
//
// wrk is required, and cheap enough per request not to become the answer itself.
// It's closed-loop, so it stops sending while the proxy is stalled and never
// records the stall: read its latency distribution as the shape of a healthy run
// rather than as a service level, which needs a constant arrival rate instead.
//
// Running the generator on the machine it measures understates the proxy however
// cheap it is; a number worth publishing needs a second machine.
// Where there isn't one, every run ends with what the proxy,
// the generator and the upstream each held against the machine underneath.
// Read a run as the proxy's number only while that balance leaves the machine something:
// past it they take cores from each other,
// and -threads too low understates the proxy by as much as the crowding does.
//
// The proxy is started with its upstream pool sized to -connections, which isn't
// the default. See TUNING_GQLHASH_PROXY.md.
//
// wrk counts the 403 of the rejected run as a failure. It's the expected answer.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fixtures are what a run sends and what the upstream answers, kept as files so
// they can be read, diffed and curled without Go string escaping.
//
//go:embed fixtures
var fixtures embed.FS

// fixture returns a file from [fixtures] without the trailing newline: the
// bodies go out as they are and the answer is measured by its length.
func fixture(name string) string {
	b, err := fixtures.ReadFile("fixtures/" + name)
	if err != nil {
		// Embedded at build time, so a miss is a mistake in this package.
		panic(err)
	}
	return strings.TrimRight(string(b), "\n")
}

var (
	allowedBody  = fixture("allowed.json")
	rejectedBody = fixture("rejected.json")
	// allowedDoc is the one document on the allowlist, formatted unlike the
	// request above on purpose: the hash ignores the difference.
	allowedDoc = fixture("get-user.graphql")
	// upstreamAnswer is short and fixed, so a run measures the proxy rather
	// than the API behind it. Its length is part of what the numbers mean.
	upstreamAnswer = fixture("upstream-answer.json")
)

// defaultWrkThreads is what wrk is asked for unless -threads says otherwise.
//
// A third of the machine, measured rather than picked: on 24 hardware threads
// the rejected path rises from ~316,000/s at two wrk threads to ~722,000/s at
// eight and goes no further, and the fasthttp build from ~1,261,000/s at eight
// to ~1,358,000/s at ten. Below that the generator is the measurement,
// above it the two only take cores from each other. Derived from the machine because the
// count that saturates one won't saturate the next.
//
// The forwarded path doesn't move with this at all — the upstream costs more
// than the generator there, and no thread count buys that back.
func defaultWrkThreads() int { return max(4, runtime.NumCPU()/3) }

// maxConnections is well past anything a run needs and keeps the derived pool
// sizes far from overflowing.
const maxConnections = 1 << 16

// threadsFor is what wrk is asked for. It refuses to run with fewer connections
// than threads, so the count follows the connections where those are fewer.
func threadsFor(threads, connections int) int { return min(threads, connections) }

// wrkDuration renders d as wrk's parser takes it: an integer and a unit.
// Go's syntax is the wider of the two — 500ms, 1.5s and 1m30s all parse and none is
// wrk's — so what wrk won't take is refused here and not by the child process.
func wrkDuration(d time.Duration) (string, error) {
	if d <= 0 {
		return "", fmt.Errorf("-duration must be positive, received %s", d)
	}
	if d%time.Second != 0 {
		return "", fmt.Errorf(
			"-duration must be a whole number of seconds, received %s", d)
	}
	return strconv.FormatInt(int64(d/time.Second), 10) + "s", nil
}

// checkConnections reports whether wrk will take this many.
func checkConnections(threads, connections int) error {
	if threads < 1 {
		return fmt.Errorf("-threads must be 1 or more, received %d", threads)
	}
	if connections < threads {
		return fmt.Errorf(
			"-connections must be at least -threads (%d), received %d",
			threads, connections)
	}
	if connections > maxConnections {
		return fmt.Errorf("-connections must be %d or fewer, received %d",
			maxConnections, connections)
	}
	return nil
}

func main() {
	duration := flag.Duration("duration", 10*time.Second,
		"How long each path is driven, as a whole number of seconds.\n"+
			"wrk takes no finer unit than that.")
	connections := flag.Int("connections", 200,
		"Connections held open, at least -threads.\n"+
			"Too few measure the connections rather than the proxy: a forward\n"+
			"takes about a millisecond, so what a run can reach is bounded by\n"+
			"how many are open. Too many measure the queue in front of it.")
	threads := flag.Int("threads", defaultWrkThreads(),
		"Threads wrk drives with, never more than -connections.\n"+
			"Too few and the generator is the measurement; too many and it\n"+
			"takes the cores the proxy needs. A run prints what each held.")
	command := flag.String("command", "gqlhash-proxy",
		"Which binary under cmd/ to measure. gqlhash-proxy-fhttp is the same\n"+
			"proxy on fasthttp, see GQLHASH_PROXY_FHTTP.md.")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q, every option is named\n\n",
			flag.Arg(0))
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*duration, *threads, *connections, *command); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(duration time.Duration, threads, connections int, command string) error {
	if err := checkConnections(threads, connections); err != nil {
		return err
	}
	window, err := wrkDuration(duration)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("wrk"); err != nil {
		return errors.New("wrk isn't installed, and it's what this drives load with")
	}

	work, err := os.MkdirTemp("", "gqlhash-loadtest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	if err := writeFixtures(work); err != nil {
		return err
	}

	root, err := repoRoot()
	if err != nil {
		return err
	}

	fmt.Println("building", command)
	binary := filepath.Join(work, "proxy")
	build := exec.Command("go", "build", "-o", binary, "./cmd/"+command)
	build.Dir, build.Stdout, build.Stderr = root, os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building ./cmd/%s: %w", command, err)
	}

	// Served from here rather than spawned: one less process to start,
	// and its listener is the port, so nothing races for one.
	upstream, err := startUpstream()
	if err != nil {
		return err
	}
	defer func() { _ = upstream.Close() }()
	fmt.Println("serving the upstream on", upstream.Addr().String())

	proxy, err := startProxy(binary, upstream.Addr().String(), work, connections)
	if err != nil {
		return err
	}
	defer proxy.stop()
	fmt.Printf("serving %s on %s, its control server on %s\n",
		command, proxy.address, proxy.control)

	target := "http://" + proxy.address + "/graphql"
	if err := checkReady(target); err != nil {
		return fmt.Errorf("%w\n%s", err, proxy.logs())
	}

	for _, c := range []struct {
		label, body string
		// want is the decision every request of this run has to reach.
		// wrk counts non-2xx answers and no more, so it can't tell this 403 from a
		// 500 or see the upstream failing behind an allowed request.
		want string
	}{
		{"allowed, forwarded upstream",
			filepath.Join(work, "allowed.json"), decisionAllowed},
		{"rejected, answered by the proxy",
			filepath.Join(work, "rejected.json"), decisionRejected},
	} {
		fmt.Printf("\n=== %s through wrk for %s, %d connections, %d wrk threads\n",
			c.label, window, connections, threadsFor(threads, connections))

		before, err := readDecisions(proxy.control)
		if err != nil {
			return err
		}
		m := startMeter(proxy.pid())
		// The harness serves the upstream in this process,
		// so its own CPU is what the API behind the proxy costs.
		harness0, harness0OK := processCPU(os.Getpid())
		generator, driveErr := drive(target, c.body, window, threads, connections, work)
		harness1, harness1OK := processCPU(os.Getpid())
		// Finished either way: an abandoned meter leaks its goroutine,
		// and what it collected before the failure is worth printing.
		spent := m.finish()
		if driveErr != nil {
			return driveErr
		}
		if err := proxy.died(); err != nil {
			return fmt.Errorf("the proxy stopped during the run: %w\n%s",
				err, proxy.logs())
		}
		// Divide the cores by the req/s above for the CPU one request costs,
		// the figure that ports across machines.
		fmt.Printf("proxy: %s\n", spent)

		// Everything above runs on the one machine, so the proxy is only being
		// measured while the three of them fit on it. Where they don't,
		// the number is what the machine had left rather than what the proxy can do.
		upstream := report{elapsed: spent.elapsed, cpuUnknown: true}
		if harness0OK && harness1OK {
			upstream.cpu = time.Duration((harness1 - harness0) * float64(time.Second))
			upstream.cpuUnknown = false
		}
		fmt.Printf("generator: %s\nupstream: %s\n%s\n",
			cpuLine(generator), cpuLine(upstream), balance(spent, generator, upstream))

		after, err := readDecisions(proxy.control)
		if err != nil {
			return err
		}
		made := after.since(before)
		fmt.Printf("decided: %s\n", made)
		if err := made.only(c.want); err != nil {
			return fmt.Errorf("%s: %w", c.label, err)
		}
	}

	if tail := proxy.logs(); tail != "" {
		fmt.Printf("\n=== proxy log, last lines\n%s", tail)
	}
	return nil
}

// The decisions the proxy counts, as its control server names them.
const (
	decisionAllowed   = "allowed"
	decisionRejected  = "rejected"
	decisionMalformed = "malformed"
	decisionUpstream  = "upstream errors"
)

// decisions is what the proxy has decided, read from its control server:
// the only account of a run that tells one answer from another,
// where wrk counts non-2xx responses and stops.
type decisions struct {
	Allowed   int64 `json:"allowed"`
	Rejected  int64 `json:"rejected"`
	Malformed int64 `json:"malformed"`
	Upstream  int64 `json:"upstream_errors"`
}

// counts pairs every decision with its total, in a fixed order so a message
// naming one reads the same way twice.
func (d decisions) counts() []struct {
	name  string
	total int64
} {
	return []struct {
		name  string
		total int64
	}{
		{decisionAllowed, d.Allowed},
		{decisionRejected, d.Rejected},
		{decisionMalformed, d.Malformed},
		{decisionUpstream, d.Upstream},
	}
}

// since is what was decided between an earlier reading and this one.
func (d decisions) since(before decisions) decisions {
	return decisions{
		Allowed:   d.Allowed - before.Allowed,
		Rejected:  d.Rejected - before.Rejected,
		Malformed: d.Malformed - before.Malformed,
		Upstream:  d.Upstream - before.Upstream,
	}
}

func (d decisions) String() string {
	parts := make([]string, 0, 4)
	for _, c := range d.counts() {
		parts = append(parts, fmt.Sprintf("%d %s", c.total, c.name))
	}
	return strings.Join(parts, ", ")
}

// only reports whether every request of a run reached the one decision it was meant to.
// A run that decided nothing failed too: the load never arrived.
func (d decisions) only(want string) error {
	for _, c := range d.counts() {
		switch {
		case c.name == want && c.total == 0:
			return fmt.Errorf("the proxy decided nothing as %s", want)
		case c.name != want && c.total != 0:
			return fmt.Errorf(
				"the proxy answered %d request(s) as %s, expected only %s",
				c.total, c.name, want)
		}
	}
	return nil
}

// readDecisions asks the control server what the proxy has decided so far.
func readDecisions(control string) (decisions, error) {
	var d decisions
	res, err := readyClient.Get("http://" + control + "/status")
	if err != nil {
		return d, fmt.Errorf("reading the decisions of the proxy: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return d, fmt.Errorf("the control server answered %d for /status",
			res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		return d, fmt.Errorf("reading the decisions of the proxy: %w", err)
	}
	return d, nil
}

// writeFixtures lays out the allowlist and the two request bodies.
func writeFixtures(work string) error {
	queries := filepath.Join(work, "queries")
	if err := os.MkdirAll(queries, 0o755); err != nil {
		return err
	}
	for path, content := range map[string]string{
		filepath.Join(queries, "get-user.graphql"): allowedDoc,
		filepath.Join(work, "allowed.json"):        allowedBody,
		filepath.Join(work, "rejected.json"):       rejectedBody,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func repoRoot() (string, error) {
	// Asked of the toolchain rather than walked for,
	// so it agrees with what go build would pick from the same directory.
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("finding the module: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("not inside a Go module")
	}
	return filepath.Dir(gomod), nil
}

// startUpstream serves the GraphQL API the proxy is put in front of,
// answering a fixed body as cheaply as it can so a run measures the proxy and not an API.
func startUpstream() (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	answer := []byte(upstreamAnswer)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(answer)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	return listener, nil
}

// proxyProcess is the proxy under test.
type proxyProcess struct {
	cmd *exec.Cmd
	// address is the data plane; control serves /metrics, /status and /reload.
	// Both are behind :0, so the log is the only place they're known.
	address, control string
	output           *syncBuffer

	// exited carries what the process ended with. One goroutine owns Wait,
	// so watching for an early death and stopping it later don't both call it.
	exited chan error
}

func (p *proxyProcess) pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *proxyProcess) stop() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
}

// died reports what the proxy ended with, nil where it's still running.
// A run measures nothing once the process is gone, and wrk reports the connection
// errors without saying why.
func (p *proxyProcess) died() error {
	select {
	case err := <-p.exited:
		// Put it back: stop and any later call want the same answer.
		p.exited <- err
		if err == nil {
			return errors.New("it exited cleanly, which it shouldn't while serving")
		}
		return err
	default:
		return nil
	}
}

// logs returns the last lines the proxy wrote.
func (p *proxyProcess) logs() string {
	lines := strings.Split(strings.TrimRight(p.output.String(), "\n"), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// startProxy runs the proxy on a port of its own and waits for it to report the address,
// the only way to learn the port behind :0 — so nothing guesses at a free one.
func startProxy(binary, upstream, work string, connections int) (*proxyProcess, error) {
	out := new(syncBuffer)
	cmd := exec.Command(binary,
		"-server.listen", "127.0.0.1:0",
		"-control.listen", "127.0.0.1:0",
		"-upstream.url", "http://"+upstream+"/graphql",
		"-allowlist", filepath.Join(work, "queries"),
		"-log.level", "info",
		"-upstream.max-idle-conns-per-host", strconv.Itoa(connections),
		"-upstream.max-idle-conns", strconv.Itoa(connections*4),
	)
	cmd.Stdout, cmd.Stderr = out, out
	// The proxy reads GQLHASH_PROXY_* from the environment, so a variable left
	// in the shell would quietly configure what's being measured,
	// and GQLHASH_PROXY_LOG_JSON=false would hide the address below.
	// Everything else is passed on, GOGC and GOMEMLIMIT among it.
	cmd.Env = withoutProxyEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proxyProcess{cmd: cmd, output: out, exited: make(chan error, 1)}
	go func() { p.exited <- cmd.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if address := loggedAddress(out.String(), "listening"); address != "" {
			p.address = address
			p.control = loggedAddress(out.String(), "serving /metrics and /reload")
			return p, nil
		}
		// A proxy that refused its flags or couldn't bind is reported now,
		// not after the whole deadline runs out.
		if err := p.died(); err != nil {
			return nil, fmt.Errorf("the proxy stopped before it served: %w\n%s",
				err, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.stop()
	return nil, fmt.Errorf("the proxy didn't report an address:\n%s", out.String())
}

// withoutProxyEnv drops the variables the proxy would configure itself from.
func withoutProxyEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, v := range env {
		if !strings.HasPrefix(v, "GQLHASH_PROXY_") {
			kept = append(kept, v)
		}
	}
	return kept
}

// loggedAddress finds the address the startup log reports for one event.
func loggedAddress(logs, message string) string {
	for line := range strings.SplitSeq(logs, "\n") {
		var event struct {
			Message string `json:"message"`
			Address string `json:"address"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Message == message {
			return event.Address
		}
	}
	return ""
}

// checkReady makes sure both paths answer before anything is measured,
// so a broken setup fails here rather than measuring the wrong one.
func checkReady(target string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, err := post(target, allowedBody)
		if err == nil && code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"the proxy doesn't forward the allowed document (%d, %v)", code, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	code, err := post(target, rejectedBody)
	if err != nil {
		return err
	}
	if code != http.StatusForbidden {
		return fmt.Errorf("the proxy doesn't reject the unknown document (got %d)", code)
	}
	return nil
}

// readyClient bounds one attempt, so a proxy that accepts and says nothing is
// caught by the deadline in [checkReady] rather than hanging it.
var readyClient = &http.Client{Timeout: 5 * time.Second}

func post(target, body string) (int, error) {
	res, err := readyClient.Post(target, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, nil
}

// drive runs wrk against target, lets it write to the terminal, and reports what
// the generator itself held. Every core wrk takes is one the proxy doesn't get,
// so a run that doesn't count them can't tell a saturated proxy from a crowded machine.
func drive(
	target, body, window string, threads, connections int, work string,
) (report, error) {
	// wrk needs the body in a Lua script.
	content, err := os.ReadFile(body)
	if err != nil {
		return report{}, err
	}
	script := filepath.Join(work, "wrk.lua")
	lua := fmt.Sprintf("wrk.method = \"POST\"\n"+
		"wrk.headers[\"Content-Type\"] = \"application/json\"\n"+
		"wrk.body = [==[%s]==]\n", content)
	if err := os.WriteFile(script, []byte(lua), 0o600); err != nil {
		return report{}, err
	}
	wrk := exec.Command("wrk",
		"-t"+strconv.Itoa(threadsFor(threads, connections)),
		"-c"+strconv.Itoa(connections), "-d"+window,
		"-s", script, "--latency", target)
	wrk.Stdout, wrk.Stderr = os.Stdout, os.Stderr

	started := time.Now()
	runErr := wrk.Run()
	// Taken from wait4 rather than sampled: procfs is gone by the time wrk has exited,
	// and this is the exact figure the kernel charged it.
	spent := report{elapsed: time.Since(started), memoryUnknown: true}
	if state := wrk.ProcessState; state != nil {
		spent.cpu = state.UserTime() + state.SystemTime()
	} else {
		spent.cpuUnknown = true
	}
	return spent, runErr
}

// report is what the proxy spent over one run.
type report struct {
	cpu            time.Duration
	elapsed        time.Duration
	peakKB, meanKB float64
	cpuUnknown     bool
	memoryUnknown  bool
}

// cores is the CPU it held for the length of the run.
func (r report) cores() float64 {
	if r.elapsed <= 0 {
		return 0
	}
	return r.cpu.Seconds() / r.elapsed.Seconds()
}

// cpuLine is [report.String] without the memory,
// for the two processes a run watches for their CPU alone.
func cpuLine(r report) string {
	if r.cpuUnknown {
		return "CPU unavailable"
	}
	return fmt.Sprintf("%.1fs of CPU over %.0fs, %.1f cores",
		r.cpu.Seconds(), r.elapsed.Seconds(), r.cores())
}

// balance is what the three of them came to against the machine underneath.
// Read a run as the proxy's number only while this is under what the machine has:
// past that they are taking cores from each other and the proxy is the one
// being starved, since it is the only one of the three the kernel can't leave idle.
// Hardware threads, so a machine with SMT reaches its physical core count
// well before this reads full.
func balance(proxy, generator, upstream report) string {
	if proxy.cpuUnknown || generator.cpuUnknown || upstream.cpuUnknown {
		return "balance unavailable"
	}
	held := proxy.cores() + generator.cores() + upstream.cores()
	return fmt.Sprintf("balance: %.1f of %d cores held, %.0f%% of the machine "+
		"(proxy %.0f%%, generator %.0f%%, upstream %.0f%%)",
		held, runtime.NumCPU(), 100*held/float64(runtime.NumCPU()),
		100*proxy.cores()/held, 100*generator.cores()/held,
		100*upstream.cores()/held)
}

func (r report) String() string {
	cpu := "CPU unavailable"
	if !r.cpuUnknown {
		cpu = fmt.Sprintf("%.1fs of CPU over %.0fs, %.1f cores",
			r.cpu.Seconds(), r.elapsed.Seconds(), r.cores())
	}
	memory := "memory unavailable"
	if !r.memoryUnknown {
		memory = fmt.Sprintf("%.0f MB peak, %.0f MB mean",
			r.peakKB/1024, r.meanKB/1024)
	}
	return cpu + ", " + memory
}

// meter watches what a process spends over one run.
//
// The CPU is a difference across the run. The memory is sampled rather than read
// once at the end: a single reading says nothing about what a run held,
// and the kernel's high-water mark (VmHWM) spans the whole process,
// so the second run of a pair would inherit the first one's peak.
type meter struct {
	pid     int
	cpu0    float64
	cpu0OK  bool
	started time.Time
	stop    chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	samples []float64
}

// startMeter begins watching pid. Sampling every 50ms is fine detail against
// runs measured in seconds, at two small reads a tick.
func startMeter(pid int) *meter {
	m := &meter{
		pid:     pid,
		started: time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	m.cpu0, m.cpu0OK = processCPU(pid)
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			m.take()
			select {
			case <-m.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return m
}

func (m *meter) take() {
	kb, ok := residentKB(m.pid)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, kb)
}

// finish stops the watching and reports what was spent.
func (m *meter) finish() report {
	close(m.stop)
	<-m.done

	r := report{elapsed: time.Since(m.started)}
	// Both ends are needed: without the first the difference is the whole life
	// of the process, without the second it's zero.
	// Either passes silently for a measurement, so neither is guessed at.
	cpu1, cpu1OK := processCPU(m.pid)
	if m.cpu0OK && cpu1OK {
		r.cpu = time.Duration((cpu1 - m.cpu0) * float64(time.Second))
	} else {
		r.cpuUnknown = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) == 0 {
		r.memoryUnknown = true
		return r
	}
	var total float64
	for _, kb := range m.samples {
		total += kb
		r.peakKB = max(r.peakKB, kb)
	}
	r.meanKB = total / float64(len(m.samples))
	return r
}

// clockTicks is USER_HZ, the unit /proc reports CPU in. Not in the file and not
// fixed by the format, so it's asked for rather than assumed: reading it from
// the C library needs cgo, and one getconf per run is nothing here.
var clockTicks = sync.OnceValues(func() (float64, error) {
	out, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return 0, fmt.Errorf("reading CLK_TCK: %w", err)
	}
	ticks, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || ticks <= 0 {
		return 0, fmt.Errorf("CLK_TCK is not a positive number: %q", out)
	}
	return ticks, nil
})

// processCPU is the CPU a process has spent so far, in seconds.
func processCPU(pid int) (float64, bool) {
	if runtime.GOOS != "linux" {
		return psField(pid, "time=", parseClockTime)
	}
	ticks, err := clockTicks()
	if err != nil {
		return 0, false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	return parseProcStatCPU(stat, ticks)
}

// parseProcStatCPU reads the CPU out of a /proc/<pid>/stat line. The command
// name is the parenthesized second field and can hold spaces and parentheses of its own,
// so fields are counted from after the last one rather than the start.
func parseProcStatCPU(stat []byte, ticks float64) (float64, bool) {
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || ticks <= 0 {
		return 0, false
	}
	fields := strings.Fields(string(stat[end+1:]))
	// utime and stime are the 14th and 15th fields of the line,
	// which are the 12th and 13th of what follows the command name.
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return (utime + stime) / ticks, true
}

// residentKB is the memory a process holds right now.
func residentKB(pid int) (float64, bool) {
	if runtime.GOOS != "linux" {
		return psField(pid, "rss=", func(s string) (float64, error) {
			return strconv.ParseFloat(s, 64)
		})
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		rest, found := strings.CutPrefix(line, "VmRSS:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseFloat(fields[0], 64)
		return kb, err == nil
	}
	return 0, false
}

// psField reads one field of ps, which stands in for procfs where there is none.
func psField(
	pid int, format string, parse func(string) (float64, error),
) (float64, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", format).Output()
	if err != nil {
		return 0, false
	}
	v, err := parse(strings.TrimSpace(string(out)))
	return v, err == nil
}

// parseClockTime reads the [HH:]MM:SS that ps prints. A run lasts seconds,
// so the day form past 24 hours can't turn up.
func parseClockTime(s string) (float64, error) {
	var total float64
	for part := range strings.SplitSeq(s, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, err
		}
		total = total*60 + n
	}
	return total, nil
}

// syncBuffer collects what the proxy writes while the main goroutine reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
