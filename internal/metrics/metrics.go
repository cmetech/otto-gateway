// Package metrics exposes a Prometheus /metrics endpoint for the gateway
// (parity roadmap Track 4). It owns its own registry so the gateway's usage and
// ops signals scrape into a timeseries DB without coupling the metrics wiring to
// pool/session internals — the pool + session gauges are pulled at scrape time
// through injected closures, mirroring the boundary-clean adapter pattern used
// by internal/admin.
//
// All series use the gw_ prefix (matches the de-brand: GW_LOG, GW_INSTALL_DIR).
package metrics

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsPath is excluded from request metrics + (in server wiring) the access
// log so high-frequency scrapes neither self-measure nor spam logs.
const metricsPath = "/metrics"

// PoolStats is the metrics-friendly projection of pool.HealthSummary. The cmd
// wiring adapts the pool's runtime type into this so this package never imports
// internal/pool.
type PoolStats struct {
	Size, Alive, Busy   int
	Healthy             bool
	SpawnFailing        bool
	LastSpawnErrUnixSec float64 // 0 when no spawn error recorded
	LastProgressUnixSec float64 // 0 before first forward-progress event
}

// Metrics owns the Prometheus registry and the request-instrumentation
// middleware. Construct with New; expose Handler() at GET /metrics and wrap the
// router with Middleware.
type Metrics struct {
	reg      *prometheus.Registry
	reqTotal *prometheus.CounterVec
	reqDur   *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// New builds the registry: the free Go-runtime + process collectors, the HTTP
// request instruments, and a pull-collector over the injected pool/session
// sources (called at scrape time — no background goroutine).
func New(pool func() PoolStats, sessions func() int) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		reg: reg,
		reqTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_http_requests_total",
			Help: "Total HTTP requests handled, by method, matched route, and status.",
		}, []string{"method", "route", "status"}),
		reqDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gw_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds, by method, matched route, and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gw_http_in_flight_requests",
			Help: "In-flight HTTP requests currently being served.",
		}),
	}
	reg.MustRegister(m.reqTotal, m.reqDur, m.inFlight, newPoolCollector(pool, sessions))
	return m
}

// Handler serves the Prometheus exposition format. Mount at GET /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Middleware records request count, duration, and in-flight gauge for every
// route except /metrics itself. The route label is chi's RoutePattern (bounded
// cardinality) — never the raw path — collapsing to "other" for unmatched
// requests.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == metricsPath {
			next.ServeHTTP(w, r)
			return
		}
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)

		// RoutePattern is populated by chi during routing, so it is read AFTER
		// next. Empty (no match) collapses to "other" to bound cardinality.
		route := "other"
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		status := statusText(ww.Status())
		m.reqTotal.WithLabelValues(r.Method, route, status).Inc()
		m.reqDur.WithLabelValues(r.Method, route, status).Observe(time.Since(start).Seconds())
	})
}

// statusText renders the status code as a string label. A zero status (handler
// wrote no header and no body) is normalized to 200, matching net/http's
// implicit WriteHeader(200).
func statusText(code int) string {
	if code == 0 {
		code = http.StatusOK
	}
	// itoa without fmt to keep the hot path allocation-light.
	return itoa(code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
