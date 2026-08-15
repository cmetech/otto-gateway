package metrics_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/metrics"
)

// TestMetrics_KiroUsageSeries: the per-turn kiro recorder methods surface
// gw_kiro_credits_total / turns_total / turn_duration_seconds /
// context_usage_percent / mcp_server_init_total, all carrying gateway_id. The
// context histogram is observed ONCE PER COMPLETED TURN (via RecordTurnMeter's
// end-of-turn ctx), not per streaming frame — so a turn with no ctx does not
// observe it.
func TestMetrics_KiroUsageSeries(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	m.RecordTurnMeter(1.5, 2000, 45.5, true) // turn WITH end-of-turn ctx
	m.RecordTurnMeter(0.5, 1000, 0, false)   // turn with no ctx reported
	m.RecordMCPInit("filesystem", true)
	m.RecordMCPInit("broken", false)

	body := scrape(t, m)
	for _, want := range []string{
		`gw_kiro_credits_total{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turns_total{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turn_duration_seconds_count{gateway_id="gw-test-123"} 2`,
		`gw_kiro_turn_duration_seconds_sum{gateway_id="gw-test-123"} 3`,
		// Only the first turn carried ctx → count 1, sum 45.5.
		`gw_kiro_context_usage_percent_count{gateway_id="gw-test-123"} 1`,
		`gw_kiro_context_usage_percent_sum{gateway_id="gw-test-123"} 45.5`,
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

func TestMetrics_ToolProtocolEventsNormalizeModelsAndRejectUnknownEnums(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})

	m.RecordToolProtocolEvent("", "activation_failed", "failed")
	m.RecordToolProtocolEvent("auto", "", "first_attempt")
	m.RecordToolProtocolEvent("claude-sonnet-4-7", "required_missing", "corrected")
	m.RecordToolProtocolEvent("sensitive-model-output", "secret-prompt-marker", "secret-arguments-marker")

	body := scrape(t, m)
	for _, want := range []string{
		`gw_selected_model_tool_protocol_failures_total{gateway_id="gw-test-123",model="auto",reason="activation_failed"} 1`,
		`gw_selected_model_tool_protocol_failures_total{gateway_id="gw-test-123",model="claude-sonnet-4-7",reason="required_missing"} 1`,
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="auto",outcome="first_attempt"} 1`,
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="auto",outcome="failed"} 1`,
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="claude-sonnet-4-7",outcome="corrected"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}

	protocolLines := toolProtocolSampleLines(body)
	for _, forbidden := range []string{
		`reason="secret-prompt-marker"`,
		`outcome="secret-arguments-marker"`,
		`output=`,
		`prompt=`,
		`arguments=`,
		`session_id=`,
	} {
		if strings.Contains(protocolLines, forbidden) {
			t.Errorf("tool-protocol samples expose forbidden or unbounded label %q:\n%s", forbidden, protocolLines)
		}
	}
}

func TestMetrics_ToolProtocolEventsAcceptOnlyClosedReasonsAndOutcomes(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	reasons := []string{
		"activation_failed",
		"required_missing",
		"named_mismatch",
		"malformed_wrapper",
		"capability_refusal",
		"built_in_tool_denied",
		"embedded_dispatcher_wrapper",
		"tool_result_provenance_refusal",
	}
	outcomes := []string{"first_attempt", "corrected", "failed", "buffer_bypass", "fallback_first_attempt"}

	for _, reason := range reasons {
		m.RecordToolProtocolEvent("known-model", reason, "")
	}
	for _, outcome := range outcomes {
		m.RecordToolProtocolEvent("known-model", "", outcome)
	}
	m.RecordToolProtocolEvent("known-model", "unknown_reason", "unknown_outcome")

	body := scrape(t, m)
	for _, reason := range reasons {
		want := fmt.Sprintf(`gw_selected_model_tool_protocol_failures_total{gateway_id="gw-test-123",model="known-model",reason=%q} 1`, reason)
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing allowed reason sample %q", want)
		}
	}
	for _, outcome := range outcomes {
		want := fmt.Sprintf(`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="known-model",outcome=%q} 1`, outcome)
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing allowed outcome sample %q", want)
		}
	}
	if strings.Contains(body, "unknown_reason") || strings.Contains(body, "unknown_outcome") {
		t.Fatalf("unknown tool-protocol enums created a series:\n%s", toolProtocolSampleLines(body))
	}
}

func TestMetrics_ToolProtocolEventsReuseBoundedModelLimiter(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RecordToolProtocolEvent("known-model", "", "first_attempt")
	for i := 0; i <= metrics.MaxSkillCardinality; i++ {
		m.RecordToolProtocolEvent(fmt.Sprintf("custom-model-%02d", i), "", "first_attempt")
	}

	body := scrape(t, m)
	for _, want := range []string{
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="known-model",outcome="first_attempt"} 1`,
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="custom-model-62",outcome="first_attempt"} 1`,
		`gw_selected_model_tool_protocol_recovery_total{gateway_id="gw-test-123",model="other",outcome="first_attempt"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing bounded-cardinality sample %q", want)
		}
	}
	if strings.Contains(body, `model="custom-model-63"`) || strings.Contains(body, `model="custom-model-64"`) {
		t.Fatalf("models beyond shared cardinality cap did not collapse to other:\n%s", toolProtocolSampleLines(body))
	}
}

func toolProtocolSampleLines(body string) string {
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "gw_selected_model_tool_protocol_") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
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
