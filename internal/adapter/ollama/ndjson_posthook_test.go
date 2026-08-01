// Quick 260530-df2 — NDJSON streaming PostHook invocation tests for
// /api/chat AND /api/generate. Mirrors the anthropic SSE PostHook
// tests but tunes for ollama's NDJSON wire shape + the
// streaming-coerce buffering decision.

package ollama

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

// nilLoggerJSON returns a slog logger writing JSON records into buf.
// Tests use this when asserting the WARN-and-swallow contract on
// PostHook errors.
func nilLoggerJSON(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type privacyStreamingEngine struct {
	service *privacy.Service
	chunks  []canonical.Chunk
	events  *[]string
}

func (e *privacyStreamingEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	run, err := e.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return e.CollectFromRun(ctx, run, req)
}

func (e *privacyStreamingEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	*e.events = append(*e.events, "before")
	if _, err := e.service.Before(ctx, req); err != nil {
		return nil, err
	}
	return newFakeRunHandle(e.chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil), nil
}

func (e *privacyStreamingEngine) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	*e.events = append(*e.events, "collect")
	var text strings.Builder
	for chunk := range run.Stream().Chunks() {
		if chunk.Kind == canonical.ChunkKindText && chunk.Text != nil {
			text.WriteString(chunk.Text.Content)
		}
	}
	final, err := run.Stream().Result()
	if err != nil {
		return nil, err
	}
	stop := canonical.StopUnknown
	if final != nil {
		stop = final.StopReason
	}
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: text.String()}},
		},
		StopReason: stop,
	}
	*e.events = append(*e.events, "after")
	if err := e.service.After(ctx, req, resp); err != nil {
		*e.events = append(*e.events, "after_error")
		return nil, err
	}
	return resp, nil
}

func (e *privacyStreamingEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	*e.events = append(*e.events, "after")
	return e.service.After(ctx, req, resp)
}

type eventResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	statuses []int
	flushes  int
	events   *[]string
}

func newEventResponseWriter(events *[]string) *eventResponseWriter {
	return &eventResponseWriter{header: make(http.Header), events: events}
}

func (w *eventResponseWriter) Header() http.Header { return w.header }

func (w *eventResponseWriter) WriteHeader(status int) {
	if len(w.statuses) != 0 {
		return
	}
	w.statuses = append(w.statuses, status)
	*w.events = append(*w.events, "write_header")
}

func (w *eventResponseWriter) Write(payload []byte) (int, error) {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	*w.events = append(*w.events, "write")
	return w.body.Write(payload)
}

func (w *eventResponseWriter) Flush() {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.flushes++
	*w.events = append(*w.events, "flush")
}

type outputPanickingOllamaClassifier struct{}

func (outputPanickingOllamaClassifier) Classify(_, value string) []privacy.Finding {
	if value == "trigger-output-panic" {
		panic("private-output-panic-detail")
	}
	return nil
}

func newStreamingPrivacyService(t *testing.T, profile privacy.Profile, classifier privacy.Classifier) *privacy.Service {
	t.Helper()
	config := privacy.Config{
		DefaultProfile:  profile,
		RequestProfiles: []privacy.Profile{profile},
		PIIEnabled:      true,
		PIIMode:         privacy.ActionReplace,
		Classifier:      classifier,
	}
	if profile == privacy.ProfileStrict {
		config.AliasKey = []byte("ollama-streaming-alias-key")
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

func TestOllamaPrivacy_StrictStreamCollectsAndRunsAfterBeforeNativeReplay(t *testing.T) {
	endpoints := []struct {
		name       string
		path       string
		body       string
		frameField string
	}{
		{
			name: "chat", path: "/chat",
			body:       `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			frameField: `"message":{"role":"assistant","content":"complete response"}`,
		},
		{
			name: "generate", path: "/generate",
			body:       `{"model":"auto","prompt":"hi","stream":true}`,
			frameField: `"response":"complete response"`,
		},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			var events []string
			eng := &privacyStreamingEngine{
				service: newStreamingPrivacyService(t, privacy.ProfileStrict, nil),
				chunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "complete "}},
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "response"}},
				},
				events: &events,
			}
			writer := newEventResponseWriter(&events)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.path, strings.NewReader(endpoint.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GW-Privacy-Profile", "strict")
			newTestAdapter(eng, nil).ProtectedRouter().ServeHTTP(writer, req)

			if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
				t.Fatalf("statuses=%v body=%s", writer.statuses, writer.body.String())
			}
			afterAt := eventPosition(events, "after")
			writeAt := eventPosition(events, "write_header")
			if afterAt < 0 || writeAt < 0 || writeAt < afterAt {
				t.Fatalf("events=%v, response committed before Service.After", events)
			}
			lines := scanNDJSON(t, writer.body.Bytes())
			if len(lines) != 2 {
				t.Fatalf("lines=%d, want synthetic data+terminal frames; body=%s", len(lines), writer.body.String())
			}
			if !strings.Contains(string(lines[0]), endpoint.frameField) || !strings.Contains(string(lines[0]), `"done":false`) {
				t.Fatalf("native data frame=%s", lines[0])
			}
			if !strings.Contains(string(lines[1]), `"done":true`) || !strings.Contains(string(lines[1]), `"done_reason":"stop"`) {
				t.Fatalf("native terminal frame=%s", lines[1])
			}
			receipt := decodePrivacyReceiptHeader(t, writer.Header().Get("X-GW-Privacy-Receipt"))
			if receipt.Profile != privacy.ProfileStrict || receipt.Coverage != "full" || receipt.Result != "pass" {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func TestOllamaPrivacy_StrictStreamBlockOrInternalWritesNoSuccessResponse(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/chat", body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{name: "generate", path: "/generate", body: `{"model":"auto","prompt":"hi","stream":true}`},
	}
	outcomes := []struct {
		name       string
		text       string
		classifier privacy.Classifier
		wantStatus int
		wantCode   string
		wantResult string
	}{
		{
			name: "block", text: "[SECRET:API_KEY_1]",
			wantStatus: http.StatusBadGateway, wantCode: privacy.CodeOutputBlocked, wantResult: "block",
		},
		{
			name: "internal", text: "trigger-output-panic", classifier: outputPanickingOllamaClassifier{},
			wantStatus: http.StatusServiceUnavailable, wantCode: privacy.CodeInternalError, wantResult: "error",
		},
	}
	for _, endpoint := range endpoints {
		for _, outcome := range outcomes {
			t.Run(endpoint.name+"_"+outcome.name, func(t *testing.T) {
				var events []string
				eng := &privacyStreamingEngine{
					service: newStreamingPrivacyService(t, privacy.ProfileStrict, outcome.classifier),
					chunks:  []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: outcome.text}}},
					events:  &events,
				}
				writer := newEventResponseWriter(&events)
				req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.path, strings.NewReader(endpoint.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-GW-Privacy-Profile", "strict")
				newTestAdapter(eng, nil).ProtectedRouter().ServeHTTP(writer, req)

				if len(writer.statuses) != 1 || writer.statuses[0] != outcome.wantStatus {
					t.Fatalf("statuses=%v want=%d body=%s events=%v", writer.statuses, outcome.wantStatus, writer.body.String(), events)
				}
				for _, status := range writer.statuses {
					if status == http.StatusOK {
						t.Fatalf("strict failure committed 200: statuses=%v events=%v", writer.statuses, events)
					}
				}
				wantBody := `{"error":"` + outcome.wantCode + `"}` + "\n"
				if got := writer.body.String(); got != wantBody {
					t.Fatalf("body=%q, want error-only %q", got, wantBody)
				}
				if eventPosition(events, "write_header") < eventPosition(events, "after_error") {
					t.Fatalf("events=%v, response committed before failed Service.After", events)
				}
				receipt := decodePrivacyReceiptHeader(t, writer.Header().Get("X-GW-Privacy-Receipt"))
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

func TestOllamaPrivacy_StandardStreamRetainsIncrementalFlushPath(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/chat", body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`},
		{name: "generate", path: "/generate", body: `{"model":"auto","prompt":"hi","stream":true}`},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			var events []string
			eng := &privacyStreamingEngine{
				service: newStreamingPrivacyService(t, privacy.ProfileStandard, nil),
				chunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "first"}},
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "second"}},
				},
				events: &events,
			}
			writer := newEventResponseWriter(&events)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, endpoint.path, strings.NewReader(endpoint.body))
			req.Header.Set("Content-Type", "application/json")
			newTestAdapter(eng, nil).ProtectedRouter().ServeHTTP(writer, req)

			if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
				t.Fatalf("statuses=%v body=%s", writer.statuses, writer.body.String())
			}
			if writer.flushes < 3 {
				t.Fatalf("flushes=%d, want per-chunk plus terminal flush", writer.flushes)
			}
			if writeAt, afterAt := eventPosition(events, "write_header"), eventPosition(events, "after"); writeAt < 0 || afterAt < 0 || writeAt > afterAt {
				t.Fatalf("events=%v, standard stream did not write incrementally before After", events)
			}
			receipt := decodePrivacyReceiptHeader(t, writer.Header().Get("X-GW-Privacy-Receipt"))
			if receipt.Profile != privacy.ProfileStandard || receipt.Coverage != "input" || receipt.Result != "pass" {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}

func eventPosition(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}

// runNDJSONEmitterAndPostHooks drives the NDJSON emitter against the
// supplied chunks/FinalResult and, on completion, fires
// eng.RunPostHooks with the aggregated response. Mirrors handlers.go's
// call sequence (handleChat / handleGenerate after Task 3 step 2).
func runNDJSONEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, isChat bool, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) { //nolint:unparam // helper-pair symmetry with anthropic variant
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: final, err: finalErr},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	resp, err := runNDJSONEmitter(ctx, noopCancelFn, rec, run, "auto", isChat, time.Now(), logger, req, nil, 0)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract — log via test logger.
			logger.Warn("ollama: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestOllamaNDJSON_Chat_PostHooksFireAfterStreamCompletes drives a chat
// stream with text + thought chunks. Asserts:
//   - RunPostHooks fires exactly once
//   - The captured resp.Message.Content[0].Text is the concatenated
//     assistant text (NOT empty — the load-bearing aggregator
//     correctness invariant)
//   - The thinking part appears at Content[1] when applicable.
func TestOllamaNDJSON_Chat_PostHooksFireAfterStreamCompletes(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "pondering"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (PostHook fires once per streaming request)", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil, want aggregated response")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %v, want 'Hello world' (concatenated)", resp.Message.Content)
	}
	var sawThinking bool
	for _, p := range resp.Message.Content {
		if p.Kind == canonical.ContentKindThinking && p.Text == "pondering" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Errorf("Content: missing ContentKindThinking 'pondering' part; got %+v", resp.Message.Content)
	}
}

// TestOllamaNDJSON_Generate_PostHooksFireAfterStreamCompletes — same
// shape for the /api/generate (handleGenerate) call site. isChat=false.
// Thinking is dropped on the wire per D-04 but text aggregation still
// works.
func TestOllamaNDJSON_Generate_PostHooksFireAfterStreamCompletes(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "gen text"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, false, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	if len(resp.Message.Content) < 1 || resp.Message.Content[0].Text != "gen text" {
		t.Errorf("Content[0].Text: got %v, want 'gen text'", resp.Message.Content)
	}
}

// TestOllamaNDJSON_PostHooksFireOnPartialStreamError — Result()-error
// path still returns the partial aggregated response so PostHooks fire
// for forensics + duration_ms (T-df2-03 sync.Map leak mitigation).
// Same intent as the anthropic-side test.
func TestOllamaNDJSON_PostHooksFireOnPartialStreamError(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	req := &canonical.ChatRequest{Model: "auto"}

	_, resp, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, nil, errors.New("upstream blew up"), nilLogger())
	if err == nil {
		t.Fatal("runNDJSONEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on Result() error")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on partial — forensics)", eng.postN)
	}
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part 'partial', got %+v", eng.lastPostResp)
	}
}

// TestOllamaNDJSON_PostHookErrorLoggedNotPropagated — when RunPostHooks
// returns an error, runNDJSONEmitter has already written bytes to the
// wire. The handler logs at WARN and continues. T-df2-02.
func TestOllamaNDJSON_PostHookErrorLoggedNotPropagated(t *testing.T) {
	eng := &fakeEngine{postErr: errors.New("posthook failed")}
	var buf bytes.Buffer
	logger := nilLoggerJSON(&buf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, true, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runNDJSONEmitter: got %v, want nil (PostHook error must NOT propagate)", err)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1", eng.postN)
	}
	logged := buf.String()
	if !strings.Contains(logged, "posthook") {
		t.Errorf("slog log: missing 'posthook' substring; got %q", logged)
	}
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Errorf("slog level: missing WARN record for posthook error; got %q", logged)
	}
}

// TestOllamaNDJSON_PostHooksFireOnClientDisconnect — ctx-cancel mid-
// stream still returns the partial aggregation so PostHooks fire.
func TestOllamaNDJSON_PostHooksFireOnClientDisconnect(t *testing.T) {
	eng := &fakeEngine{}

	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}}
	// Channel intentionally NOT closed so the loop blocks after the chunk.
	defer close(ch)
	run := &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "disconnect",
	}

	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	rec := httptest.NewRecorder()
	resp, err := runNDJSONEmitter(ctx, noopCancelFn, rec, run, "auto", true, time.Now(), nilLogger(), &canonical.ChatRequest{Model: "auto"}, nil, 0)
	if err == nil {
		t.Fatal("runNDJSONEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{Model: "auto"}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on disconnect)", eng.postN)
	}
}

// TestOllamaNDJSON_ChatPostHookSeesToolCalls (Defect 1c, 2026-07-16) —
// when the stream emits a kiro-native ChunkKindToolCall on /api/chat, it is
// surfaced structurally on the done:true line AND mirrored onto the
// post-stream aggregated response's Message.ToolCalls so LoggingHook.After /
// ChatTraceHook.After observe the structured call. No `[tool:` narration is
// written into Content.
func TestOllamaNDJSON_ChatPostHookSeesToolCalls(t *testing.T) {
	eng := &fakeEngine{}
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: map[string]any{"filePath": "a.md"},
		}},
	}
	req := &canonical.ChatRequest{
		Model: "auto",
		Tools: []canonical.ToolSpec{{Name: "read", Description: "stub"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runNDJSONEmitterAndPostHooks(t, context.Background(), eng, req, chunks, true, final, nil, nilLogger())
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}
	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1", eng.postN)
	}
	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil")
	}
	// No `[tool:` narration in Content.
	for _, part := range resp.Message.Content {
		if strings.Contains(part.Text, "[tool:") {
			t.Errorf("Content must not carry a [tool: marker; got %+v", resp.Message.Content)
		}
	}
	// The structured tool call is mirrored onto Message.ToolCalls.
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1 (kiro-native surfaced structurally)", len(resp.Message.ToolCalls))
	}
	if got := resp.Message.ToolCalls[0].Name; got != "read" {
		t.Errorf("ToolCalls[0].Name: got %q, want read", got)
	}
}
