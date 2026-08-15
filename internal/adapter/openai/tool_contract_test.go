package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

func TestOpenAIToolContract(t *testing.T) {
	newRouter := func(eng *fakeEngine) http.Handler {
		t.Helper()
		a := newFakeAdapter(eng)
		router := chi.NewRouter()
		router.Route("/v1", func(r chi.Router) { a.RegisterRoutes(r) })
		return router
	}
	post := func(t *testing.T, handler http.Handler, contract, role, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if contract != "" {
			req.Header.Set("X-Otto-Tool-Contract", contract)
		}
		if role != "" {
			req.Header.Set("X-Otto-Call-Role", role)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	response := &canonical.ChatResponse{
		Model: "selected-model",
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "ordinary response",
		}}},
		StopReason: canonical.StopEndTurn,
	}

	t.Run("v1 success echoes and reaches canonical request", func(t *testing.T) {
		eng := &fakeEngine{collectResp: response}
		rec := post(t, newRouter(eng), "v1", "post_tool", `{"model":"selected-model","messages":[{"role":"user","content":"hello"}]}`)
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
		rec := post(t, newRouter(&fakeEngine{}), "v1", "primary", `{"model":"selected-model","messages":[]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Result().Header.Get("X-Otto-Tool-Contract"); got != "v1" {
			t.Errorf("committed echo header = %q, want v1", got)
		}
	})

	t.Run("absence preserves legacy response", func(t *testing.T) {
		eng := &fakeEngine{collectResp: response}
		rec := post(t, newRouter(eng), "", "", `{"model":"selected-model","messages":[{"role":"user","content":"hello"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Otto-Tool-Contract"); got != "" {
			t.Errorf("legacy echo header = %q, want empty", got)
		}
	})

	t.Run("unsupported version fails closed before engine", func(t *testing.T) {
		eng := &fakeEngine{collectResp: response}
		rec := post(t, newRouter(eng), "private-version-canary", "primary", `{"model":"selected-model","messages":[{"role":"user","content":"hello"}]}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if eng.lastReq != nil {
			t.Fatal("unsupported contract reached engine")
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode error envelope: %v", err)
		}
		if envelope.Error.Code == nil || *envelope.Error.Code != canonical.CodeUnsupportedToolContractVersion {
			t.Errorf("error code = %v, want unsupported_tool_contract_version", envelope.Error.Code)
		}
		if strings.Contains(rec.Body.String(), "private-version-canary") {
			t.Fatal("error body exposed unsupported header value")
		}
	})
}
