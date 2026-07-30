package acceptance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDecisionsAreCounted covers what a decision is called. Every refusal has a
// series of its own, because an operator reading a spike does something
// different for each: an allowlist out of date, a client sending nonsense,
// a client and -server.max-body disagreeing, somebody probing for a way past the
// allowlist, and a nesting attack.
//
// It runs on a server of its own: what's asserted is the count,
// which every request before it would move.
func TestDecisionsAreCounted(t *testing.T) {
	// The depth limit both commands are built with, and a document past it.
	const limit = 128
	tooDeep := "{" + strings.Repeat("f{", limit) + "f" + strings.Repeat("}", limit+1)

	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc}, "-server.max-body", "4096")

		// One request per decision.
		for _, tc := range []struct {
			decision, body string
			expect         int
		}{
			{"allowed", docAllowed, http.StatusOK},
			{"rejected", docRejected, http.StatusForbidden},
			{"malformed", `not json`, http.StatusBadRequest},
			{"too_large", `{"query":"` + strings.Repeat("x", 8192) + `"}`,
				http.StatusRequestEntityTooLarge},
			{"ambiguous", `{"query":"` + allowedText + `","query":"` +
				rejectedText + `"}`, http.StatusBadRequest},
			{"too_deep", `{"query":` + strconv.Quote(tooDeep) + `}`,
				http.StatusForbidden},
		} {
			if code, answer := post(t, e.server, tc.body); code != tc.expect {
				t.Fatalf("%s: expected %d; received %d: %s",
					tc.decision, tc.expect, code, answer)
			}
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		for _, decision := range []string{
			"allowed", "rejected", "malformed", "too_large", "ambiguous", "too_deep",
		} {
			for _, metric := range []string{
				`gqlhash_proxy_requests_total{decision=%q} 1`,
				`gqlhash_proxy_request_duration_seconds_count{decision=%q} 1`,
			} {
				want := strings.ReplaceAll(metric, "%q", `"`+decision+`"`)
				if !strings.Contains(exposition, want) {
					t.Errorf("expected %q in the exposition", want)
				}
			}
		}

		// The same counts, in the shape a deployment reads without Prometheus.
		_, body := control(t, e.server, http.MethodGet, "/status", "")
		var status struct {
			Allowed   int `json:"allowed"`
			Rejected  int `json:"rejected"`
			Malformed int `json:"malformed"`
			TooLarge  int `json:"too_large"`
			Ambiguous int `json:"ambiguous"`
			TooDeep   int `json:"too_deep"`
		}
		if err := json.Unmarshal([]byte(body), &status); err != nil {
			t.Fatalf("answering no JSON: %v: %s", err, body)
		}
		if status.Allowed != 1 || status.Rejected != 1 || status.Malformed != 1 ||
			status.TooLarge != 1 || status.Ambiguous != 1 || status.TooDeep != 1 {
			t.Errorf("expected one of each; received %s", body)
		}
	})
}

// TestAmbiguousDecisionCoversEveryShape covers the requests that name their
// document twice, which are counted apart from malformed ones however they name it:
// a JSON member twice, a GET carrying a body, a query parameter beside a body,
// and a GET naming the parameter twice.
func TestAmbiguousDecisionCoversEveryShape(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		allowed := url.QueryEscape(allowedText)

		// A JSON member named twice.
		body := `{"query":"` + allowedText + `","query":"` + rejectedText + `"}`
		if code, _ := post(t, e.server, body); code != http.StatusBadRequest {
			t.Fatalf("the member twice: received %d", code)
		}
		// A GET naming the parameter twice.
		if code, _ := get(t, e.server, "query="+allowed+"&query="+allowed); code !=
			http.StatusBadRequest {
			t.Fatalf("the parameter twice: received %d", code)
		}
		// A GET carrying a body.
		req, err := http.NewRequest(http.MethodGet,
			"http://"+e.address+"/graphql?query="+allowed,
			strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := send(t, req); code != http.StatusBadRequest {
			t.Fatalf("a GET with a body: received %d", code)
		}
		// A query parameter beside a body.
		req, err = http.NewRequest(http.MethodPost,
			"http://"+e.address+"/graphql?query="+allowed,
			strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if code, _ := send(t, req); code != http.StatusBadRequest {
			t.Fatalf("a parameter beside a body: received %d", code)
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		for _, want := range []string{
			`gqlhash_proxy_requests_total{decision="ambiguous"} 4`,
			// None of them is a malformed request, which is where they used to
			// land and where a broken client still does.
			`gqlhash_proxy_requests_total{decision="malformed"} 0`,
		} {
			if !strings.Contains(exposition, want) {
				t.Errorf("expected %q in the exposition", want)
			}
		}
		if n := e.api.count(); n != 0 {
			t.Errorf("expected none of them forwarded; received %d", n)
		}
	})
}

// TestMetricsExpositionShape covers what a scraper reads: every series is
// declared with a type, and a rate() over a counter declared as a gauge is
// silently wrong. The buckets are the proxy's own, since a rejection takes
// microseconds and the default set starts at five milliseconds.
func TestMetricsExpositionShape(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
			t.Fatal("expected the document served")
		}
		if code, _ := post(t, e.server, docRejected); code != http.StatusForbidden {
			t.Fatal("expected the other refused")
		}
		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")

		for _, want := range []string{
			"# TYPE gqlhash_proxy_requests_total counter",
			"# TYPE gqlhash_proxy_upstream_errors_total counter",
			"# TYPE gqlhash_proxy_request_duration_seconds histogram",
			"# TYPE gqlhash_proxy_allowlist_documents gauge",
			"# TYPE gqlhash_proxy_allowlist_loaded_timestamp_seconds gauge",
			// A decision takes microseconds, so the buckets reach there.
			`gqlhash_proxy_request_duration_seconds_bucket{decision="rejected",le="0.0001"}`,
			`gqlhash_proxy_request_duration_seconds_bucket{decision="rejected",le="0.001"}`,
		} {
			if !strings.Contains(exposition, want) {
				t.Errorf("expected %q in the exposition", want)
			}
		}

		// The sum is in seconds. A rejection is under a millisecond and over
		// nothing at all, which is what a milliseconds-for-seconds mix-up or a
		// clock read after the answer would break.
		sum := metricValue(t, exposition,
			`gqlhash_proxy_request_duration_seconds_sum{decision="rejected"}`)
		if sum <= 0 || sum >= 1 {
			t.Errorf("expected a rejection under a second and over zero; received %v",
				sum)
		}
	})
}

// TestMetricsScrapeIsIdempotent covers reading the metrics twice: the second
// scrape reports what the first did. An exposition that resets on collect adds
// up to the same total for one scraper and to nothing for two.
func TestMetricsScrapeIsIdempotent(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})
		for range 3 {
			if code, _ := post(t, e.server, docAllowed); code != http.StatusOK {
				t.Fatal("expected the document served")
			}
		}

		_, first := control(t, e.server, http.MethodGet, "/metrics", "")
		_, second := control(t, e.server, http.MethodGet, "/metrics", "")
		for _, series := range []string{
			`gqlhash_proxy_requests_total{decision="allowed"}`,
			`gqlhash_proxy_request_duration_seconds_count{decision="allowed"}`,
		} {
			if a, b := metricValue(t, first, series), metricValue(t, second, series); a != b {
				t.Errorf("%s: expected the same on a second scrape; %v then %v",
					series, a, b)
			} else if a != 3 {
				t.Errorf("%s: expected 3; received %v", series, a)
			}
		}
	})
}

// TestMetricsAllowlistTracksReload covers the gauges of the allowlist:
// the size is the list in use, and the load time moves with it.
// A dashboard reads them to see a reload arrive.
func TestMetricsAllowlistTracksReload(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc})

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition, "gqlhash_proxy_allowlist_documents"); got != 1 {
			t.Errorf("expected one document; received %v", got)
		}
		before := metricValue(t, exposition,
			"gqlhash_proxy_allowlist_loaded_timestamp_seconds")
		if before <= 0 {
			t.Errorf("expected a load time; received %v", before)
		}

		writeDoc(t, e.dir, "b.graphql", "{ b }")
		time.Sleep(1100 * time.Millisecond)
		if code, body := control(
			t, e.server, http.MethodPost, "/reload", "",
		); code != http.StatusOK {
			t.Fatalf("reload: %d: %s", code, body)
		}

		_, exposition = control(t, e.server, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition, "gqlhash_proxy_allowlist_documents"); got != 2 {
			t.Errorf("expected the reloaded size; received %v", got)
		}
		if after := metricValue(t, exposition,
			"gqlhash_proxy_allowlist_loaded_timestamp_seconds"); after <= before {
			t.Errorf("expected the load time to move; %v then %v", before, after)
		}
	})
}

// TestMetricsBatchCountsOnce covers a batch, which is one request carrying
// several documents: it's one decision, so it's counted once.
// Counting per document would make a request rate that no client's traffic matches.
func TestMetricsBatchCountsOnce(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		e := newEnv(t, tgt, []string{allowedDoc, "{ b }"}, "-allow-batch")

		batch := `[{"query":"` + allowedText + `"},{"query":"{ b }"}]`
		if code, answer := post(t, e.server, batch); code != http.StatusOK {
			t.Fatalf("expected the batch served; received %d: %s", code, answer)
		}

		_, exposition := control(t, e.server, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="allowed"}`); got != 1 {
			t.Errorf("expected the batch counted once; received %v", got)
		}
	})
}

// TestClientHangupIsNoUpstreamError covers a client that gives up while its
// request is with the API. The proxy decided to allow it, so that's what it
// counts: a closed browser tab is nothing the API did wrong,
// and counting it as an upstream failure pages somebody for it.
func TestClientHangupIsNoUpstreamError(t *testing.T) {
	each(t, func(t *testing.T, tgt target) {
		reached := make(chan struct{})
		release := make(chan struct{})
		letGo := sync.OnceFunc(func() { close(release) })
		defer letGo()
		var once sync.Once
		s := serveUpstream(t, tgt, func(w http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(reached) })
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, upstreamAnswer)
		})

		// A client that goes away once the API has the request.
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-reached
			cancel()
		}()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+s.address+"/graphql", strings.NewReader(docAllowed))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if _, err := http.DefaultClient.Do(req); err == nil {
			t.Fatal("expected the client's request to be given up on")
		}
		letGo()

		// Give the proxy a moment to notice and count.
		time.Sleep(200 * time.Millisecond)
		_, exposition := control(t, s, http.MethodGet, "/metrics", "")
		if got := metricValue(t, exposition,
			"gqlhash_proxy_upstream_errors_total"); got != 0 {
			t.Errorf("expected no upstream error; received %v", got)
		}
		if got := metricValue(t, exposition,
			`gqlhash_proxy_requests_total{decision="allowed"}`); got != 1 {
			t.Errorf("expected it counted as allowed; received %v", got)
		}
	})
}

// metricValue reads one series out of an exposition.
func metricValue(t *testing.T, exposition, series string) float64 {
	t.Helper()
	for line := range strings.SplitSeq(exposition, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name != series {
			continue
		}
		got, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			t.Fatalf("%s: %q is no value: %v", series, value, err)
		}
		return got
	}
	t.Fatalf("%s: no such series in the exposition:\n%s", series, exposition)
	return 0
}
