package main

import (
	"strings"
	"testing"
	"time"
)

// TestWrkDuration covers the syntax wrk takes, which is narrower than the one
// [flag.Duration] parses. Anything it would refuse has to be refused here, or a
// run fails in the child process with wrk's usage text and no explanation.
func TestWrkDuration(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{time.Second, "1s"},
		{10 * time.Second, "10s"},
		{90 * time.Second, "90s"},
		{time.Hour, "3600s"},
	} {
		got, err := wrkDuration(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.in, err)
		} else if got != c.want {
			t.Errorf("%s: expected %q; received %q", c.in, c.want, got)
		}
	}

	// wrk rejects every one of these, so this does first.
	for _, in := range []time.Duration{
		0, -time.Second, 500 * time.Millisecond, 1500 * time.Millisecond,
		time.Minute + 30*time.Second + 500*time.Millisecond,
	} {
		if got, err := wrkDuration(in); err == nil {
			t.Errorf("%s: expected a refusal; received %q", in, got)
		}
	}
}

// TestConnections pins the contract wrk imposes: it won't run with fewer
// connections than threads.
func TestConnections(t *testing.T) {
	// Four threads rather than the default, which follows the machine:
	// what this pins is the rule, not the count one machine happens to derive.
	for _, n := range []int{0, 1, 2, 3, -1, maxConnections + 1} {
		if err := checkConnections(4, n); err == nil {
			t.Errorf("%d: expected a refusal", n)
		}
	}
	for _, n := range []int{4, 200, maxConnections} {
		if err := checkConnections(4, n); err != nil {
			t.Errorf("%d: unexpected refusal: %v", n, err)
		}
	}
	// The default has to be a count wrk will take, whatever the machine.
	if err := checkConnections(defaultWrkThreads(), 200); err != nil {
		t.Errorf("the default thread count was refused: %v", err)
	}
	// The floor follows -threads rather than the default.
	if err := checkConnections(16, 8); err == nil {
		t.Error("expected 8 connections under 16 threads to be refused")
	}
	if err := checkConnections(0, 200); err == nil {
		t.Error("expected a thread count under one to be refused")
	}
	// Threads never outnumber connections, whatever the count.
	for _, c := range []struct{ threads, connections, want int }{
		{4, 4, 4}, {4, 8, 4}, {4, 200, 4}, {16, 200, 16}, {16, 16, 16},
	} {
		if got := threadsFor(c.threads, c.connections); got != c.want {
			t.Errorf("%d threads, %d connections: expected %d; received %d",
				c.threads, c.connections, c.want, got)
		}
	}
}

// TestBalance covers the accounting that tells a saturated proxy from a machine
// with nothing left to give it.
func TestBalance(t *testing.T) {
	second := time.Second
	got := balance(
		report{cpu: 14 * second, elapsed: second},
		report{cpu: 4 * second, elapsed: second},
		report{cpu: 2 * second, elapsed: second},
	)
	for _, want := range []string{"20.0 of ", "proxy 70%", "generator 20%", "upstream 10%"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}

	// A reading that failed can't be counted as a zero: it would report the
	// machine as emptier than it is, which is the one conclusion this feeds.
	if got := balance(
		report{cpu: second, elapsed: second},
		report{cpuUnknown: true},
		report{cpu: second, elapsed: second},
	); got != "balance unavailable" {
		t.Errorf("expected an unavailable balance; received %q", got)
	}
}

// TestDecisionsOnly covers the check that makes a measured run self-validating.
func TestDecisionsOnly(t *testing.T) {
	if err := (decisions{Allowed: 100}).only(decisionAllowed); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := (decisions{Rejected: 100}).only(decisionRejected); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// A run that decided nothing measured nothing.
	if err := (decisions{}).only(decisionAllowed); err == nil {
		t.Error("expected an empty run to be refused")
	}

	// The one wrk can't see: the upstream failing behind an allowed request is
	// a 502, which counts as non-2xx exactly like the expected 403 would.
	err := decisions{Allowed: 100, Upstream: 3}.only(decisionAllowed)
	if err == nil {
		t.Fatal("expected upstream errors during an allowed run to be caught")
	}
	if !strings.Contains(err.Error(), decisionUpstream) {
		t.Errorf("expected the message to name the decision; received %q", err)
	}

	// And the other way: a rejected run that quietly forwarded something.
	if err := (decisions{Rejected: 100, Allowed: 1}).only(decisionRejected); err == nil {
		t.Error("expected a stray allowed request to be caught")
	}
	// Malformed answers look like rejections from outside: both are non-2xx.
	if err := (decisions{
		Rejected: 100, Malformed: 5,
	}).only(decisionRejected); err == nil {
		t.Error("expected malformed requests during a rejected run to be caught")
	}
}

func TestDecisionsSince(t *testing.T) {
	before := decisions{Allowed: 10, Rejected: 20, Malformed: 30, Upstream: 40}
	after := decisions{Allowed: 15, Rejected: 20, Malformed: 31, Upstream: 40}
	got := after.since(before)
	want := decisions{Allowed: 5, Malformed: 1}
	if got != want {
		t.Errorf("expected %+v; received %+v", want, got)
	}
}

// TestLoggedAddress covers the log reading that finds the ports behind :0.
func TestLoggedAddress(t *testing.T) {
	const logs = `not json at all
{"level":"info","message":"allowlist loaded","documents":1}
{"level":"info","message":"serving /metrics and /reload","address":"127.0.0.1:9090"}

{"level":"info","message":"listening","address":"127.0.0.1:8080","documents":1}
`
	if got := loggedAddress(logs, "listening"); got != "127.0.0.1:8080" {
		t.Errorf("expected the data-plane address; received %q", got)
	}
	if got := loggedAddress(
		logs, "serving /metrics and /reload",
	); got != "127.0.0.1:9090" {
		t.Errorf("expected the control address; received %q", got)
	}
	if got := loggedAddress(logs, "nothing logs this"); got != "" {
		t.Errorf("expected no address; received %q", got)
	}
	if got := loggedAddress("", "listening"); got != "" {
		t.Errorf("expected no address from an empty log; received %q", got)
	}
}

// TestWithoutProxyEnv covers the isolation that keeps a variable in the shell
// from configuring the proxy being measured.
func TestWithoutProxyEnv(t *testing.T) {
	got := withoutProxyEnv([]string{
		"PATH=/usr/bin",
		"GQLHASH_PROXY_LOG_JSON=false",
		"GOGC=800",
		"GQLHASH_PROXY_SERVER_MAX_BODY=10",
		"GOMEMLIMIT=1GiB",
	})
	want := []string{"PATH=/usr/bin", "GOGC=800", "GOMEMLIMIT=1GiB"}
	if len(got) != len(want) {
		t.Fatalf("expected %v; received %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expected %v; received %v", want, got)
			break
		}
	}
}

// TestParseProcStatCPU covers the field counting, which a command name holding
// spaces or parentheses would break if it were counted from the start.
func TestParseProcStatCPU(t *testing.T) {
	// Fields after the name: state, ppid, pgrp, session, tty, tpgid, flags,
	// minflt, cminflt, majflt, cmajflt, utime, stime, ...
	const tail = " S 1 2 3 4 5 6 7 8 9 10 250 150 0 0"

	// A comm can hold spaces and parentheses; the last ')' on the line is what
	// ends it, since nothing after it carries one.
	for _, name := range []string{"(proxy)", "(a name) with parens)", "(x y z)"} {
		got, ok := parseProcStatCPU([]byte("42 "+name+tail), 100)
		if !ok {
			t.Errorf("%s: expected it to parse", name)
			continue
		}
		if want := 4.0; got != want { // (250 + 150) / 100
			t.Errorf("%s: expected %v; received %v", name, want, got)
		}
	}

	// The tick rate scales the answer, so a bad one has to be refused rather
	// than divided by.
	if _, ok := parseProcStatCPU([]byte("42 (proxy)"+tail), 0); ok {
		t.Error("expected a zero tick rate to be refused")
	}
	if _, ok := parseProcStatCPU([]byte("nothing parenthesized"), 100); ok {
		t.Error("expected a line without a command name to be refused")
	}
	if _, ok := parseProcStatCPU([]byte("42 (proxy) S 1 2"), 100); ok {
		t.Error("expected a short line to be refused")
	}
}

func TestParseClockTime(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
	}{
		{"0:01", 1},
		{"1:30", 90},
		{"01:02:03", 3723},
		{"0:01.50", 1.5},
	} {
		got, err := parseClockTime(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.in, err)
		} else if got != c.want {
			t.Errorf("%q: expected %v; received %v", c.in, c.want, got)
		}
	}
	if _, err := parseClockTime("not a time"); err == nil {
		t.Error("expected a refusal")
	}
}

// TestReportString covers what a run prints, and that a reading which failed
// says so rather than passing a zero off as a measurement.
func TestReportString(t *testing.T) {
	full := report{
		cpu: 12 * time.Second, elapsed: 2 * time.Second,
		peakKB: 100 * 1024, meanKB: 50 * 1024,
	}
	got := full.String()
	for _, want := range []string{
		"12.0s of CPU", "6.0 cores", "100 MB peak", "50 MB mean",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}

	if got := (report{
		cpuUnknown: true, memoryUnknown: true,
	}).String(); !strings.Contains(
		got, "CPU unavailable",
	) || !strings.Contains(got, "memory unavailable") {
		t.Errorf("expected both to be reported as unavailable; received %q", got)
	}

	// Nothing is divided by a zero-length run.
	if got := (report{cpu: time.Second}).cores(); got != 0 {
		t.Errorf("expected no cores for a zero-length run; received %v", got)
	}
}
