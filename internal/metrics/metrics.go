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
	SlotRecycles     uint64 // worker recycling: successful scheduled replacements
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
	reg         *prometheus.Registry
	reqTotal    *prometheus.CounterVec
	reqDur      *prometheus.HistogramVec
	inFlight    prometheus.Gauge
	llmTotal    *prometheus.CounterVec
	llmOutcome  *prometheus.CounterVec
	skills      *skillLimiter
	clients     *skillLimiter
	poolAcquire *prometheus.HistogramVec

	workerRecyclesByReason *prometheus.CounterVec
	idleRecycleRSS         prometheus.Histogram
	idleRecycleIdle        prometheus.Histogram

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
	modelReqs            *prometheus.CounterVec
	toolProtocolFailures *prometheus.CounterVec
	toolProtocolRecovery *prometheus.CounterVec
	models               *skillLimiter

	// hookReg is the gateway_id-wrapped registerer retained so optional
	// feature series (RegisterCompression) can attach after New.
	hookReg prometheus.Registerer

	privacyRequests           *prometheus.CounterVec
	privacyTransformations    *prometheus.CounterVec
	privacyRestorations       *prometheus.CounterVec
	privacyBlocks             *prometheus.CounterVec
	privacyResidualFindings   *prometheus.CounterVec
	privacyReceipts           *prometheus.CounterVec
	privacyDuration           *prometheus.HistogramVec
	privacyScopeEvents        *prometheus.CounterVec
	privacyCapacityRejections *prometheus.CounterVec
	privacyMappingOperations  *prometheus.CounterVec
	privacyErrors             *prometheus.CounterVec
	privacyTriageRequests     *prometheus.CounterVec
	privacyWorkloads          *skillLimiter
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

// RecordToolProtocolEvent records one bounded selected-model protocol outcome
// and returns the exact normalized model label used for its metric series so
// structured logging can reuse it without a second normalization path.
// Empty/unknown reasons do not create failure series; empty/unknown outcomes do
// not create recovery series. The model shares RecordModelRequest's limiter so
// both attribution surfaces have one cardinality budget.
func (m *Metrics) RecordToolProtocolEvent(model, reason, outcome string) string {
	model = modelBucket(m.models, model)
	if validToolProtocolReason(reason) {
		m.toolProtocolFailures.WithLabelValues(model, reason).Inc()
	}
	if validToolProtocolOutcome(outcome) {
		m.toolProtocolRecovery.WithLabelValues(model, outcome).Inc()
	}
	return model
}

func validToolProtocolReason(reason string) bool {
	switch reason {
	case "activation_failed", "required_missing", "named_mismatch", "malformed_wrapper", "capability_refusal", "built_in_tool_denied":
		return true
	default:
		return false
	}
}

func validToolProtocolOutcome(outcome string) bool {
	switch outcome {
	case "first_attempt", "corrected", "failed", "buffer_bypass":
		return true
	default:
		return false
	}
}

// RecordLLMOutcome records the adapter-classified final application outcome
// for one recognized LLM request. Callers provide only the bounded values
// documented by the dashboard contract.
func (m *Metrics) RecordLLMOutcome(surface, outcome, stream, sessionMode string) {
	m.llmOutcome.WithLabelValues(surface, outcome, stream, sessionMode).Inc()
}

// RecordPoolAcquire observes how long a request waited for a pool slot and the
// bounded terminal result: immediate, waited, timeout, cancelled, or closed.
func (m *Metrics) RecordPoolAcquire(duration time.Duration, result string) {
	m.poolAcquire.WithLabelValues(result).Observe(duration.Seconds())
}

// RecordWorkerRecycle records a successful scheduled worker replacement. Only
// pool-owned bounded reasons are accepted so callers cannot create unbounded
// Prometheus label cardinality.
func (m *Metrics) RecordWorkerRecycle(reason string, rssBytes uint64, idle time.Duration) {
	if reason != "max_turns" && reason != "idle_memory" {
		return
	}
	m.workerRecyclesByReason.WithLabelValues(reason).Inc()
	if reason == "idle_memory" {
		m.idleRecycleRSS.Observe(float64(rssBytes))
		m.idleRecycleIdle.Observe(idle.Seconds())
	}
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
	case strings.HasSuffix(route, "/completions"):
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
		llmOutcome: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_llm_request_outcomes_total",
			Help: "Final LLM request outcomes, including failures after streaming HTTP headers commit.",
		}, []string{"surface", "outcome", "stream", "session_mode"}),
		skills:  newSkillLimiter(),
		clients: newSkillLimiter(),
		poolAcquire: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gw_pool_acquire_duration_seconds",
			Help:    "Time spent acquiring a warm pool slot, by terminal result.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30},
		}, []string{"result"}),
		workerRecyclesByReason: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_pool_slot_recycles_by_reason_total",
			Help: "Successful scheduled worker recycles by bounded trigger reason.",
		}, []string{"reason"}),
		idleRecycleRSS: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gw_pool_idle_memory_recycle_trigger_rss_bytes",
			Help:    "Direct worker working set observed at successful idle-memory recycle.",
			Buckets: []float64{256 << 20, 384 << 20, 512 << 20, 768 << 20, 1024 << 20, 1536 << 20, 2048 << 20, 4096 << 20, 8192 << 20},
		}),
		idleRecycleIdle: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gw_pool_idle_memory_recycle_trigger_idle_seconds",
			Help:    "Completed-request idle duration observed at successful idle-memory recycle.",
			Buckets: []float64{60, 300, 900, 1800, 3600, 14400, 43200, 86400},
		}),

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
		toolProtocolFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_selected_model_tool_protocol_failures_total",
			Help: "Selected-model tool-protocol failures by bounded model and reason.",
		}, []string{"model", "reason"}),
		toolProtocolRecovery: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gw_selected_model_tool_protocol_recovery_total",
			Help: "Selected-model tool-protocol outcomes by bounded model and outcome.",
		}, []string{"model", "outcome"}),
		models: newSkillLimiter(),
	}
	reggw.MustRegister(
		buildInfo, m.reqTotal, m.reqDur, m.inFlight, m.llmTotal, m.llmOutcome, m.poolAcquire,
		m.workerRecyclesByReason, m.idleRecycleRSS, m.idleRecycleIdle,
		m.kiroCredits, m.kiroTurns, m.kiroTurnDur, m.kiroCtxPct, m.mcpInit, m.modelReqs,
		m.toolProtocolFailures, m.toolProtocolRecovery,
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

// CompressionStats is the bounded lifetime snapshot exposed by the
// CompressionHook. It lives in metrics so feature packages remain independent
// from this package and the command can bridge between the two.
type CompressionStats struct {
	Eligible        int64
	Runs            int64
	SavedTokens     int64
	BudgetUnmet     int64
	PanicRecoveries int64
}

// RegisterCompression exposes the CompressionHook counters as pull-style
// CounterFuncs (read at scrape time from the hook's atomics — no background
// goroutine, matching the pool collector posture). Call at most once, after
// New, when the compression feature is wired.
func (m *Metrics) RegisterCompression(stats func() CompressionStats) {
	m.hookReg.MustRegister(
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_eligible_total",
			Help: "Compression-eligible requests whose estimated size reached the trigger and exceeded the budget.",
		}, func() float64 { return float64(stats().Eligible) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_runs_total",
			Help: "Requests where CompressionHook reduced the transcript.",
		}, func() float64 { return float64(stats().Runs) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_tokens_saved_estimate_total",
			Help: "Estimated tokens removed from transcripts (UTF-8 bytes/4 heuristic).",
		}, func() float64 { return float64(stats().SavedTokens) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_budget_unmet_total",
			Help: "Compression-eligible requests that remained above the configured token budget.",
		}, func() float64 { return float64(stats().BudgetUnmet) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_panic_recoveries_total",
			Help: "Panics recovered inside CompressionHook so requests could continue.",
		}, func() float64 { return float64(stats().PanicRecoveries) }),
	)
}

// PrivacyStats is the bounded, value-free privacy posture read at scrape time.
type PrivacyStats struct {
	ScopesActive       int
	RequestsInFlight   int
	Entries            int
	MaxScopes          int
	MaxEntriesPerScope int
	MaxTotalEntries    int
	ScopeTTL           time.Duration
	OldestScopeAge     time.Duration
	TriageEnabled      bool
}

// RegisterPrivacy attaches privacy event collectors and pull gauges to the
// existing Gateway registry. Call once after New.
func (m *Metrics) RegisterPrivacy(stats func() PrivacyStats) {
	m.privacyRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_requests_total",
		Help: "Privacy-boundary request outcomes by bounded profile, surface, workload, and result.",
	}, []string{"profile", "surface", "workload", "result"})
	m.privacyTransformations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_transformations_total",
		Help: "Privacy transformations by bounded profile, entity, and action.",
	}, []string{"profile", "entity", "action"})
	m.privacyRestorations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_restorations_total",
		Help: "Privacy restoration outcomes by bounded profile and entity.",
	}, []string{"profile", "entity", "result"})
	m.privacyBlocks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_blocks_total",
		Help: "Privacy fail-closed blocks by bounded profile, stage, and reason.",
	}, []string{"profile", "stage", "reason"})
	m.privacyResidualFindings = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_residual_findings_total",
		Help: "Privacy residual-scan findings by bounded profile, stage, and entity.",
	}, []string{"profile", "stage", "entity"})
	m.privacyReceipts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_receipts_total",
		Help: "Privacy receipt outcomes by bounded profile and result.",
	}, []string{"profile", "result"})
	m.privacyDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gw_privacy_processing_duration_seconds",
		Help:    "Privacy processing duration in seconds by bounded profile and stage.",
		Buckets: prometheus.DefBuckets,
	}, []string{"profile", "stage"})
	m.privacyScopeEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_scope_events_total",
		Help: "Privacy scope lifecycle events by bounded event.",
	}, []string{"event"})
	m.privacyCapacityRejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_capacity_rejections_total",
		Help: "Privacy capacity rejections by bounded resource.",
	}, []string{"resource"})
	m.privacyMappingOperations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_mapping_operations_total",
		Help: "Privacy mapping operations by bounded operation and result.",
	}, []string{"operation", "result"})
	m.privacyErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_errors_total",
		Help: "Privacy internal errors by bounded stage and reason.",
	}, []string{"stage", "reason"})
	m.privacyTriageRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_privacy_triage_requests_total",
		Help: "Privacy triage operations by bounded operation and result.",
	}, []string{"operation", "result"})
	m.privacyWorkloads = newSkillLimiter()

	m.hookReg.MustRegister(
		m.privacyRequests,
		m.privacyTransformations,
		m.privacyRestorations,
		m.privacyBlocks,
		m.privacyResidualFindings,
		m.privacyReceipts,
		m.privacyDuration,
		m.privacyScopeEvents,
		m.privacyCapacityRejections,
		m.privacyMappingOperations,
		m.privacyErrors,
		m.privacyTriageRequests,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_scopes_active", Help: "Current retained privacy scopes."}, func() float64 { return float64(stats().ScopesActive) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_scope_requests_in_flight", Help: "Current requests holding privacy scope references."}, func() float64 { return float64(stats().RequestsInFlight) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_mapping_entries", Help: "Current reversible privacy mapping entries."}, func() float64 { return float64(stats().Entries) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_scope_capacity", Help: "Configured maximum retained privacy scopes."}, func() float64 { return float64(stats().MaxScopes) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_mapping_capacity", Help: "Configured global reversible privacy mapping capacity."}, func() float64 { return float64(stats().MaxTotalEntries) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_mapping_per_scope_capacity", Help: "Configured reversible privacy mapping capacity per scope."}, func() float64 { return float64(stats().MaxEntriesPerScope) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_scope_ttl_seconds", Help: "Configured privacy scope idle TTL in seconds."}, func() float64 { return stats().ScopeTTL.Seconds() }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_oldest_scope_age_seconds", Help: "Age in seconds of the oldest retained privacy scope."}, func() float64 { return stats().OldestScopeAge.Seconds() }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gw_privacy_triage_enabled", Help: "Whether break-glass privacy triage is enabled (1 or 0)."}, func() float64 {
			if stats().TriageEnabled {
				return 1
			}
			return 0
		}),
	)
}

// RecordPrivacyRequest records the outcome of one privacy-aware request.
func (m *Metrics) RecordPrivacyRequest(profile, surface, workload, result string) {
	if m.privacyRequests != nil {
		m.privacyRequests.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(surface, privacySurfaces),
			m.privacyWorkloads.bucket(workload),
			privacyLabel(result, privacyResults),
		).Inc()
	}
}

// RecordPrivacyTransformation records a privacy transformation by entity and action.
func (m *Metrics) RecordPrivacyTransformation(profile, entity, action string) {
	if m.privacyTransformations != nil {
		m.privacyTransformations.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(entity, privacyEntities),
			privacyLabel(action, privacyActions),
		).Inc()
	}
}

// RecordPrivacyRestoration records the outcome of restoring a transformed entity.
func (m *Metrics) RecordPrivacyRestoration(profile, entity, result string) {
	if m.privacyRestorations != nil {
		m.privacyRestorations.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(entity, privacyEntities),
			privacyLabel(result, privacyResults),
		).Inc()
	}
}

// RecordPrivacyBlock records a request blocked at a privacy processing stage.
func (m *Metrics) RecordPrivacyBlock(profile, stage, reason string) {
	if m.privacyBlocks != nil {
		m.privacyBlocks.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(stage, privacyStages),
			privacyLabel(reason, privacyReasons),
		).Inc()
	}
}

// RecordPrivacyResidual records a residual sensitive entity found after processing.
func (m *Metrics) RecordPrivacyResidual(profile, stage, entity string) {
	if m.privacyResidualFindings != nil {
		m.privacyResidualFindings.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(stage, privacyStages),
			privacyLabel(entity, privacyEntities),
		).Inc()
	}
}

// RecordPrivacyReceipt records the outcome represented by a privacy receipt.
func (m *Metrics) RecordPrivacyReceipt(profile, result string) {
	if m.privacyReceipts != nil {
		m.privacyReceipts.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(result, privacyResults),
		).Inc()
	}
}

// ObservePrivacyDuration records the elapsed time for a privacy processing stage.
func (m *Metrics) ObservePrivacyDuration(profile, stage string, elapsed time.Duration) {
	if m.privacyDuration != nil {
		m.privacyDuration.WithLabelValues(
			privacyLabel(profile, privacyProfiles),
			privacyLabel(stage, privacyStages),
		).Observe(elapsed.Seconds())
	}
}

// RecordPrivacyScopeEvent records a privacy scope lifecycle event.
func (m *Metrics) RecordPrivacyScopeEvent(event string) {
	if m.privacyScopeEvents != nil {
		m.privacyScopeEvents.WithLabelValues(privacyLabel(event, privacyScopeEvents)).Inc()
	}
}

// RecordPrivacyCapacityRejection records a rejected privacy resource allocation.
func (m *Metrics) RecordPrivacyCapacityRejection(resource string) {
	if m.privacyCapacityRejections != nil {
		m.privacyCapacityRejections.WithLabelValues(privacyLabel(resource, privacyResources)).Inc()
	}
}

// RecordPrivacyMappingOperation records the outcome of a privacy mapping operation.
func (m *Metrics) RecordPrivacyMappingOperation(operation, result string) {
	if m.privacyMappingOperations != nil {
		m.privacyMappingOperations.WithLabelValues(
			privacyLabel(operation, privacyMappingOperations),
			privacyLabel(result, privacyResults),
		).Inc()
	}
}

// RecordPrivacyError records a privacy processing error by stage and reason.
func (m *Metrics) RecordPrivacyError(stage, reason string) {
	if m.privacyErrors != nil {
		m.privacyErrors.WithLabelValues(
			privacyLabel(stage, privacyStages),
			privacyLabel(reason, privacyReasons),
		).Inc()
	}
}

// RecordPrivacyTriage records the outcome of a privacy triage operation.
func (m *Metrics) RecordPrivacyTriage(operation, result string) {
	if m.privacyTriageRequests != nil {
		m.privacyTriageRequests.WithLabelValues(
			privacyLabel(operation, privacyTriageOperations),
			privacyLabel(result, privacyResults),
		).Inc()
	}
}

func privacyLabel(value string, allowed map[string]struct{}) string {
	if _, ok := allowed[value]; ok {
		return value
	}
	return "other"
}

func privacyValues(values ...string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		allowed[value] = struct{}{}
	}
	return allowed
}

var (
	privacyProfiles          = privacyValues("standard", "strict")
	privacySurfaces          = privacyValues("ollama", "openai", "anthropic")
	privacyActions           = privacyValues("replace", "mask", "hash", "drop", "encrypt", "pseudonymize")
	privacyStages            = privacyValues("profile", "scope", "input", "output", "classify", "transform", "verify", "restore", "mapping", "encrypt", "token", "action", "receipt", "triage")
	privacyReasons           = privacyValues("token", "residual", "secret", "original", "alias", "integrity", "internal", "panic", "capacity", "closed", "invalid", "unavailable")
	privacyResults           = privacyValues("pass", "block", "error", "hit", "miss", "insert", "restore", "collision", "rejected", "denied", "failed", "completed", "closing")
	privacyScopeEvents       = privacyValues("created", "expired", "closed", "cleared")
	privacyResources         = privacyValues("scope", "per_scope", "global")
	privacyMappingOperations = privacyValues("lookup", "insert", "restore", "collision")
	privacyTriageOperations  = privacyValues("status", "list", "inspect", "clear", "clear_all")
	privacyEntities          = privacyValues(
		"Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone", "SIP_URI",
		"IMEI", "IMSI", "MSISDN", "MAC_ADDRESS", "COORDINATES", "SITE",
		"USAddress", "USState", "USZIP", "PERSON", "LOCATION",
		"PRIVATE_KEY", "PROXY_AUTHORIZATION", "AUTHORIZATION", "GITHUB_TOKEN",
		"GITLAB_TOKEN", "OPENAI_API_KEY", "CREDENTIAL_URL", "PASSPHRASE",
		"PASSWORD", "AWS_SECRET_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "CLIENT_SECRET",
		"REFRESH_TOKEN", "OAUTH_TOKEN", "ACCESS_TOKEN", "API_KEY", "SECRET",
	)
)

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
