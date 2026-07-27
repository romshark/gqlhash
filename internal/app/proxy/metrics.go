package proxy

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// decision is what the proxy did with a request. It's the label of a metric, so
// the strings are the values a dashboard groups by.
type decision string

const (
	decisionAllowed   decision = "allowed"
	decisionRejected  decision = "rejected"
	decisionMalformed decision = "malformed"
)

// Metrics exposes the state of the proxy in the Prometheus text format.
//
// The counters are read from [Counters] on scrape, so counting a request costs
// what it costs without metrics. Only the duration is measured per request, which
// is why [Proxy.ServeHTTP] takes the clock only when metrics are on.
type Metrics struct {
	registry *prometheus.Registry
	duration *prometheus.HistogramVec
}

// NewMetrics returns the metrics of proxy, reading its allowlist from store.
func NewMetrics(proxy *Proxy, store *Store) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gqlhash",
			Subsystem: "proxy",
			Name:      "request_duration_seconds",
			Help:      "Time from reading a request to answering it.",
			// A rejection takes microseconds and a forwarded request
			// milliseconds, so the buckets span both. The default set starts
			// at 5ms.
			Buckets: []float64{
				0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005,
				0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5,
			},
		}, []string{"decision"}),
	}

	m.registry.MustRegister(
		m.duration,
		&proxyCollector{proxy: proxy, store: store},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Observe records one request. start is when it arrived.
func (m *Metrics) Observe(d decision, start time.Time) {
	m.duration.WithLabelValues(string(d)).Observe(time.Since(start).Seconds())
}

// Handler serves the metrics.
func (m *Metrics) Handler(log zerolog.Logger) http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorLog:      &promLogger{log: log},
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// promLogger hands the errors of the metrics handler to zerolog.
type promLogger struct{ log zerolog.Logger }

func (l *promLogger) Println(v ...any) {
	l.log.Error().Msgf("serving metrics: %v", v...)
}

// proxyCollector reports the counters and the allowlist of a proxy, read on
// scrape so the hot path stays untouched.
type proxyCollector struct {
	proxy *Proxy
	store *Store
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
	allowed, rejected, malformed, upstream := c.proxy.CountersSnapshot()
	count := func(v uint64, d decision) {
		ch <- prometheus.MustNewConstMetric(descRequests,
			prometheus.CounterValue, float64(v), string(d))
	}
	count(allowed, decisionAllowed)
	count(rejected, decisionRejected)
	count(malformed, decisionMalformed)
	ch <- prometheus.MustNewConstMetric(descUpstreamErrors,
		prometheus.CounterValue, float64(upstream))

	list := c.store.Load()
	ch <- prometheus.MustNewConstMetric(descDocuments,
		prometheus.GaugeValue, float64(list.Len()))
	if list != nil {
		ch <- prometheus.MustNewConstMetric(descLoadedAt,
			prometheus.GaugeValue, float64(list.loadedAt.Unix()))
	}
}
