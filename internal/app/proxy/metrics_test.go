package proxy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2"
	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// TestPromLoggerPrintln covers the logger the metrics handler reports through.
//
// Println has the arguments of fmt.Sprintln, and promhttp calls it with the message and
// the error apart, so what's under test is that both arrive whole:
// a format string with one verb keeps the first and leaves the error in an
// %!(EXTRA) tail, which is the part an operator needs.
func TestPromLoggerPrintln(t *testing.T) {
	logs := new(strings.Builder)
	l := &promLogger{log: zerolog.New(logs)}

	// The call promhttp makes, see promhttp/http.go.
	l.Println("error gathering metrics:", errors.New("file already closed"))

	got := logs.String()
	if !strings.Contains(got,
		`"message":"serving metrics: error gathering metrics: file already closed"`) {
		t.Errorf("expected the message and the error whole; received %s", got)
	}
	if strings.Contains(got, "%!") {
		t.Errorf("expected no mangled verb; received %s", got)
	}
	if !strings.Contains(got, `"level":"error"`) {
		t.Errorf("expected it at error level; received %s", got)
	}

	// Any number of arguments, since Println promises no particular count.
	for _, args := range [][]any{{}, {"one"}, {"one", 2, errors.New("three")}} {
		logs.Reset()
		l.Println(args...)
		if got := logs.String(); strings.Contains(got, "%!") {
			t.Errorf("%v: expected no mangled verb; received %s", args, got)
		}
	}
}

// TestCollectBeforeFirstReload covers the exposition of an allowlist that was
// never loaded, which a scrape can reach while the proxy is still starting.
//
// The two gauges answer differently on purpose: 0 documents is the truth,
// and a load time of 0 isn't — it reads as 1970, and an alert on the age of the
// allowlist would fire on it. Nothing is the honest answer until there's a load.
func TestCollectBeforeFirstReload(t *testing.T) {
	list := allowlist.New(sha256.New, gqlhash.Options{})
	m := newMetrics(new(counters), list)

	rec := httptest.NewRecorder()
	m.Handler(testLogger()).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200; received %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "gqlhash_proxy_allowlist_documents 0") {
		t.Errorf("expected 0 documents; received %s", body)
	}
	// No sample and no family at all, HELP line included.
	if strings.Contains(body, "gqlhash_proxy_allowlist_loaded_timestamp_seconds") {
		t.Errorf("expected no load time before the first reload; received %s", body)
	}

	// After one, both are there and the time is the load's.
	before := time.Now().Unix()
	if _, err := list.Reload(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	m.Handler(testLogger()).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body = rec.Body.String()

	// The sample, not the HELP line that carries the same name.
	var seconds float64
	var found bool
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := fmt.Sscanf(line,
			"gqlhash_proxy_allowlist_loaded_timestamp_seconds %g", &seconds,
		); err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected the load time after a reload; received %s", body)
	}
	if int64(seconds) < before {
		t.Errorf("expected the time of the reload; received %v", seconds)
	}
}
