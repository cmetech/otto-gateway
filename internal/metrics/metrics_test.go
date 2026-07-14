package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// TestMetrics_EventCounters (Track 4b): respawns, ping escalations/suspend-skips,
// session reaps surface as monotonic counters.
func TestMetrics_EventCounters(t *testing.T) {
	m := testMetrics(
		metrics.PoolStats{SlotRespawns: 5, PingEscalations: 2, PingSuspendSkips: 7},
		metrics.SessionStats{Reaped: 3},
	)
	body := scrape(t, m)
	for _, want := range []string{
		`gw_pool_slot_respawns_total{gateway_id="gw-test-123"} 5`,
		`gw_acp_ping_escalations_total{gateway_id="gw-test-123"} 2`,
		`gw_acp_ping_suspend_skips_total{gateway_id="gw-test-123"} 7`,
		`gw_sessions_reaped_total{gateway_id="gw-test-123"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
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
