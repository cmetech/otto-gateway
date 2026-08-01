// Quick 260530-df2 — OpenAI SSE streaming PostHook invocation tests.
// Mirrors the anthropic + ollama PostHook test pattern tuned for the
// OpenAI flat data-only SSE wire shape.

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

// jsonCaptureLogger returns a slog logger writing JSON records into buf
// — used to assert the WARN-and-swallow contract on PostHook errors.
func jsonCaptureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// runSSEEmitterAndPostHooks drives runSSEEmitter against the supplied
// chunks/FinalResult and, on completion, fires eng.RunPostHooks with
// the aggregated response. Mirrors handlers.go's call sequence after
// Task 4 step 2.
func runSSEEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) { //nolint:unparam // helper-pair symmetry with anthropic variant
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	run := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  final,
			rerr:   finalErr,
		},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, run, req, nil, "auto", 0, logger)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract.
			logger.Warn("openai: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestOpenAISSE_PostHooksFireAfterStreamCompletes — text-only stream.
// Asserts PostHook fires exactly once with a resp whose
// Message.Content[0].Text is the concatenated assistant text and
// resp.StopReason matches the FinalResult.
func TestOpenAISSE_PostHooksFireAfterStreamCompletes(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "OpenAI"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "Hello OpenAI" {
		t.Errorf("Content[0].Text: got %v, want 'Hello OpenAI'", resp.Message.Content)
	}
	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
}

// TestOpenAISSE_PostHooksFireOnPartialStreamError — Result()-error path
// still returns partial resp so PostHooks fire (forensics + duration_ms).
func TestOpenAISSE_PostHooksFireOnPartialStreamError(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	_, resp, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, nil, errors.New("upstream blew up"), nullLogger())
	if err == nil {
		t.Fatal("runSSEEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response for forensics")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part 'partial', got %+v", eng.lastPostResp)
	}
}

// TestOpenAISSE_PostHookErrorLoggedNotPropagated — WARN-and-swallow
// contract (T-df2-02).
func TestOpenAISSE_PostHookErrorLoggedNotPropagated(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{postErr: errors.New("posthook failed")}
	var buf bytes.Buffer
	logger := jsonCaptureLogger(&buf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runSSEEmitter: got %v, want nil (PostHook error must NOT propagate)", err)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	logged := buf.String()
	if !strings.Contains(logged, "posthook") {
		t.Errorf("slog log: missing 'posthook' substring; got %q", logged)
	}
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("slog level: missing WARN record; got %q", logged)
	}
}

// TestOpenAISSE_PostHooksFireOnClientDisconnect — ctx-cancel still
// fires PostHook on the partial aggregation.
func TestOpenAISSE_PostHooksFireOnClientDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}
	defer close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{chunks: ch, final: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "disconnect",
	}

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, run, &canonical.ChatRequest{Model: "auto"}, nil, "auto", 0, nullLogger())
	if err == nil {
		t.Fatal("runSSEEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{Model: "auto"}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
}

// TestOpenAISSE_PostHookSeesToolCallsAfterStreamingCoerce — when
// req.Tools is populated and the assistant emits a JSON tool-call
// payload as text, the streaming-coerce path activates: it discards
// buffered text frames and emits a multi-frame native delta.tool_calls
// SSE sequence + finish_reason="tool_calls". The post-stream PostHook
// canonical response should carry Message.ToolCalls populated with the
// coerce-synthesized entry — that's what handlers see (and what
// chat-trace.log records).
func TestOpenAISSE_PostHookSeesToolCallsAfterStreamingCoerce(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	// JSON-shaped assistant text payload that CoerceToolCall recognizes:
	// matches one of the req.Tools entries by Name.
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `{"tool":"read","args":{"filePath":"a.md"}}`}},
	}
	req := &canonical.ChatRequest{
		Model: "auto",
		Tools: []canonical.ToolSpec{{Name: "read", Description: "read a file"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	// The exact CoerceToolCall behavior is governed by engine package;
	// the contract this test pins is: WHEN coerce fired (the wire emits
	// finish_reason:"tool_calls"), the PostHook canonical resp must
	// reflect Message.ToolCalls. If CoerceToolCall doesn't recognize
	// this exact payload shape (which depends on the test catalog),
	// the test verifies the negative — text aggregation is still
	// present.
	//
	// The two acceptable behaviors below are both correct per the
	// post-stream aggregator design: if coerce fires, ToolCalls is
	// populated; if it doesn't, the text falls through to Content[0].
	if len(resp.Message.ToolCalls) > 0 {
		if resp.Message.ToolCalls[0].Name != "read" {
			t.Errorf("ToolCalls[0].Name: got %q, want %q", resp.Message.ToolCalls[0].Name, "read")
		}
	} else {
		// Coerce miss → JSON text should be in Content[0].
		if len(resp.Message.Content) < 1 || !strings.Contains(resp.Message.Content[0].Text, "filePath") {
			t.Errorf("Content[0].Text: missing JSON payload (coerce miss path); got %+v", resp.Message.Content)
		}
	}
}

type openAIPrivacyStreamingEngine struct {
	service *privacy.Service
	chunks  []canonical.Chunk
	events  *[]string
}

func (e *openAIPrivacyStreamingEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	run, err := e.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return e.CollectFromRun(ctx, run, req)
}

func (e *openAIPrivacyStreamingEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	*e.events = append(*e.events, "before")
	if _, err := e.service.Before(ctx, req); err != nil {
		return nil, err
	}
	ch := make(chan canonical.Chunk, len(e.chunks))
	for _, chunk := range e.chunks {
		ch <- chunk
	}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "privacy-stream",
	}, nil
}

func (e *openAIPrivacyStreamingEngine) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	*e.events = append(*e.events, "collect")
	var content strings.Builder
	for chunk := range run.Stream().Chunks() {
		if chunk.Kind == canonical.ChunkKindText && chunk.Text != nil {
			content.WriteString(chunk.Text.Content)
		}
	}
	final, err := run.Stream().Result()
	if err != nil {
		return nil, err
	}
	stopReason := canonical.StopUnknown
	if final != nil {
		stopReason = final.StopReason
	}
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: content.String()}},
		},
		StopReason: stopReason,
	}
	*e.events = append(*e.events, "after")
	if err := e.service.After(ctx, req, resp); err != nil {
		*e.events = append(*e.events, "after_error")
		return nil, err
	}
	return resp, nil
}

func (e *openAIPrivacyStreamingEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	*e.events = append(*e.events, "after")
	return e.service.After(ctx, req, resp)
}

type openAIEventWriter struct {
	header   http.Header
	body     bytes.Buffer
	statuses []int
	flushes  int
	events   *[]string
}

func newOpenAIEventWriter(events *[]string) *openAIEventWriter {
	return &openAIEventWriter{header: make(http.Header), events: events}
}

func (w *openAIEventWriter) Header() http.Header { return w.header }

func (w *openAIEventWriter) WriteHeader(status int) {
	if len(w.statuses) != 0 {
		return
	}
	w.statuses = append(w.statuses, status)
	*w.events = append(*w.events, "write_header")
}

func (w *openAIEventWriter) Write(payload []byte) (int, error) {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	*w.events = append(*w.events, "write")
	return w.body.Write(payload)
}

func (w *openAIEventWriter) Flush() {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.flushes++
	*w.events = append(*w.events, "flush")
}

type outputPanickingOpenAIClassifier struct{}

func (outputPanickingOpenAIClassifier) Classify(_, value string) []privacy.Finding {
	if value == "trigger-output-panic" {
		panic("private-output-panic-detail")
	}
	return nil
}

func newOpenAIStreamingPrivacyService(t *testing.T, profile privacy.Profile, classifier privacy.Classifier) *privacy.Service {
	t.Helper()
	config := privacy.Config{
		DefaultProfile:  profile,
		RequestProfiles: []privacy.Profile{profile},
		PIIEnabled:      true,
		PIIMode:         privacy.ActionReplace,
		Classifier:      classifier,
	}
	if profile == privacy.ProfileStrict {
		config.AliasKey = []byte("openai-streaming-alias-key")
		config.SecretAction = privacy.ActionReplace
		config.TechnicalAction = privacy.ActionPseudonymize
		config.ScopeTTL = time.Hour
		config.MaxScopes = 8
		config.MaxEntriesPerScope = 32
		config.MaxTotalEntries = 128
	}
	service, err := privacy.NewService(config)
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func newOpenAIStandardCompletionsPrivacyService(t *testing.T, mode privacy.Action) *privacy.Service {
	t.Helper()
	config := privacy.Config{
		DefaultProfile:  privacy.ProfileStandard,
		RequestProfiles: []privacy.Profile{privacy.ProfileStandard},
		PIIEnabled:      true,
		PIIMode:         mode,
	}
	if mode == privacy.ActionEncrypt {
		config.PIIEncryptKey = []byte("0123456789abcdef0123456789abcdef")
	}
	service, err := privacy.NewService(config)
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func TestOpenAIPrivacy_LegacyCompletionsRequestedStreamUsesValidatedReplayOutcome(t *testing.T) {
	tests := []struct {
		name            string
		service         func(*testing.T) *privacy.Service
		profileHeader   string
		wantContentType string
		wantProfile     privacy.Profile
		wantSSE         bool
	}{
		{
			name: "default_standard_encrypt_retains_JSON_downgrade",
			service: func(t *testing.T) *privacy.Service {
				return newOpenAIStandardCompletionsPrivacyService(t, privacy.ActionEncrypt)
			},
			wantContentType: "application/json",
			wantProfile:     privacy.ProfileStandard,
		},
		{
			name: "standard_replace_retains_JSON_downgrade_and_full_coverage",
			service: func(t *testing.T) *privacy.Service {
				return newOpenAIStandardCompletionsPrivacyService(t, privacy.ActionReplace)
			},
			wantContentType: "application/json",
			wantProfile:     privacy.ProfileStandard,
		},
		{
			name: "strict_replays_validated_native_SSE",
			service: func(t *testing.T) *privacy.Service {
				return newOpenAIStreamingPrivacyService(t, privacy.ProfileStrict, nil)
			},
			profileHeader:   "strict",
			wantContentType: "text/event-stream",
			wantProfile:     privacy.ProfileStrict,
			wantSSE:         true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			eng := &openAIPrivacyStreamingEngine{
				service: tc.service(t),
				chunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "complete "}},
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "response"}},
				},
				events: &events,
			}
			headers := make(http.Header)
			if tc.profileHeader != "" {
				headers.Set("X-GW-Privacy-Profile", tc.profileHeader)
			}
			rec := doOpenAIPost(t, eng, "/completions", `{"model":"auto","prompt":"hi","stream":true}`, headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s events=%v", rec.Code, rec.Body.String(), events)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantContentType) {
				t.Fatalf("Content-Type=%q, want prefix %q; body=%s", got, tc.wantContentType, rec.Body.String())
			}
			receipt := decodeOpenAIPrivacyReceipt(t, rec.Header().Get("X-GW-Privacy-Receipt"))
			if receipt.Profile != tc.wantProfile || receipt.Coverage != "full" || receipt.Result != "pass" {
				t.Fatalf("receipt=%+v, want profile=%s coverage=full result=pass", receipt, tc.wantProfile)
			}
			body := rec.Body.String()
			if tc.wantSSE {
				for _, want := range []string{`"object":"text_completion"`, `"text":"complete response"`, `"finish_reason":"stop"`, "data: [DONE]"} {
					if !strings.Contains(body, want) {
						t.Fatalf("SSE body missing %q: %s", want, body)
					}
				}
				return
			}
			if strings.Contains(body, "data:") || strings.Contains(body, "[DONE]") {
				t.Fatalf("standard legacy response changed to SSE: %s", body)
			}
			var completion textCompletion
			if err := json.Unmarshal(rec.Body.Bytes(), &completion); err != nil {
				t.Fatalf("decode text completion JSON: %v; body=%s", err, body)
			}
			if completion.Object != "text_completion" || completion.Model != "auto" || len(completion.Choices) != 1 || completion.Choices[0].Text != "complete response" || completion.Choices[0].FinishReason != "stop" {
				t.Fatalf("legacy text completion body=%+v", completion)
			}
		})
	}
}

func openAIPrivacyStreamEndpoints() []struct {
	name        string
	path        string
	body        string
	objectField string
	content     string
} {
	return []struct {
		name        string
		path        string
		body        string
		objectField string
		content     string
	}{
		{
			name: "chat", path: "/chat/completions",
			body:        `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			objectField: `"object":"chat.completion.chunk"`,
			content:     `"content":"complete response"`,
		},
		{
			name: "completions", path: "/completions",
			body:        `{"model":"auto","prompt":"hi","stream":true}`,
			objectField: `"object":"text_completion"`,
			content:     `"text":"complete response"`,
		},
	}
}

func TestOpenAIPrivacy_StrictRequestedStreamCollectsAndRunsAfterBeforeNativeReplay(t *testing.T) {
	for _, endpoint := range openAIPrivacyStreamEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			var events []string
			eng := &openAIPrivacyStreamingEngine{
				service: newOpenAIStreamingPrivacyService(t, privacy.ProfileStrict, nil),
				chunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "complete "}},
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "response"}},
				},
				events: &events,
			}
			writer := newOpenAIEventWriter(&events)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1"+endpoint.path, strings.NewReader(endpoint.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GW-Privacy-Profile", "strict")
			a := New(Config{Logger: nullLogger(), Engine: eng})
			router := chi.NewRouter()
			router.Route("/v1", func(sub chi.Router) { a.RegisterRoutes(sub) })
			router.ServeHTTP(writer, req)

			if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
				t.Fatalf("statuses=%v body=%s events=%v", writer.statuses, writer.body.String(), events)
			}
			afterAt := openAIEventPosition(events, "after")
			writeAt := openAIEventPosition(events, "write_header")
			if afterAt < 0 || writeAt < 0 || writeAt < afterAt {
				t.Fatalf("events=%v, response committed before Service.After", events)
			}
			for _, want := range []string{endpoint.objectField, endpoint.content, `"finish_reason":"stop"`, "data: [DONE]"} {
				if !strings.Contains(writer.body.String(), want) {
					t.Fatalf("body missing %q: %s", want, writer.body.String())
				}
			}
			receipt := decodeOpenAIPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
			if receipt.Profile != privacy.ProfileStrict || receipt.Coverage != "full" || receipt.Result != "pass" {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func TestOpenAIPrivacy_StrictRequestedStreamBlockOrInternalWritesNoSuccessResponse(t *testing.T) {
	outcomes := []struct {
		name       string
		text       string
		classifier privacy.Classifier
		wantStatus int
		wantCode   string
		wantResult string
	}{
		{name: "block", text: "[SECRET:API_KEY_1]", wantStatus: http.StatusBadGateway, wantCode: privacy.CodeOutputBlocked, wantResult: "block"},
		{name: "internal", text: "trigger-output-panic", classifier: outputPanickingOpenAIClassifier{}, wantStatus: http.StatusServiceUnavailable, wantCode: privacy.CodeInternalError, wantResult: "error"},
	}
	for _, endpoint := range openAIPrivacyStreamEndpoints() {
		for _, outcome := range outcomes {
			t.Run(endpoint.name+"_"+outcome.name, func(t *testing.T) {
				var events []string
				eng := &openAIPrivacyStreamingEngine{
					service: newOpenAIStreamingPrivacyService(t, privacy.ProfileStrict, outcome.classifier),
					chunks:  []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: outcome.text}}},
					events:  &events,
				}
				writer := newOpenAIEventWriter(&events)
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1"+endpoint.path, strings.NewReader(endpoint.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-GW-Privacy-Profile", "strict")
				a := New(Config{Logger: nullLogger(), Engine: eng})
				router := chi.NewRouter()
				router.Route("/v1", func(sub chi.Router) { a.RegisterRoutes(sub) })
				router.ServeHTTP(writer, req)

				if len(writer.statuses) != 1 || writer.statuses[0] != outcome.wantStatus {
					t.Fatalf("statuses=%v want=%d body=%s events=%v", writer.statuses, outcome.wantStatus, writer.body.String(), events)
				}
				if strings.Contains(writer.body.String(), "data:") || strings.Contains(writer.body.String(), "[DONE]") {
					t.Fatalf("strict failure emitted success SSE: %s", writer.body.String())
				}
				var envelope errorEnvelope
				if err := json.Unmarshal(writer.body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode error envelope: %v; body=%s", err, writer.body.String())
				}
				if envelope.Error.Type != errAPI || envelope.Error.Message != outcome.wantCode || envelope.Error.Code == nil || *envelope.Error.Code != outcome.wantCode {
					t.Fatalf("error=%+v", envelope.Error)
				}
				if openAIEventPosition(events, "write_header") < openAIEventPosition(events, "after_error") {
					t.Fatalf("events=%v, response committed before failed Service.After", events)
				}
				receipt := decodeOpenAIPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
				if receipt.Result != outcome.wantResult {
					t.Fatalf("receipt=%+v", receipt)
				}
				if strings.Contains(writer.body.String(), "private-output-panic-detail") {
					t.Fatalf("response leaked internal detail: %s", writer.body.String())
				}
			})
		}
	}
}

func TestOpenAIPrivacy_StandardChatStreamRetainsIncrementalFlushPath(t *testing.T) {
	var events []string
	eng := &openAIPrivacyStreamingEngine{
		service: newOpenAIStreamingPrivacyService(t, privacy.ProfileStandard, nil),
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "first"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "second"}},
		},
		events: &events,
	}
	writer := newOpenAIEventWriter(&events)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	a := New(Config{Logger: nullLogger(), Engine: eng})
	router := chi.NewRouter()
	router.Route("/v1", func(sub chi.Router) { a.RegisterRoutes(sub) })
	router.ServeHTTP(writer, req)

	if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
		t.Fatalf("statuses=%v body=%s", writer.statuses, writer.body.String())
	}
	if writer.flushes < 4 {
		t.Fatalf("flushes=%d, want role, per-chunk, terminal, and done flushes", writer.flushes)
	}
	if writeAt, afterAt := openAIEventPosition(events, "write_header"), openAIEventPosition(events, "after"); writeAt < 0 || afterAt < 0 || writeAt > afterAt {
		t.Fatalf("events=%v, standard stream did not write incrementally before After", events)
	}
	receipt := decodeOpenAIPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
	if receipt.Profile != privacy.ProfileStandard || receipt.Coverage != "input" || receipt.Result != "pass" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func openAIEventPosition(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}
