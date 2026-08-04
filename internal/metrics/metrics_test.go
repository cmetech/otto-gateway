package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/metrics"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: want 200, got %d", rec.Code)
	}
	return rec.Body.String()
}

func testMetrics(pool metrics.PoolStats, sess metrics.SessionStats) *metrics.Metrics {
	return metrics.New(
		metrics.BuildInfo{GatewayID: "gw-test-123", Version: "1.2.3", Commit: "deadbee"},
		func() metrics.PoolStats { return pool },
		func() metrics.SessionStats { return sess },
		nil, // worker procs: exercised separately in worker_collector_test.go
	)
}

// TestMetrics_GatewayIDConstantLabel: every series carries the gateway_id
// constant label so a fleet can group by it, and gw_build_info exposes version.
func TestMetrics_GatewayIDConstantLabel(t *testing.T) {
	m := testMetrics(metrics.PoolStats{Size: 1, Alive: 1}, metrics.SessionStats{})
	body := scrape(t, m)
	if !strings.Contains(body, `gw_pool_alive{gateway_id="gw-test-123"} 1`) {
		t.Errorf("gauge missing gateway_id constant label\n%s", body)
	}
	// Free runtime metrics also carry the label (wrapped registerer).
	if !strings.Contains(body, `go_goroutines{gateway_id="gw-test-123"}`) {
		t.Errorf("go_ metric missing gateway_id label\n%s", body)
	}
	if !strings.Contains(body, `gw_build_info{commit="deadbee",gateway_id="gw-test-123",version="1.2.3"} 1`) {
		t.Errorf("gw_build_info missing/incorrect\n%s", body)
	}
}

// TestMetrics_PoolAndSessionGauges: pull-collector reports gauges at scrape.
func TestMetrics_PoolAndSessionGauges(t *testing.T) {
	m := testMetrics(
		metrics.PoolStats{Size: 4, Alive: 3, Busy: 1, Healthy: true},
		metrics.SessionStats{Active: 2},
	)
	body := scrape(t, m)
	for _, want := range []string{
		`gw_pool_size{gateway_id="gw-test-123"} 4`,
		`gw_pool_alive{gateway_id="gw-test-123"} 3`,
		`gw_pool_busy{gateway_id="gw-test-123"} 1`,
		`gw_pool_healthy{gateway_id="gw-test-123"} 1`,
		`gw_sessions_active{gateway_id="gw-test-123"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

// TestMetrics_EventCounters (Track 4b): respawns, scheduled recycles, ping
// escalations/suspend-skips, session reaps surface as monotonic counters.
func TestMetrics_EventCounters(t *testing.T) {
	m := testMetrics(
		metrics.PoolStats{SlotRespawns: 5, SlotRecycles: 3, PingEscalations: 2, PingSuspendSkips: 7},
		metrics.SessionStats{Reaped: 3},
	)
	body := scrape(t, m)
	for _, want := range []string{
		`gw_pool_slot_respawns_total{gateway_id="gw-test-123"} 5`,
		`gw_pool_slot_recycles_total{gateway_id="gw-test-123"} 3`,
		`gw_acp_ping_escalations_total{gateway_id="gw-test-123"} 2`,
		`gw_acp_ping_suspend_skips_total{gateway_id="gw-test-123"} 7`,
		`gw_sessions_reaped_total{gateway_id="gw-test-123"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

// TestRecordWorkerRecycle_ReasonAndIdleHistograms proves successful recycle
// events retain only the two approved reason labels, and that the idle-memory
// trigger measurements are not polluted by max-turn or invalid calls.
func TestRecordWorkerRecycle_ReasonAndIdleHistograms(t *testing.T) {
	m := metrics.New(
		metrics.BuildInfo{GatewayID: "gw-test"},
		func() metrics.PoolStats { return metrics.PoolStats{} },
		func() metrics.SessionStats { return metrics.SessionStats{} },
		nil,
	)
	m.RecordWorkerRecycle("max_turns", 0, 0)
	m.RecordWorkerRecycle("idle_memory", 800<<20, 16*time.Minute)
	m.RecordWorkerRecycle("unbounded-input", 1, time.Second)
	body := scrape(t, m)

	for _, want := range []string{
		`gw_pool_slot_recycles_by_reason_total{gateway_id="gw-test",reason="max_turns"} 1`,
		`gw_pool_slot_recycles_by_reason_total{gateway_id="gw-test",reason="idle_memory"} 1`,
		`gw_pool_idle_memory_recycle_trigger_rss_bytes_count{gateway_id="gw-test"} 1`,
		`gw_pool_idle_memory_recycle_trigger_idle_seconds_count{gateway_id="gw-test"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `reason="unbounded-input"`) {
		t.Fatalf("unbounded reason escaped validation\n%s", body)
	}
}

// TestMetrics_FreeRuntimeMetrics: promhttp gives Go-runtime metrics for free.
func TestMetrics_FreeRuntimeMetrics(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	if !strings.Contains(scrape(t, m), "go_goroutines") {
		t.Error("scrape missing free go_goroutines metric")
	}
}

// TestMetrics_Middleware_RecordsRequestWithRoutePattern: request counter labeled
// by chi RoutePattern (bounded cardinality).
func TestMetrics_Middleware_RecordsRequestWithRoutePattern(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/v1/messages", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/messages", nil))

	body := scrape(t, m)
	if !strings.Contains(body, `gw_http_requests_total{gateway_id="gw-test-123",method="GET",route="/v1/messages",status="200"} 1`) {
		t.Errorf("request counter not recorded with RoutePattern label\n%s", body)
	}
	if !strings.Contains(body, "gw_http_request_duration_seconds") {
		t.Errorf("duration histogram not present\n%s", body)
	}
}

// TestMetrics_LLMRequestsBySkill: chat routes record gw_llm_requests_total
// labeled by surface (derived from the route) and skill (from X-GW-Skill,
// sanitized).
func TestMetrics_LLMRequestsBySkill(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/v1/messages", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-GW-Skill", "Jira-Triage")
	r.ServeHTTP(httptest.NewRecorder(), req)

	body := scrape(t, m)
	if !strings.Contains(body, `gw_llm_requests_total{client="none",gateway_id="gw-test-123",skill="jira-triage",surface="anthropic"} 1`) {
		t.Errorf("gw_llm_requests_total not recorded with surface+skill\n%s", body)
	}
}

// TestMetrics_LLMRequests_MissingSkillIsNone: an LLM call with no X-GW-Skill
// header is still counted, bucketed as skill="none".
func TestMetrics_LLMRequests_MissingSkillIsNone(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/api/chat", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil))

	if !strings.Contains(scrape(t, m), `gw_llm_requests_total{client="none",gateway_id="gw-test-123",skill="none",surface="ollama"} 1`) {
		t.Error("missing-skill LLM call should be counted with skill=none, surface=ollama")
	}
}

func TestMetrics_LLMRequests_CompletionsUsesOpenAISurface(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/v1/completions", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/completions", nil))

	if !strings.Contains(scrape(t, m), `gw_llm_requests_total{client="none",gateway_id="gw-test-123",skill="none",surface="openai"} 1`) {
		t.Error("/v1/completions must be counted as an OpenAI LLM request")
	}
}

func TestLLMRequestOutcome_SeriesExposed(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RecordLLMOutcome("openai", "success", "false", "stateless")

	body := scrape(t, m)
	want := `gw_llm_request_outcomes_total{gateway_id="gw-test-123",outcome="success",session_mode="stateless",stream="false",surface="openai"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("LLM application outcome missing:\n%s", body)
	}
}

// TestMetrics_LLMRequests_NonChatRouteNotCounted: non-LLM routes do not emit
// gw_llm_requests_total.
func TestMetrics_LLMRequests_NonChatRouteNotCounted(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil))

	if strings.Contains(scrape(t, m), "gw_llm_requests_total") {
		t.Error("non-chat route must not emit gw_llm_requests_total")
	}
}

// TestMetrics_LLMRequests_CardinalityCap: once the distinct-skill cap is
// exceeded, further skills collapse to "other" (bounds TSDB series).
func TestMetrics_LLMRequests_CardinalityCap(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/v1/messages", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Send more distinct skills than the cap (metrics.MaxSkillCardinality).
	for i := 0; i < metrics.MaxSkillCardinality+5; i++ {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil)
		req.Header.Set("X-GW-Skill", "skill-"+itoa(i))
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	body := scrape(t, m)
	if !strings.Contains(body, `skill="other"`) {
		t.Errorf("skills past the cap must bucket to 'other'\n%s", body)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestMetrics_Middleware_SkipsMetricsPath: scraping /metrics is not counted.
func TestMetrics_Middleware_SkipsMetricsPath(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Handle("/metrics", m.Handler())

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

	if strings.Contains(scrape(t, m), `route="/metrics"`) {
		t.Error("/metrics scrape must not be counted as a request")
	}
}

// TestRegisterCompression_SeriesExposed: the compression counters attach
// post-New via the retained wrapped registerer and read the hook's stats
// closure at scrape time, carrying the gateway_id constant label like
// every other series.
func TestRegisterCompression_SeriesExposed(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RegisterCompression(func() metrics.CompressionStats {
		return metrics.CompressionStats{
			Eligible:        11,
			Runs:            7,
			SavedTokens:     4242,
			BudgetUnmet:     3,
			PanicRecoveries: 2,
		}
	})

	body := scrape(t, m)
	if !strings.Contains(body, `gw_compress_eligible_total{gateway_id="gw-test-123"} 11`) {
		t.Errorf("eligible counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `gw_compress_runs_total{gateway_id="gw-test-123"} 7`) {
		t.Errorf("runs counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `gw_compress_tokens_saved_estimate_total{gateway_id="gw-test-123"} 4242`) {
		t.Errorf("saved-tokens counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `gw_compress_budget_unmet_total{gateway_id="gw-test-123"} 3`) {
		t.Errorf("budget-unmet counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `gw_compress_panic_recoveries_total{gateway_id="gw-test-123"} 2`) {
		t.Errorf("panic-recoveries counter missing/wrong:\n%s", body)
	}
}

// TestRegisterPrivacy_ExactContract catches missing or renamed privacy series,
// wrong collector types, missing fixed labels, and gauges that cache startup
// state instead of pulling a fresh value at scrape time.
func TestRegisterPrivacy_ExactContract(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	stats := metrics.PrivacyStats{
		ScopesActive:       2,
		RequestsInFlight:   3,
		Entries:            5,
		MaxScopes:          128,
		MaxEntriesPerScope: 4096,
		MaxTotalEntries:    32768,
		ScopeTTL:           time.Hour,
		OldestScopeAge:     7 * time.Minute,
		TriageEnabled:      true,
	}
	m.RegisterPrivacy(func() metrics.PrivacyStats { return stats })

	m.RecordPrivacyRequest("strict", "openai", "network-hardening", "pass")
	m.RecordPrivacyTransformation("strict", "IPv4", "pseudonymize")
	m.RecordPrivacyRestoration("strict", "IPv4", "pass")
	m.RecordPrivacyBlock("strict", "output", "residual")
	m.RecordPrivacyResidual("strict", "output", "IPv4")
	m.RecordPrivacyReceipt("strict", "pass")
	m.ObservePrivacyDuration("strict", "transform", 25*time.Millisecond)
	m.RecordPrivacyScopeEvent("created")
	m.RecordPrivacyCapacityRejection("global")
	m.RecordPrivacyMappingOperation("insert", "pass")
	m.RecordPrivacyError("mapping", "internal")
	m.RecordPrivacyTriage("inspect", "pass")

	body := scrape(t, m)
	for _, name := range []string{
		"gw_privacy_requests_total", "gw_privacy_transformations_total",
		"gw_privacy_restorations_total", "gw_privacy_blocks_total",
		"gw_privacy_residual_findings_total", "gw_privacy_receipts_total",
		"gw_privacy_processing_duration_seconds", "gw_privacy_scope_events_total",
		"gw_privacy_capacity_rejections_total", "gw_privacy_mapping_operations_total",
		"gw_privacy_errors_total", "gw_privacy_triage_requests_total",
		"gw_privacy_scopes_active", "gw_privacy_scope_requests_in_flight",
		"gw_privacy_mapping_entries", "gw_privacy_scope_capacity",
		"gw_privacy_mapping_capacity", "gw_privacy_mapping_per_scope_capacity",
		"gw_privacy_scope_ttl_seconds", "gw_privacy_oldest_scope_age_seconds",
		"gw_privacy_triage_enabled",
	} {
		if !strings.Contains(body, "# HELP "+name+" ") || !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("privacy collector %q missing HELP/TYPE metadata\n%s", name, body)
		}
	}
	for _, want := range []string{
		"# TYPE gw_privacy_requests_total counter",
		`gw_privacy_requests_total{gateway_id="gw-test-123",profile="strict",result="pass",surface="openai",workload="network-hardening"} 1`,
		"# TYPE gw_privacy_transformations_total counter",
		"# TYPE gw_privacy_restorations_total counter",
		"# TYPE gw_privacy_blocks_total counter",
		"# TYPE gw_privacy_residual_findings_total counter",
		"# TYPE gw_privacy_receipts_total counter",
		"# TYPE gw_privacy_processing_duration_seconds histogram",
		"# TYPE gw_privacy_scope_events_total counter",
		"# TYPE gw_privacy_capacity_rejections_total counter",
		"# TYPE gw_privacy_mapping_operations_total counter",
		"# TYPE gw_privacy_errors_total counter",
		"# TYPE gw_privacy_triage_requests_total counter",
		`gw_privacy_scopes_active{gateway_id="gw-test-123"} 2`,
		`gw_privacy_scope_requests_in_flight{gateway_id="gw-test-123"} 3`,
		`gw_privacy_mapping_entries{gateway_id="gw-test-123"} 5`,
		`gw_privacy_scope_capacity{gateway_id="gw-test-123"} 128`,
		`gw_privacy_mapping_capacity{gateway_id="gw-test-123"} 32768`,
		`gw_privacy_mapping_per_scope_capacity{gateway_id="gw-test-123"} 4096`,
		`gw_privacy_scope_ttl_seconds{gateway_id="gw-test-123"} 3600`,
		`gw_privacy_oldest_scope_age_seconds{gateway_id="gw-test-123"} 420`,
		`gw_privacy_triage_enabled{gateway_id="gw-test-123"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy scrape missing %q\n%s", want, body)
		}
	}

	stats.ScopesActive = 9
	if body = scrape(t, m); !strings.Contains(body, `gw_privacy_scopes_active{gateway_id="gw-test-123"} 9`) {
		t.Errorf("privacy pull gauge did not refresh from stats source:\n%s", body)
	}
}

// TestPrivacyLabels_MapUnexpectedValuesToOther catches accidental use of
// caller-controlled or failure-detail strings as unbounded metric labels.
func TestPrivacyLabels_MapUnexpectedValuesToOther(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RegisterPrivacy(func() metrics.PrivacyStats { return metrics.PrivacyStats{} })
	canary := "protected-canary-9f24d6"

	m.RecordPrivacyRequest(canary, canary, "known-workload", canary)
	m.RecordPrivacyTransformation(canary, canary, canary)
	m.RecordPrivacyRestoration(canary, canary, canary)
	m.RecordPrivacyBlock(canary, canary, canary)
	m.RecordPrivacyResidual(canary, canary, canary)
	m.RecordPrivacyReceipt(canary, canary)
	m.ObservePrivacyDuration(canary, canary, time.Millisecond)
	m.RecordPrivacyScopeEvent(canary)
	m.RecordPrivacyCapacityRejection(canary)
	m.RecordPrivacyMappingOperation(canary, canary)
	m.RecordPrivacyError(canary, canary)
	m.RecordPrivacyTriage(canary, canary)

	body := scrape(t, m)
	if strings.Contains(body, canary) {
		t.Fatalf("unexpected privacy label leaked into scrape: %s", canary)
	}
	for _, want := range []string{
		`profile="other"`, `surface="other"`, `result="other"`,
		`entity="other"`, `action="other"`, `stage="other"`,
		`reason="other"`, `event="other"`, `resource="other"`,
		`operation="other"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing bounded fallback %q\n%s", want, body)
		}
	}
}

// TestPrivacyWorkload_CardinalityCap catches a separate privacy workload
// limiter being omitted or sharing state with unrelated LLM labels.
func TestPrivacyWorkload_CardinalityCap(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RegisterPrivacy(func() metrics.PrivacyStats { return metrics.PrivacyStats{} })
	for i := 0; i < metrics.MaxSkillCardinality+1; i++ {
		m.RecordPrivacyRequest("strict", "openai", "privacy-workload-"+itoa(i), "pass")
	}
	if body := scrape(t, m); !strings.Contains(body, `workload="other"`) {
		t.Errorf("privacy workloads past the cap must bucket to other:\n%s", body)
	}
}

func TestPoolAcquireDuration_SeriesAndBuckets(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	for _, result := range []string{"immediate", "waited", "timeout", "cancelled", "closed"} {
		m.RecordPoolAcquire(25*time.Millisecond, result)
	}

	body := scrape(t, m)
	for _, result := range []string{"immediate", "waited", "timeout", "cancelled", "closed"} {
		want := `gw_pool_acquire_duration_seconds_count{gateway_id="gw-test-123",result="` + result + `"} 1`
		if !strings.Contains(body, want) {
			t.Errorf("missing acquire result %q:\n%s", result, body)
		}
	}
	for _, bucket := range []string{"0.001", "0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2", "5", "10", "30"} {
		want := `gw_pool_acquire_duration_seconds_bucket{gateway_id="gw-test-123",result="immediate",le="` + bucket + `"}`
		if !strings.Contains(body, want) {
			t.Errorf("missing acquire bucket %s:\n%s", bucket, body)
		}
	}
}
