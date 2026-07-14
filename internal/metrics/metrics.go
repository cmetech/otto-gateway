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
	"strings"
	"sync"
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

// SkillHeader is the request header Co-Worker (or any client) sets to tag an
// LLM call with the invoking skill. Adding this header is fully compatible with
// the Ollama/OpenAI/Anthropic contracts (extra request headers are ignored by
// the API shape). Read into the gw_llm_requests_total{surface,skill} metric.
const SkillHeader = "X-GW-Skill"

// MaxSkillCardinality bounds the distinct skill label values (per gateway) so a
// misbehaving or unknown client cannot blow up TSDB series — the (cap+1)th
// distinct skill collapses to "other".
const MaxSkillCardinality = 64

// BuildInfo identifies this gateway instance. GatewayID becomes a constant
// label on every series so a fleet of laptops can be grouped by gateway;
// Version/Commit ride the gw_build_info metric.
type BuildInfo struct {
	GatewayID string
	Version   string
	Commit    string
}

// PoolStats is the metrics-friendly projection of pool.HealthSummary + the
// Track 4b event counters. The cmd wiring adapts the pool's runtime type into
// this so this package never imports internal/pool.
type PoolStats struct {
	Size, Alive, Busy   int
	Healthy             bool
	SpawnFailing        bool
	LastSpawnErrUnixSec float64 // 0 when no spawn error recorded
	LastProgressUnixSec float64 // 0 before first forward-progress event

	// Track 4b monotonic event counters (pool-owned).
	SlotRespawns     uint64
	PingEscalations  uint64
	PingSuspendSkips uint64
}

// SessionStats is the metrics-friendly projection of the session registry.
type SessionStats struct {
	Active int
	Reaped uint64 // Track 4b monotonic counter
}

// Metrics owns the Prometheus registry and the request-instrumentation
// middleware. Construct with New; expose Handler() at GET /metrics and wrap the
// router with Middleware.
type Metrics struct {
	reg      *prometheus.Registry
	reqTotal *prometheus.CounterVec
	reqDur   *prometheus.HistogramVec
	inFlight prometheus.Gauge
	llmTotal *prometheus.CounterVec
	skills   *skillLimiter
}

// skillLimiter sanitizes + cardinality-caps the X-GW-Skill label value.
type skillLimiter struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	limit int
}

func newSkillLimiter(limit int) *skillLimiter {
	return &skillLimiter{seen: make(map[string]struct{}), limit: limit}
}

// bucket returns a bounded, sanitized skill label: "none" for an empty header,
// the sanitized value while under the cardinality cap, else "other".
func (s *skillLimiter) bucket(raw string) string {
	v := sanitizeSkill(raw)
	if v == "none" {
		return v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[v]; ok {
		return v
	}
	if len(s.seen) >= s.limit {
		return "other"
	}
	s.seen[v] = struct{}{}
	return v
}

// sanitizeSkill lowercases, restricts to [a-z0-9_.-], truncates to 64, and maps
// empty → "none". Keeps the label value safe and low-noise.
func sanitizeSkill(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return "none"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "none"
	}
	return out
}

// surfaceForRoute maps a matched chat route to its API surface, or ("", false)
// for non-LLM routes. Suffix-matched so a configured path prefix still resolves.
func surfaceForRoute(route string) (string, bool) {
	switch {
	case strings.HasSuffix(route, "/messages"):
		return "anthropic", true
	case strings.HasSuffix(route, "/chat/completions"):
		return "openai", true
	case strings.HasSuffix(route, "/api/chat"), strings.HasSuffix(route, "/api/generate"):
		return "ollama", true
	default:
		return "", false
	}
}

// New builds the registry: the free Go-runtime + process collectors, the HTTP
// request instruments, gw_build_info, and a pull-collector over the injected
// pool/session sources (called at scrape time — no background goroutine).
//
// info.GatewayID is applied as a CONSTANT label on every series (via
// WrapRegistererWith) so a fleet can group by gateway_id; empty collapses to
// "unknown".
func New(info BuildInfo, pool func() PoolStats, sessions func() SessionStats) *Metrics {
	reg := prometheus.NewRegistry()
	gwID := info.GatewayID
	if gwID == "" {
		gwID = "unknown"
	}
	// Every collector registered through reggw inherits the gateway_id label.
	reggw := prometheus.WrapRegistererWith(prometheus.Labels{"gateway_id": gwID}, reg)
	reggw.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gw_build_info",
		Help: "Gateway build identity; value is always 1. gateway_id is a constant label on all series.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(info.Version, info.Commit).Set(1)

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
		llmTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_llm_requests_total",
			Help: "Total LLM chat requests, by API surface and invoking skill (X-GW-Skill).",
		}, []string{"surface", "skill"}),
		skills: newSkillLimiter(MaxSkillCardinality),
	}
	reggw.MustRegister(buildInfo, m.reqTotal, m.reqDur, m.inFlight, m.llmTotal, newPoolCollector(pool, sessions))
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

		// LLM-call attribution: only for chat routes, tagged by the invoking
		// skill (X-GW-Skill, bounded). Non-LLM routes are not counted here.
		if surface, ok := surfaceForRoute(route); ok {
			m.llmTotal.WithLabelValues(surface, m.skills.bucket(r.Header.Get(SkillHeader))).Inc()
		}
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
