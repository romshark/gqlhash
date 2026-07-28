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

// decision is what the proxy did with a request. It's the label of a metric,
// so the strings are the values a dashboard groups by.
type decision string

const (
	decisionAllowed   decision = "allowed"
	decisionRejected  decision = "rejected"
	decisionMalformed decision = "malformed"
)

// metrics exposes the state of the proxy in the Prometheus text format.
//
// The counters are read from [counters] on scrape, so counting a request costs
// what it costs without metrics. Only the duration is measured per request,
// which is why [proxy.ServeHTTP] reads the clock for every request.
type metrics struct {
	registry *prometheus.Registry
	duration *prometheus.HistogramVec
}

// newMetrics returns metrics over counters and the documents in use in list.
//
// It takes the counters rather than the [proxy] that keeps them, so a proxy can
// build its own metrics while it's being built: the two would otherwise each
// need the other first.
func newMetrics(counters *counters, list *allowlist.Allowlist) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gqlhash",
			Subsystem: "proxy",
			Name:      "request_duration_seconds",
			Help:      "Time from reading a request to answering it.",
			// A rejection takes microseconds and a forwarded request milliseconds,
			// so the buckets span both. The default set starts at 5ms.
			Buckets: []float64{
				0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
				0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5,
			},
		}, []string{"decision"}),
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
	m.duration.WithLabelValues(string(d)).Observe(time.Since(start).Seconds())
}

// Handler serves the metrics.
func (m *metrics) Handler(log zerolog.Logger) http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorLog:      &promLogger{log: log},
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// promLogger hands the errors of the metrics handler to zerolog.
type promLogger struct{ log zerolog.Logger }

// Println takes the arguments of [fmt.Sprintln]: any number of them and no
// format string. promhttp passes the message and the error apart, so they're
// joined before they reach the log. A format string with one verb here would
// keep the first and leave the error in an %!(EXTRA) tail, which is the part
// worth reading.
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
			prometheus.CounterValue, float64(v), string(d))
	}
	count(made.allowed, decisionAllowed)
	count(made.rejected, decisionRejected)
	count(made.malformed, decisionMalformed)
	ch <- prometheus.MustNewConstMetric(descUpstreamErrors,
		prometheus.CounterValue, float64(made.upstream))

	// Both come from one call, so a reload between them can't pair the count of
	// one load with the time of another.
	documents, loadedAt := c.allowlist.Stats()

	// A count of 0 is the truth before the first reload. A timestamp of 0 isn't:
	// it reads as 1970, and an alert on the age of the allowlist would fire on it.
	// Nothing is the honest answer until there's a load to report.
	ch <- prometheus.MustNewConstMetric(descDocuments,
		prometheus.GaugeValue, float64(documents))
	if !loadedAt.IsZero() {
		ch <- prometheus.MustNewConstMetric(descLoadedAt,
			prometheus.GaugeValue, float64(loadedAt.Unix()))
	}
}
