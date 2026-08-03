package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"

	"github.com/romshark/gqlhash/v2/internal/allowlist"
)

// decision is what the proxy did with a request. An index rather than the label itself,
// so a request finds its histogram by offset, see [metrics].
type decision uint8

const (
	decisionAllowed decision = iota
	decisionRejected
	decisionMalformed
	decisionTooLarge
	decisionAmbiguous
	decisionTooDeep
	decisionBatchTooLarge
	decisionMethodNotAllowed
	decisionCount
)

// decisionLabels is what a dashboard groups by, in the order of the decisions.
var decisionLabels = [decisionCount]string{
	decisionAllowed:          "allowed",
	decisionRejected:         "rejected",
	decisionMalformed:        "malformed",
	decisionTooLarge:         "too_large",
	decisionAmbiguous:        "ambiguous",
	decisionTooDeep:          "too_deep",
	decisionBatchTooLarge:    "batch_too_large",
	decisionMethodNotAllowed: "method_not_allowed",
}

func (d decision) String() string { return decisionLabels[d] }

// metrics exposes the state of the proxy in the Prometheus text format.
//
// The counters are read from [counters] on scrape, so counting a request costs
// what it costs without metrics. Only the duration is measured per request,
// which is why [proxy.ServeHTTP] reads the clock.
type metrics struct {
	registry *prometheus.Registry
	duration *prometheus.HistogramVec
	// observers is the histogram of each decision, resolved once. Asking the
	// vector per request hashes the label and takes a lock:
	// ~3% of the rejected path.
	observers [decisionCount]prometheus.Observer
}

// newMetrics returns metrics over counters and the documents in use in list.
// It takes the counters rather than the [proxy] holding them, so a proxy can
// build its own: the two would otherwise each need the other first.
func newMetrics(counters *counters, list *allowlist.Allowlist) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gqlhash",
			Subsystem: "proxy",
			Name:      "request_duration_seconds",
			Help:      "Time from reading a request to answering it.",
			// A rejection takes microseconds and a forward milliseconds,
			// so the buckets span both. The default set starts at 5ms.
			Buckets: []float64{
				0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
				0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5,
			},
		}, []string{"decision"}),
	}

	// Resolved here, so every series exists from the start and a dashboard
	// doesn't wait for the first request of a kind.
	for d := range m.observers {
		m.observers[d] = m.duration.WithLabelValues(decision(d).String())
	}

	m.registry.MustRegister(
		m.duration,
		&proxyCollector{counters: counters, allowlist: list},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Observe records one request. start is when it arrived.
func (m *metrics) Observe(d decision, start time.Time) {
	m.observers[d].Observe(time.Since(start).Seconds())
}

func (m *metrics) Handler(log zerolog.Logger) http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorLog:      &promLogger{log: log},
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// promLogger hands the errors of the metrics handler to zerolog.
type promLogger struct{ log zerolog.Logger }

// Println takes the arguments of [fmt.Sprintln]: any number, no format string.
// promhttp passes the message and the error apart, so they're joined here.
// One verb would keep the first and leave the error in an %!(EXTRA) tail.
func (l *promLogger) Println(v ...any) {
	l.log.Error().Msg("serving metrics: " +
		strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}

// proxyCollector reports the counters and the allowlist of a proxy,
// read on scrape so the hot path stays untouched.
type proxyCollector struct {
	counters  *counters
	allowlist *allowlist.Allowlist
}

var (
	descRequests = prometheus.NewDesc(
		"gqlhash_proxy_requests_total",
		"Requests by what the proxy decided.",
		[]string{"decision"}, nil)
	descUpstreamErrors = prometheus.NewDesc(
		"gqlhash_proxy_upstream_errors_total",
		"Requests that were allowed but that the upstream API didn't answer.",
		nil, nil)
	descDocuments = prometheus.NewDesc(
		"gqlhash_proxy_allowlist_documents",
		"Documents on the allowlist in use.",
		nil, nil)
	descLoadedAt = prometheus.NewDesc(
		"gqlhash_proxy_allowlist_loaded_timestamp_seconds",
		"When the allowlist in use was loaded.",
		nil, nil)
)

func (c *proxyCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRequests
	ch <- descUpstreamErrors
	ch <- descDocuments
	ch <- descLoadedAt
}

func (c *proxyCollector) Collect(ch chan<- prometheus.Metric) {
	made := c.counters.snapshot()
	count := func(v uint64, d decision) {
		ch <- prometheus.MustNewConstMetric(descRequests,
			prometheus.CounterValue, float64(v), d.String())
	}
	count(made.allowed, decisionAllowed)
	count(made.rejected, decisionRejected)
	count(made.malformed, decisionMalformed)
	count(made.tooLarge, decisionTooLarge)
	count(made.ambiguous, decisionAmbiguous)
	count(made.tooDeep, decisionTooDeep)
	count(made.batchBig, decisionBatchTooLarge)
	count(made.methodBad, decisionMethodNotAllowed)
	ch <- prometheus.MustNewConstMetric(descUpstreamErrors,
		prometheus.CounterValue, float64(made.upstream))

	// One call, so a reload between them can't pair one load's count with another's time.
	documents, loadedAt := c.allowlist.Stats()

	// A count of 0 is the truth before the first reload. A timestamp of 0 isn't:
	// it reads as 1970, and an alert on the allowlist's age would fire on it.
	ch <- prometheus.MustNewConstMetric(descDocuments,
		prometheus.GaugeValue, float64(documents))
	if !loadedAt.IsZero() {
		ch <- prometheus.MustNewConstMetric(descLoadedAt,
			prometheus.GaugeValue, float64(loadedAt.Unix()))
	}
}
