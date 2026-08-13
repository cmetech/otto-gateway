// Package openai — T-5b adapter handler re-route test.
//
// When a Pre hook (e.g., the PII encrypt Pre hook) flips req.Stream=false
// during eng.Run, handleChatCompletions must abandon the SSE branch and
// route the already-running ACP session through eng.CollectFromRun,
// rendering via chatResponseToCompletion (the OpenAI non-streaming
// response shape).
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

// rerouteFakeEngine simulates a PII-encrypt-style Pre hook by flipping
// req.Stream=false inside its Run method, BEFORE returning the
// RunHandle. The handler's post-Run req.Stream check then takes the
// CollectFromRun re-route branch instead of runSSEEmitter.
type rerouteFakeEngine struct {
	collectFromRunResp *canonical.ChatResponse
	collectFromRunErr  error

	collectFromRunCalled bool
	sawStreamTrueAtRun   bool
	// postHookCalls tracks RunPostHooks invocations. Audit
	// ollama-reroute-double-posthook-fires applies symmetrically here.
	postHookCalls int
}

func (e *rerouteFakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, nil
}

func (e *rerouteFakeEngine) Run(_ context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	if req.Stream {
		e.sawStreamTrueAtRun = true
	}
	req.Stream = false
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "irrelevant"}}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_reroute",
	}, nil
}

func (e *rerouteFakeEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	e.postHookCalls++
	return nil
}

func (e *rerouteFakeEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	e.collectFromRunCalled = true
	if e.collectFromRunErr != nil {
		return nil, e.collectFromRunErr
	}
	return e.collectFromRunResp, nil
}

// TestHandleChatCompletions_StreamReroute_OnPreHookStreamDisable asserts
// that a Pre hook flipping req.Stream=false during eng.Run causes the
// /v1/chat/completions handler to call eng.CollectFromRun (NOT
// runSSEEmitter) and render the OpenAI non-streaming chat.completion JSON
// shape. Closes the PII encrypt round-trip for streaming OpenAI clients
// (Pi-SDK).
func TestHandleChatCompletions_StreamReroute_OnPreHookStreamDisable(t *testing.T) {
	eng := &rerouteFakeEngine{
		collectFromRunResp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "decrypted-response"},
				},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
	a := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		Engine: eng,
	})

	// Mount the adapter under /v1 (Plan 03-01 D-01 SurfaceMount).
	root := chi.NewRouter()
	root.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want prefix text/event-stream (synthetic SSE)", ct)
	}

	bodyStr := w.Body.String()
	// Synthetic SSE must emit role-marker frame, content frame with the
	// decrypted text, final finish_reason frame, and [DONE] terminator.
	wantSubstrings := []string{
		`"role":"assistant"`,
		`"content":"decrypted-response"`,
		`"finish_reason":"stop"`,
		"data: [DONE]",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(bodyStr, s) {
			t.Errorf("body missing expected substring %q; body=%q", s, bodyStr)
		}
	}
	// Every data line must carry a chat.completion.chunk object.
	if !strings.Contains(bodyStr, `"object":"chat.completion.chunk"`) {
		t.Errorf("body missing chat.completion.chunk object marker; body=%q", bodyStr)
	}

	if !eng.collectFromRunCalled {
		t.Error("CollectFromRun was not called — handler took the real-SSE branch (regression: T-5b re-route guard missing)")
	}
	if !eng.sawStreamTrueAtRun {
		t.Error("rerouteFakeEngine.Run did not observe Stream=true on inbound req — wire-decode broken")
	}
	// Audit ollama-reroute-double-posthook-fires (applies symmetrically
	// to openai's synthetic-SSE re-route): handler MUST NOT call
	// RunPostHooks — CollectFromRun already ran the chain.
	if eng.postHookCalls != 0 {
		t.Errorf("handler called RunPostHooks %d times on synthetic-SSE re-route; want 0", eng.postHookCalls)
	}
}

func TestSelectedModelError_StreamRerouteCollectFromRunPrecedesSSEHeaders(t *testing.T) {
	tests := []struct {
		code    string
		message string
	}{
		{
			code:    canonical.CodeSelectedModelActivationFailed,
			message: "The selected model could not be activated. Retry the request with model `auto`.",
		},
		{
			code:    canonical.CodeSelectedModelToolProtocolFailed,
			message: "The selected model did not produce a valid external tool call after one corrective attempt. Retry the request with model `auto`.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			eng := &rerouteFakeEngine{collectFromRunErr: &canonical.SelectedModelError{
				Code:  tc.code,
				Cause: errors.New("raw-cause-canary partial-assistant refusal tool-args schema-secret"),
			}}
			rec := doOpenAIPost(t, eng, "/chat/completions",
				`{"model":"chosen-model","messages":[{"role":"user","content":"hi"}],"stream":true}`, nil)

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status=%d, want 502; body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type=%q, want application/json before SSE", got)
			}
			want := `{"error":{"message":` + mustJSONQuote(t, tc.message) + `,"type":"api_error","param":null,"code":` + mustJSONQuote(t, tc.code) + `}}` + "\n"
			if got := rec.Body.String(); got != want {
				t.Fatalf("body=%q, want %q", got, want)
			}
			for _, forbidden := range []string{"data:", "partial-assistant", "refusal", "raw-cause-canary", "tool-args", "schema-secret"} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatalf("body contains forbidden %q: %s", forbidden, rec.Body.String())
				}
			}
		})
	}
}

func doOpenAIPost(t *testing.T, eng Engine, path, body string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	a := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		Engine: eng,
	})
	root := chi.NewRouter()
	root.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, req)
	return rec
}

func decodeOpenAIPrivacyReceipt(t *testing.T, value string) privacy.Receipt {
	t.Helper()
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode privacy receipt: %v", err)
	}
	var receipt privacy.Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("unmarshal privacy receipt: %v", err)
	}
	return receipt
}

func TestOpenAIPrivacy_StampsBoundedMetadataBeforeEngineDispatch(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/chat/completions", body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{name: "completions", path: "/completions", body: `{"model":"auto","prompt":"hi","stream":false}`},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			eng := &fakeEngine{collectResp: &canonical.ChatResponse{
				Message:    canonical.Message{Role: canonical.RoleAssistant},
				StopReason: canonical.StopEndTurn,
			}}
			headers := http.Header{
				"X-GW-Privacy-Profile": {"strict"},
				"X-GW-Privacy-Scope":   {"run-7f29b4d4"},
				"X-GW-Skill":           {strings.Repeat("w", 80)},
			}
			rec := doOpenAIPost(t, eng, endpoint.path, endpoint.body, headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			state, ok := privacy.StateFromContext(eng.lastCtx)
			if !ok {
				t.Fatal("engine context missing privacy request state")
			}
			meta := state.Metadata()
			if meta.RequestedProfile != "strict" || meta.ScopeID != "run-7f29b4d4" || meta.Surface != "openai" {
				t.Fatalf("privacy metadata=%+v", meta)
			}
			if meta.Workload != strings.Repeat("w", 64) {
				t.Fatalf("workload=%q, want capped 64-rune value", meta.Workload)
			}
		})
	}
}

func TestOpenAIPrivacy_NativeTypedErrorsDoNotExposeCauseOrDetail(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/chat/completions", body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{name: "completions", path: "/completions", body: `{"model":"auto","prompt":"hi","stream":false}`},
	}
	errorsToMap := []struct {
		code      string
		status    int
		errorType string
	}{
		{code: privacy.CodeRequestInvalid, status: http.StatusBadRequest, errorType: errInvalidRequest},
		{code: privacy.CodeProfileUnavailable, status: http.StatusBadRequest, errorType: errInvalidRequest},
		{code: privacy.CodeScopeClosed, status: http.StatusConflict, errorType: errInvalidRequest},
		{code: privacy.CodeInputBlocked, status: http.StatusUnprocessableEntity, errorType: errInvalidRequest},
		{code: privacy.CodeOutputBlocked, status: http.StatusBadGateway, errorType: errAPI},
		{code: privacy.CodeCapacityExceeded, status: http.StatusServiceUnavailable, errorType: errAPI},
		{code: privacy.CodeInternalError, status: http.StatusServiceUnavailable, errorType: errAPI},
	}
	for _, endpoint := range endpoints {
		for _, tc := range errorsToMap {
			t.Run(endpoint.name+"_"+tc.code, func(t *testing.T) {
				eng := &fakeEngine{collectErr: &privacy.Error{
					Code: tc.code, Stage: "private-stage-detail", Cause: errors.New("protected-cause-canary"),
				}}
				rec := doOpenAIPost(t, eng, endpoint.path, endpoint.body, nil)
				if rec.Code != tc.status {
					t.Fatalf("status=%d, want %d; body=%s", rec.Code, tc.status, rec.Body.String())
				}
				var envelope errorEnvelope
				if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode error envelope: %v; body=%s", err, rec.Body.String())
				}
				if envelope.Error.Type != tc.errorType || envelope.Error.Message != tc.code || envelope.Error.Code == nil || *envelope.Error.Code != tc.code || envelope.Error.Param != nil {
					t.Fatalf("error=%+v", envelope.Error)
				}
				if strings.Contains(rec.Body.String(), "protected-cause-canary") || strings.Contains(rec.Body.String(), "private-stage-detail") {
					t.Fatalf("privacy error leaked details: %s", rec.Body.String())
				}
			})
		}
	}
}

type openAIPrivacyServiceEngine struct {
	service *privacy.Service
	resp    *canonical.ChatResponse
}

func (e *openAIPrivacyServiceEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if _, err := e.service.Before(ctx, req); err != nil {
		return nil, err
	}
	resp := *e.resp
	resp.Message.Content = append([]canonical.ContentPart(nil), e.resp.Message.Content...)
	if err := e.service.After(ctx, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (e *openAIPrivacyServiceEngine) Run(context.Context, *canonical.ChatRequest) (RunHandle, error) {
	return nil, errors.New("unexpected streaming run")
}

func (e *openAIPrivacyServiceEngine) RunPostHooks(context.Context, *canonical.ChatRequest, *canonical.ChatResponse) error {
	return nil
}

func (e *openAIPrivacyServiceEngine) CollectFromRun(context.Context, RunHandle, *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return nil, errors.New("unexpected collect from run")
}

func newStrictOpenAIPrivacyEngine(t *testing.T, responseText string, classifier privacy.Classifier) *openAIPrivacyServiceEngine {
	t.Helper()
	service, err := privacy.NewService(privacy.Config{
		DefaultProfile:     privacy.ProfileStrict,
		RequestProfiles:    []privacy.Profile{privacy.ProfileStrict},
		AliasKey:           []byte("openai-task-ten-alias-key"),
		SecretAction:       privacy.ActionReplace,
		TechnicalAction:    privacy.ActionPseudonymize,
		ScopeTTL:           time.Hour,
		MaxScopes:          8,
		MaxEntriesPerScope: 32,
		MaxTotalEntries:    128,
		PIIEnabled:         true,
		PIIMode:            privacy.ActionReplace,
		Classifier:         classifier,
	})
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return &openAIPrivacyServiceEngine{
		service: service,
		resp: &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: responseText}},
			},
			StopReason: canonical.StopEndTurn,
		},
	}
}

type panickingOpenAIPrivacyClassifier struct{}

func (panickingOpenAIPrivacyClassifier) Classify(_, _ string) []privacy.Finding {
	panic("private-classifier-detail")
}

func TestOpenAIPrivacy_ReceiptPrecedesPassBlockAndInternalResponses(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/chat/completions", body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`},
		{name: "completions", path: "/completions", body: `{"model":"auto","prompt":"hi","stream":false}`},
	}
	outcomes := []struct {
		name       string
		text       string
		classifier privacy.Classifier
		wantStatus int
		wantCode   string
		wantCover  string
		wantResult string
	}{
		{name: "pass", text: "safe response", wantStatus: http.StatusOK, wantCover: "full", wantResult: "pass"},
		{name: "block", text: "[SECRET:API_KEY_1]", wantStatus: http.StatusBadGateway, wantCode: privacy.CodeOutputBlocked, wantCover: "full", wantResult: "block"},
		{name: "internal", text: "safe response", classifier: panickingOpenAIPrivacyClassifier{}, wantStatus: http.StatusServiceUnavailable, wantCode: privacy.CodeInternalError, wantCover: "input", wantResult: "error"},
	}
	for _, endpoint := range endpoints {
		for _, outcome := range outcomes {
			t.Run(endpoint.name+"_"+outcome.name, func(t *testing.T) {
				rec := doOpenAIPost(t, newStrictOpenAIPrivacyEngine(t, outcome.text, outcome.classifier), endpoint.path, endpoint.body, nil)
				if rec.Code != outcome.wantStatus {
					t.Fatalf("status=%d, want %d; body=%s", rec.Code, outcome.wantStatus, rec.Body.String())
				}
				if outcome.wantCode != "" {
					var envelope errorEnvelope
					if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
						t.Fatalf("decode error envelope: %v", err)
					}
					if envelope.Error.Message != outcome.wantCode || envelope.Error.Code == nil || *envelope.Error.Code != outcome.wantCode {
						t.Fatalf("error=%+v", envelope.Error)
					}
				}
				receipt := decodeOpenAIPrivacyReceipt(t, rec.Header().Get("X-GW-Privacy-Receipt"))
				if receipt.Profile != privacy.ProfileStrict || receipt.Coverage != outcome.wantCover || receipt.Result != outcome.wantResult {
					t.Fatalf("receipt=%+v", receipt)
				}
				if strings.Contains(rec.Body.String(), "private-classifier-detail") {
					t.Fatalf("response leaked internal detail: %s", rec.Body.String())
				}
			})
		}
	}
}
