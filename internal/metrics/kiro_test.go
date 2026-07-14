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

// TestMetrics_KiroUsageSeries: the per-turn kiro recorder methods surface
// gw_kiro_credits_total / turns_total / turn_duration_seconds /
// context_usage_percent / mcp_server_init_total, all carrying gateway_id.
func TestMetrics_KiroUsageSeries(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	m.RecordTurnMeter(1.5, 2000)
	m.RecordTurnMeter(0.5, 1000)
	m.RecordContextPct(45.5)
	m.RecordMCPInit("filesystem", true)
	m.RecordMCPInit("broken", false)

	body := scrape(t, m)
	for _, want := range []string{
		`gw_kiro_credits_total{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turns_total{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turn_duration_seconds_count{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turn_duration_seconds_sum{gateway_id="gw-test-123"} 3`,
		`gw_kiro_context_usage_percent_count{gateway_id="gw-test-123"} 1`,
		`gw_kiro_mcp_server_init_total{gateway_id="gw-test-123",result="ok",server="filesystem"} 1`,
		`gw_kiro_mcp_server_init_total{gateway_id="gw-test-123",result="fail",server="broken"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

// TestMetrics_ModelRequests: RecordModelRequest counts by model; an empty model
// (do-not-set-model / "auto") buckets as model="auto".
func TestMetrics_ModelRequests(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	m.RecordModelRequest("claude-sonnet-4-7")
	m.RecordModelRequest("claude-sonnet-4-7")
	m.RecordModelRequest("")

	body := scrape(t, m)
	for _, want := range []string{
		`gw_model_requests_total{gateway_id="gw-test-123",model="claude-sonnet-4-7"} 2`,
		`gw_model_requests_total{gateway_id="gw-test-123",model="auto"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

// TestMetrics_LLMRequests_FlowNameAndClient: gw_llm_requests_total reads
// X-Flow-Name as a skill alias (LangFlow) when X-GW-Skill is absent, and folds
// X-GW-Client into a bounded client label.
func TestMetrics_LLMRequests_FlowNameAndClient(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/api/chat", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil)
	req.Header.Set("X-Flow-Name", "Incident-Summarizer")
	req.Header.Set("X-GW-Client", "langflow")
	r.ServeHTTP(httptest.NewRecorder(), req)

	body := scrape(t, m)
	want := `gw_llm_requests_total{client="langflow",gateway_id="gw-test-123",skill="incident-summarizer",surface="ollama"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("scrape missing %q\n---\n%s", want, body)
	}
}

// TestMetrics_LLMRequests_SkillHeaderWinsOverFlow: X-GW-Skill takes precedence
// over X-Flow-Name when both are present; missing client buckets as "none".
func TestMetrics_LLMRequests_SkillHeaderWinsOverFlow(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Post("/v1/messages", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-GW-Skill", "jira-triage")
	req.Header.Set("X-Flow-Name", "should-be-ignored")
	r.ServeHTTP(httptest.NewRecorder(), req)

	body := scrape(t, m)
	want := `gw_llm_requests_total{client="none",gateway_id="gw-test-123",skill="jira-triage",surface="anthropic"} 1`
	if !strings.Contains(body, want) {
		t.Errorf("scrape missing %q\n---\n%s", want, body)
	}
}

// TestMetrics_SessionsCreatedRecycled: the pull-collector reports the registry's
// created + recycled monotonic counters.
func TestMetrics_SessionsCreatedRecycled(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{Created: 4, Recycled: 2})
	body := scrape(t, m)
	for _, want := range []string{
		`gw_sessions_created_total{gateway_id="gw-test-123"} 4`,
		`gw_sessions_recycled_total{gateway_id="gw-test-123"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}
