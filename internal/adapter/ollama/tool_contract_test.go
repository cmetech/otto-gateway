package ollama

import (
	"net/http"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestOllamaToolContract(t *testing.T) {
	response := &canonical.ChatResponse{
		Model: "selected-model",
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "ordinary response",
		}}},
		StopReason: canonical.StopEndTurn,
	}
	validBody := `{"model":"selected-model","messages":[{"role":"user","content":"hello"}],"stream":false}`

	t.Run("v1 success echoes and reaches canonical request", func(t *testing.T) {
		eng := &fakeEngine{resp: response}
		rec := doPostWithHeaders(t, newTestAdapter(eng, nil), "/chat", validBody, http.Header{
			"X-Otto-Tool-Contract": []string{"v1"},
			"X-Otto-Call-Role":     []string{"post_tool"},
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
		rec := doPostWithHeaders(t, newTestAdapter(&fakeEngine{}, nil), "/chat", `{"model":"selected-model","messages":[]}`, http.Header{
			"X-Otto-Tool-Contract": []string{"v1"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Result().Header.Get("X-Otto-Tool-Contract"); got != "v1" {
			t.Errorf("committed echo header = %q, want v1", got)
		}
	})

	t.Run("absence preserves legacy response", func(t *testing.T) {
		rec := doPost(t, newTestAdapter(&fakeEngine{resp: response}, nil), "/chat", validBody)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Otto-Tool-Contract"); got != "" {
			t.Errorf("legacy echo header = %q, want empty", got)
		}
	})

	t.Run("unsupported version fails closed before engine", func(t *testing.T) {
		eng := &fakeEngine{resp: response}
		rec := doPostWithHeaders(t, newTestAdapter(eng, nil), "/chat", validBody, http.Header{
			"X-Otto-Tool-Contract": []string{"private-version-canary"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if eng.lastReq != nil {
			t.Fatal("unsupported contract reached engine")
		}
		if got := rec.Header().Get("X-Otto-Error-Code"); got != canonical.CodeUnsupportedToolContractVersion {
			t.Errorf("error code header = %q", got)
		}
		if strings.Contains(rec.Body.String(), "private-version-canary") {
			t.Fatal("error body exposed unsupported header value")
		}
	})
}

func TestOllamaV1ToolChoice(t *testing.T) {
	response := &canonical.ChatResponse{
		Model: "selected-model",
		Message: canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "ordinary response",
		}}},
		StopReason: canonical.StopEndTurn,
	}
	tests := []struct {
		name     string
		choice   string
		wantType string
		wantName string
	}{
		{name: "auto", choice: `"auto"`, wantType: "auto"},
		{name: "required", choice: `"required"`, wantType: "required"},
		{name: "none", choice: `"none"`, wantType: "none"},
		{name: "named function", choice: `{"type":"function","function":{"name":"lookup_item"}}`, wantType: "function", wantName: "lookup_item"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := &fakeEngine{resp: response}
			body := `{"model":"selected-model","messages":[{"role":"user","content":"hello"}],"stream":false,"tool_choice":` + tt.choice + `}`
			rec := doPostWithHeaders(t, newTestAdapter(eng, nil), "/chat", body, http.Header{
				"X-Otto-Tool-Contract": []string{"v1"},
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if eng.lastReq == nil || eng.lastReq.ToolChoice == nil {
				t.Fatal("canonical tool choice was not populated")
			}
			if got := eng.lastReq.ToolChoice; got.Type != tt.wantType || got.Name != tt.wantName {
				t.Errorf("ToolChoice = %+v, want {Type:%q Name:%q}", got, tt.wantType, tt.wantName)
			}
		})
	}

	t.Run("unknown v1 shape fails before engine", func(t *testing.T) {
		eng := &fakeEngine{resp: response}
		rec := doPostWithHeaders(t, newTestAdapter(eng, nil), "/chat", `{"model":"selected-model","messages":[{"role":"user","content":"hello"}],"stream":false,"tool_choice":42}`, http.Header{
			"X-Otto-Tool-Contract": []string{"v1"},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if eng.lastReq != nil {
			t.Fatal("invalid v1 tool choice reached engine")
		}
	})

	for _, tc := range []struct {
		name   string
		choice string
	}{
		{name: "required", choice: `"required"`},
		{name: "named", choice: `{"type":"function","function":{"name":"lookup_item"}}`},
	} {
		t.Run("generate rejects "+tc.name+" before engine", func(t *testing.T) {
			eng := &fakeEngine{resp: response}
			body := `{"model":"selected-model","prompt":"hello","stream":false,"tool_choice":` + tc.choice + `}`
			rec := doPostWithHeaders(t, newTestAdapter(eng, nil), "/generate", body, http.Header{
				"X-Otto-Tool-Contract": []string{"v1"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if eng.lastReq != nil {
				t.Fatal("mandatory generate tool choice reached engine")
			}
			if got := rec.Header().Get("X-Otto-Error-Code"); got != canonical.CodeMandatoryToolChoiceNotSupported {
				t.Errorf("error code header = %q", got)
			}
			if got := rec.Header().Get("X-Otto-Tool-Contract"); got != "v1" {
				t.Errorf("echo header = %q, want v1", got)
			}
		})
	}
}
