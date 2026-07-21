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

	"otto-gateway/internal/procstat"

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
// the API shape). Read into the gw_llm_requests_total{surface,skill,client}
// metric. FlowHeader (X-Flow-Name) is a fallback skill alias for LangFlow
// flows, which set the flow name rather than X-GW-Skill.
const (
	SkillHeader  = "X-GW-Skill"
	FlowHeader   = "X-Flow-Name"
	ClientHeader = "X-GW-Client"
)

// MaxSkillCardinality bounds the distinct skill label values (per gateway) so a
// misbehaving or unknown client cannot blow up TSDB series — the (cap+1)th
// distinct skill collapses to "other". The same cap is reused for the client,
// model, and MCP-server labels via independent limiters.
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
	SlotRecycles     uint64 // worker recycling: scheduled respawns (KIRO_WORKER_MAX_TURNS)
	PingEscalations  uint64
	PingSuspendSkips uint64
}

// SessionStats is the metrics-friendly projection of the session registry.
type SessionStats struct {
	Active   int
	Reaped   uint64 // Track 4b monotonic counter
	Created  uint64 // total stateful sessions created (kiro usage-metrics parity)
	Recycled uint64 // total sessions recycled at the context threshold (Track 2)
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
	clients  *skillLimiter

	// Kiro usage (fed by the acp OnTurnMeter/OnContextPct/OnMCPInit hooks via
	// the RecordX methods — kiro usage-metrics parity build).
	kiroCredits prometheus.Counter
	kiroTurns   prometheus.Counter
	kiroTurnDur prometheus.Histogram
	kiroCtxPct  prometheus.Histogram
	mcpInit     *prometheus.CounterVec
	mcpServers  *skillLimiter

	// Attribution: model requested per canonical request (fed by the engine
	// OnModelRequest hook).
	modelReqs *prometheus.CounterVec
	models    *skillLimiter

	// hookReg is the gateway_id-wrapped registerer retained so optional
	// feature series (RegisterCompression) can attach after New.
	hookReg prometheus.Registerer
}

// RecordTurnMeter records one completed kiro turn: increments the turn counter,
// adds the turn's credits (when > 0), observes the turn duration, and — when the
// turn-completion frame reported a context percentage (hasCtxPct) — observes the
// context-usage histogram ONCE for the turn. Observing ctx here (not on every
// streaming frame) keeps the histogram at one sample per completed turn, so its
// avg/p95 describe end-of-turn utilization rather than being dominated by
// mid-turn samples, and keeps per-frame Prometheus work off the ACP read loop.
// Fed by the acp OnTurnMeter hook. Safe for concurrent use.
func (m *Metrics) RecordTurnMeter(credits float64, turnMs int64, ctxPct float64, hasCtxPct bool) {
	m.kiroTurns.Inc()
	if credits > 0 {
		m.kiroCredits.Add(credits)
	}
	m.kiroTurnDur.Observe(float64(turnMs) / 1000)
	if hasCtxPct {
		m.kiroCtxPct.Observe(ctxPct)
	}
}

// RecordMCPInit counts an MCP-server init outcome. Fed by the acp OnMCPInit
// hook; result is "ok" (server_initialized) or "fail" (server_init_failure).
// The server label is cardinality-capped.
func (m *Metrics) RecordMCPInit(server string, ok bool) {
	result := "fail"
	if ok {
		result = "ok"
	}
	m.mcpInit.WithLabelValues(m.mcpServers.bucket(server), result).Inc()
}

// RecordModelRequest counts one LLM request by requested model. Fed by the
// engine OnModelRequest hook; an empty/"auto" model buckets as "auto". The
// model label is cardinality-capped.
func (m *Metrics) RecordModelRequest(model string) {
	m.modelReqs.WithLabelValues(modelBucket(m.models, model)).Inc()
}

// modelBucket normalizes + cardinality-caps the model label. Empty or "auto"
// collapses to "auto" so a do-not-set-model request is still attributable.
func modelBucket(lim *skillLimiter, raw string) string {
	if strings.TrimSpace(raw) == "" || strings.EqualFold(strings.TrimSpace(raw), "auto") {
		return "auto"
	}
	b := lim.bucket(raw)
	if b == "none" {
		return "auto"
	}
	return b
}

// skillLimiter sanitizes + cardinality-caps the X-GW-Skill label value.
type skillLimiter struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	limit int
}

func newSkillLimiter() *skillLimiter {
	return &skillLimiter{seen: make(map[string]struct{}), limit: MaxSkillCardinality}
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
func New(info BuildInfo, pool func() PoolStats, sessions func() SessionStats, workers func() []WorkerProc) *Metrics {
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
		reg:     reg,
		hookReg: reggw,
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
			Help: "Total LLM chat requests, by API surface, invoking skill " +
				"(X-GW-Skill, or X-Flow-Name alias), and client (X-GW-Client).",
		}, []string{"surface", "skill", "client"}),
		skills:  newSkillLimiter(),
		clients: newSkillLimiter(),

		kiroCredits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gw_kiro_credits_total",
			Help: "Total kiro credits consumed (sum of credit-unit meteringUsage across turns).",
		}),
		kiroTurns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gw_kiro_turns_total",
			Help: "Total kiro turns that reported metering (turn-completion metadata frames).",
		}),
		kiroTurnDur: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gw_kiro_turn_duration_seconds",
			Help:    "Kiro turn wall-time in seconds (turnDurationMs/1000).",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300},
		}),
		kiroCtxPct: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gw_kiro_context_usage_percent",
			Help:    "Kiro context-window utilization as a percent (0–100); Grafana derives avg/max/p95.",
			Buckets: []float64{5, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 100},
		}),
		mcpInit: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_kiro_mcp_server_init_total",
			Help: "Total kiro MCP-server init outcomes, by server and result (ok|fail).",
		}, []string{"server", "result"}),
		mcpServers: newSkillLimiter(),

		modelReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_model_requests_total",
			Help: "Total LLM requests by requested model (canonical request Model; empty/auto → auto).",
		}, []string{"model"}),
		models: newSkillLimiter(),
	}
	reggw.MustRegister(
		buildInfo, m.reqTotal, m.reqDur, m.inFlight, m.llmTotal,
		m.kiroCredits, m.kiroTurns, m.kiroTurnDur, m.kiroCtxPct, m.mcpInit, m.modelReqs,
		newPoolCollector(pool, sessions),
	)
	// Per-worker CPU/RSS. Registered through reggw (like every other collector)
	// so the gateway_id constant label rides along. Reads procstat at scrape
	// time; a nil workers closure yields an inert collector (no series).
	if workers != nil {
		reggw.MustRegister(newWorkerCollector(workers, procstat.Read))
	}
	return m
}

// RegisterCompression exposes the CompressionHook counters as pull-style
// CounterFuncs (read at scrape time from the hook's atomics — no
// background goroutine, matching the pool collector posture). Call at
// most once, after New, when the compression feature is wired.
func (m *Metrics) RegisterCompression(stats func() (runs, savedTokens int64)) {
	m.hookReg.MustRegister(
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_runs_total",
			Help: "Requests where CompressionHook reduced the transcript.",
		}, func() float64 { r, _ := stats(); return float64(r) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_tokens_saved_estimate_total",
			Help: "Estimated tokens removed from transcripts (UTF-8 bytes/4 heuristic).",
		}, func() float64 { _, s := stats(); return float64(s) }),
	)
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
		// skill (X-GW-Skill, or the X-Flow-Name LangFlow alias when the former
		// is absent) and the client (X-GW-Client). Both labels are bounded.
		// Non-LLM routes are not counted here.
		if surface, ok := surfaceForRoute(route); ok {
			skill := r.Header.Get(SkillHeader)
			if strings.TrimSpace(skill) == "" {
				skill = r.Header.Get(FlowHeader)
			}
			m.llmTotal.WithLabelValues(
				surface,
				m.skills.bucket(skill),
				m.clients.bucket(r.Header.Get(ClientHeader)),
			).Inc()
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
