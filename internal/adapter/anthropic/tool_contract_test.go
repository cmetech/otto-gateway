package anthropic

import (
	"net/http"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestAnthropicToolContract(t *testing.T) {
	response := &canonical.ChatResponse{
		Model: "selected-model",
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "ordinary response",
		}}},
		StopReason: canonical.StopEndTurn,
	}
	validBody := `{"model":"selected-model","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`

	t.Run("v1 success echoes and reaches canonical request", func(t *testing.T) {
		eng := &fakeEngine{collectResp: response}
		rec := doPostWithHeader(t, newTestAdapter(eng), "/messages", validBody, map[string]string{
			"X-Otto-Tool-Contract": "v1",
			"X-Otto-Call-Role":     "post_tool",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Otto-Tool-Contract"); got != "v1" {
			t.Errorf("echo header = %q, want v1", got)
		}
		if eng.lastReq == nil {
			t.Fatal("engine did not receive request")
		}
		if eng.lastReq.ToolContractVersion != "v1" || eng.lastReq.CallRole != "post_tool" {
			t.Errorf("canonical metadata = {%q %q}, want {v1 post_tool}", eng.lastReq.ToolContractVersion, eng.lastReq.CallRole)
		}
	})

	t.Run("v1 validation error echoes before body", func(t *testing.T) {
		rec := doPostWithHeader(t, newTestAdapter(&fakeEngine{}), "/messages", `{"model":"","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, map[string]string{
			"X-Otto-Tool-Contract": "v1",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Result().Header.Get("X-Otto-Tool-Contract"); got != "v1" {
			t.Errorf("committed echo header = %q, want v1", got)
		}
	})

	t.Run("absence preserves legacy response", func(t *testing.T) {
		rec := doPost(t, newTestAdapter(&fakeEngine{collectResp: response}), "/messages", validBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Otto-Tool-Contract"); got != "" {
			t.Errorf("legacy echo header = %q, want empty", got)
		}
	})

	t.Run("unsupported version fails closed before engine", func(t *testing.T) {
		eng := &fakeEngine{collectResp: response}
		rec := doPostWithHeader(t, newTestAdapter(eng), "/messages", validBody, map[string]string{
			"X-Otto-Tool-Contract": "private-version-canary",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if eng.lastReq != nil {
			t.Fatal("unsupported contract reached engine")
		}
		if got := rec.Header().Get("X-Otto-Error-Code"); got != "unsupported_tool_contract_version" {
			t.Errorf("error code header = %q", got)
		}
		if !strings.Contains(rec.Body.String(), `"type":"invalid_request_error"`) {
			t.Errorf("body = %s, want native invalid_request_error", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "private-version-canary") {
			t.Fatal("error body exposed unsupported header value")
		}
	})
}
