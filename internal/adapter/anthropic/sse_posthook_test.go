// Quick 260530-df2 — SSE streaming PostHook invocation tests.
//
// Covers the streaming branch's RunPostHooks call site (handlers.go after
// runSSEEmitter returns). The plan's load-bearing risk is aggregator
// richness — these tests pin both the invocation AND the content shape
// of the resp the PostHook sees (text + thinking + tool_use, plus
// Message.ToolCalls per D-07).

package anthropic

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

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

// runSSEEmitterAndPostHooks drives runSSEEmitter against the supplied
// chunks + FinalResult and, on completion, calls eng.RunPostHooks with
// the aggregated response. Mirrors handlers.go's streaming branch shape
// (handlers.go:202-212 after Task 2 step 3) so tests exercise the same
// call sequence the production path does.
//
// model is "auto" so it matches existing golden conventions; req is
// supplied by the caller so tool_use tests can attach tools[].
func runSSEEmitterAndPostHooks(t *testing.T, ctx context.Context, eng Engine, req *canonical.ChatRequest, chunks []canonical.Chunk, final *canonical.FinalResult, finalErr error, logger *slog.Logger) (*httptest.ResponseRecorder, *canonical.ChatResponse, error) { //nolint:unparam // helper-pair symmetry with openai variant; result shape contract
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  final,
			err:    finalErr,
		},
		sessionID: "session_posthook",
	}
	rec := httptest.NewRecorder()
	var tools []canonical.ToolSpec
	if req != nil {
		tools = req.Tools
	}
	resp, err := runSSEEmitter(ctx, rec, runHandle, tools, "auto", 0, logger)
	if resp != nil {
		if pErr := eng.RunPostHooks(ctx, req, resp); pErr != nil {
			// Streaming WARN-and-swallow contract — log via the test
			// logger; mirror handlers.go production behavior.
			logger.Warn("anthropic: posthook error after streaming completion", "err", pErr)
		}
	}
	return rec, resp, err
}

// TestAnthropicSSE_PostHooksFireAfterStreamCompletes drives a stream of
// text + thought + tool_use chunks, then asserts:
//   - RunPostHooks was called exactly once (postN == 1)
//   - The captured resp.Message.Content has the concatenated text part,
//     the thinking part, AND the tool_use part with ID+Name+Input matching
//     the chunks emitted.
//   - resp.StopReason matches the FinalResult's StopReason.
//   - resp.Message.ToolCalls is populated per the D-07 Anthropic
//     exception (mirrors CollectAnthropicChat shape).
func TestAnthropicSSE_PostHooksFireAfterStreamCompletes(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "ponder"}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: map[string]any{"filePath": "CLAUDE.md"},
		}},
	}
	req := &canonical.ChatRequest{Model: "auto"}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}

	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, req, chunks, final, nil, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}

	if eng.postN != 1 {
		t.Fatalf("postN: got %d, want 1 (exactly one PostHook fire per streaming request)", eng.postN)
	}

	resp := eng.lastPostResp
	if resp == nil {
		t.Fatal("lastPostResp: got nil, want aggregated response")
	}
	// Text part is at Content[0] per assembleAnthropicChatResponse —
	// EXCEPT a coerced/native tool-only turn (text=="" && tool parts
	// present) omits the leading empty text block (Track 3b Task 5). This
	// test feeds non-empty text ("Hello world"), so the text part is
	// present at Content[0].
	if len(resp.Message.Content) < 1 {
		t.Fatalf("Content: got %v, want at least 1 part (text)", resp.Message.Content)
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %q, want %q (concatenated deltas)", resp.Message.Content[0].Text, "Hello world")
	}

	// Look for thinking + tool_use parts. Order: text, thinking, tool_use.
	var sawThinking, sawToolUse bool
	for _, p := range resp.Message.Content {
		if p.Kind == canonical.ContentKindThinking && p.Text == "ponder" {
			sawThinking = true
		}
		if p.Kind == canonical.ContentKindToolUse && p.ToolUse != nil &&
			p.ToolUse.ID == "toolu_01" && p.ToolUse.Name == "read" {
			if fp, _ := p.ToolUse.Input["filePath"].(string); fp == "CLAUDE.md" {
				sawToolUse = true
			}
		}
	}
	if !sawThinking {
		t.Errorf("Content: missing ContentKindThinking 'ponder' part; got %+v", resp.Message.Content)
	}
	if !sawToolUse {
		t.Errorf("Content: missing ContentKindToolUse with id=toolu_01 name=read input.filePath=CLAUDE.md; got %+v", resp.Message.Content)
	}

	// D-07 exception: Message.ToolCalls populated alongside Content[].
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1", len(resp.Message.ToolCalls))
	}
	if resp.Message.ToolCalls[0].ID != "toolu_01" {
		t.Errorf("ToolCalls[0].ID: got %q, want %q", resp.Message.ToolCalls[0].ID, "toolu_01")
	}

	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
}

// TestAnthropicSSE_PostHooksFireOnPartialStreamError exercises the
// Result()-error path. The plan explicitly favors returning the
// (partial) aggregated response on rerr != nil so operators get
// forensics + duration_ms on terminal stream failures. The streaming
// PostHook fires on the partial — chat-trace.log gets a
// post_chain_out record with whatever content arrived before the
// error. (The handler's eng.Run-fails-pre-headers path, in contrast,
// returns at handlers.go:179-187 BEFORE runSSEEmitter is called, so
// no PostHook fires there — verified by the handler integration
// tests, not by this emitter-level test.)
func TestAnthropicSSE_PostHooksFireOnPartialStreamError(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}},
	}
	// Result() returns error → finalizeStream returns (partialResp, err).
	_, resp, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, nil, errors.New("upstream blew up"), nullLogger())
	if err == nil {
		t.Fatalf("runSSEEmitter: got nil err, want propagated stream error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on Result() error (operators want forensics)")
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook fires on partial — forensics + duration_ms)", eng.postN)
	}
	// The partial aggregator should carry the text that did arrive.
	if eng.lastPostResp == nil || len(eng.lastPostResp.Message.Content) < 1 ||
		eng.lastPostResp.Message.Content[0].Text != "partial" {
		t.Errorf("lastPostResp.Content: want first part text 'partial', got %+v", eng.lastPostResp)
	}
}

// TestAnthropicSSE_PostHookErrorDoesNotFailResponse — when RunPostHooks
// returns an error, the SSE emitter has already written bytes to the
// wire. The client has received their stream successfully. The
// production handler logs the error at WARN and returns normally; tests
// assert (a) runSSEEmitter still returned nil (no propagation), (b) a
// WARN slog record carrying "anthropic: posthook" was emitted.
func TestAnthropicSSE_PostHookErrorDoesNotFailResponse(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{postErr: errors.New("posthook failed")}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
	}
	_, _, err := runSSEEmitterAndPostHooks(t, context.Background(), eng, &canonical.ChatRequest{Model: "auto"}, chunks, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil, logger)
	if err != nil {
		t.Fatalf("runSSEEmitter: got %v, want nil (PostHook error must NOT propagate to client)", err)
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

// TestAnthropicSSE_PostHooksFireOnClientDisconnect — when streamCtx is
// canceled mid-stream, runSSEEmitter returns ctx.Err() but the aggregator
// is preserved on the sseEmitter so the handler can still call
// RunPostHooks on the partial aggregation (operators want forensics +
// duration_ms even on disconnect).
//
// Task 2 spec: on the disconnect path, runSSEEmitter MUST still return
// the (partial) aggregated response alongside the error. The helper
// then fires RunPostHooks because resp != nil.
func TestAnthropicSSE_PostHooksFireOnClientDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t)
	eng := &fakeEngine{}

	// Channel that delivers one chunk then never closes. Cancel ctx
	// after first chunk is consumed to force the ctx.Done path.
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}}
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: canonical.StopUnknown},
		},
		sessionID: "session_disconnect",
	}
	defer close(ch)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so the loop processes the chunk first.
	go func() {
		// Wait until the chunk has been observed by polling the recorder.
		// Implementation detail: cancel as soon as we're scheduled — the
		// loop is fast enough that the chunk lands first under normal
		// conditions. If not, the test still proves the contract: on any
		// disconnect, PostHook fires with whatever was aggregated.
		cancel()
	}()
	rec := httptest.NewRecorder()
	resp, err := runSSEEmitter(ctx, rec, runHandle, nil, "auto", 0, nullLogger())
	if err == nil {
		t.Fatalf("runSSEEmitter: got nil err, want ctx-cancel error")
	}
	if resp == nil {
		t.Fatal("resp: got nil, want partial aggregated response on disconnect")
	}
	if pErr := eng.RunPostHooks(ctx, &canonical.ChatRequest{}, resp); pErr != nil {
		t.Fatalf("RunPostHooks: %v", pErr)
	}
	if eng.postN != 1 {
		t.Errorf("postN: got %d, want 1 (PostHook must fire on disconnect with partial aggregation)", eng.postN)
	}
}

type anthropicPrivacyStreamingEngine struct {
	service *privacy.Service
	chunks  []canonical.Chunk
	events  *[]string
}

func (e *anthropicPrivacyStreamingEngine) appendEvent(event string) {
	if e.events != nil {
		*e.events = append(*e.events, event)
	}
}

func (e *anthropicPrivacyStreamingEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	run, err := e.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return e.CollectFromRun(ctx, run, req)
}

func (e *anthropicPrivacyStreamingEngine) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	e.appendEvent("before")
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

func (e *anthropicPrivacyStreamingEngine) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	e.appendEvent("collect")
	var content strings.Builder
	for chunk := range run.Stream().Chunks() {
		if chunk.Kind == canonical.ChunkKindText && chunk.Text != nil {
			content.WriteString(chunk.Text.Content)
		}
	}
	if _, err := run.Stream().Result(); err != nil {
		return nil, err
	}
	resp := &canonical.ChatResponse{
		Model: req.Model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: content.String()}},
		},
		StopReason: canonical.StopEndTurn,
	}
	if err := e.RunPostHooks(ctx, req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (e *anthropicPrivacyStreamingEngine) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	e.appendEvent("after")
	if err := e.service.After(ctx, req, resp); err != nil {
		e.appendEvent("after_error")
		return err
	}
	e.appendEvent("after_tail")
	return nil
}

type anthropicEventWriter struct {
	header   http.Header
	body     bytes.Buffer
	statuses []int
	flushes  int
	events   *[]string
}

func newAnthropicEventWriter(events *[]string) *anthropicEventWriter {
	return &anthropicEventWriter{header: make(http.Header), events: events}
}

func (w *anthropicEventWriter) Header() http.Header { return w.header }

func (w *anthropicEventWriter) WriteHeader(status int) {
	if len(w.statuses) != 0 {
		return
	}
	w.statuses = append(w.statuses, status)
	*w.events = append(*w.events, "write_header")
}

func (w *anthropicEventWriter) Write(payload []byte) (int, error) {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	*w.events = append(*w.events, "write")
	return w.body.Write(payload)
}

func (w *anthropicEventWriter) Flush() {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.flushes++
	*w.events = append(*w.events, "flush")
}

type outputPanickingAnthropicClassifier struct{}

func (outputPanickingAnthropicClassifier) Classify(_, value string) []privacy.Finding {
	if value == "trigger-output-panic" {
		panic("private-output-panic-detail")
	}
	return nil
}

func newAnthropicStreamingPrivacyService(t *testing.T, profile privacy.Profile, classifier privacy.Classifier) *privacy.Service {
	t.Helper()
	config := privacy.Config{
		DefaultProfile:  profile,
		RequestProfiles: []privacy.Profile{profile},
		PIIEnabled:      true,
		PIIMode:         privacy.ActionReplace,
		Classifier:      classifier,
	}
	if profile == privacy.ProfileStrict {
		config.AliasKey = []byte("anthropic-streaming-alias-key")
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

func serveAnthropicPrivacyStream(t *testing.T, writer http.ResponseWriter, eng Engine, profile string) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/messages", strings.NewReader(
		`{"model":"auto","max_tokens":256,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if profile != "" {
		req.Header.Set("X-GW-Privacy-Profile", profile)
	}
	newTestAdapter(eng).ProtectedRouter().ServeHTTP(writer, req)
}

func TestAnthropicPrivacy_StrictRequestedStreamCollectsAndRunsAfterBeforeNativeReplay(t *testing.T) {
	var events []string
	eng := &anthropicPrivacyStreamingEngine{
		service: newAnthropicStreamingPrivacyService(t, privacy.ProfileStrict, nil),
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "complete "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "response"}},
		},
		events: &events,
	}
	writer := newAnthropicEventWriter(&events)
	serveAnthropicPrivacyStream(t, writer, eng, "strict")

	if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
		t.Fatalf("statuses=%v body=%s events=%v", writer.statuses, writer.body.String(), events)
	}
	afterAt := anthropicEventPosition(events, "after")
	afterTailAt := anthropicEventPosition(events, "after_tail")
	writeAt := anthropicEventPosition(events, "write_header")
	if afterAt < 0 || afterTailAt < afterAt || writeAt < afterTailAt {
		t.Fatalf("events=%v, response committed before Service.After", events)
	}
	wantEvents := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if got := sseEventLines(writer.body.String()); !equalSlice(got, wantEvents) {
		t.Fatalf("SSE events=%v, want %v; body=%s", got, wantEvents, writer.body.String())
	}
	data := sseDataLines(writer.body.String())
	if len(data) != len(wantEvents) {
		t.Fatalf("data frames=%d, want %d; body=%s", len(data), len(wantEvents), writer.body.String())
	}
	var start messageStart
	if err := json.Unmarshal([]byte(data[0]), &start); err != nil {
		t.Fatalf("decode message_start: %v", err)
	}
	if start.Type != "message_start" || !strings.HasPrefix(start.Message.ID, "msg_01") || start.Message.Type != "message" || start.Message.Role != "assistant" || start.Message.Model != "auto" || start.Message.Content == nil || len(start.Message.Content) != 0 || start.Message.StopReason != nil || start.Message.StopSequence != nil || start.Message.Usage != (usage{}) {
		t.Fatalf("message_start=%+v", start)
	}
	wantData := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"complete response"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}`,
		`{"type":"message_stop"}`,
	}
	for index, want := range wantData {
		if data[index+1] != want {
			t.Fatalf("data[%d]=%s, want %s", index+1, data[index+1], want)
		}
	}
	receipt := decodeAnthropicPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
	if receipt.Profile != privacy.ProfileStrict || receipt.Coverage != "full" || receipt.Result != "pass" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestAnthropicPrivacy_StrictRequestedStreamBlockOrInternalWritesOnlyNativeError(t *testing.T) {
	outcomes := []struct {
		name       string
		text       string
		classifier privacy.Classifier
		wantStatus int
		wantCode   string
		wantResult string
	}{
		{name: "block", text: "[SECRET:API_KEY_1]", wantStatus: http.StatusBadGateway, wantCode: privacy.CodeOutputBlocked, wantResult: "block"},
		{name: "internal", text: "trigger-output-panic", classifier: outputPanickingAnthropicClassifier{}, wantStatus: http.StatusServiceUnavailable, wantCode: privacy.CodeInternalError, wantResult: "error"},
	}
	for _, outcome := range outcomes {
		t.Run(outcome.name, func(t *testing.T) {
			var events []string
			eng := &anthropicPrivacyStreamingEngine{
				service: newAnthropicStreamingPrivacyService(t, privacy.ProfileStrict, outcome.classifier),
				chunks:  []canonical.Chunk{{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: outcome.text}}},
				events:  &events,
			}
			writer := newAnthropicEventWriter(&events)
			serveAnthropicPrivacyStream(t, writer, eng, "strict")

			if len(writer.statuses) != 1 || writer.statuses[0] != outcome.wantStatus {
				t.Fatalf("statuses=%v want=%d body=%s events=%v", writer.statuses, outcome.wantStatus, writer.body.String(), events)
			}
			if strings.Contains(writer.body.String(), "event: message_") || strings.Contains(writer.body.String(), "event: content_block_") || strings.Contains(writer.body.String(), "data:") {
				t.Fatalf("strict failure emitted success SSE: %s", writer.body.String())
			}
			var envelope errorEnvelope
			if err := json.Unmarshal(writer.body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v; body=%s", err, writer.body.String())
			}
			if envelope.Type != "error" || envelope.Error.Type != errAPI || envelope.Error.Message != outcome.wantCode {
				t.Fatalf("envelope=%+v", envelope)
			}
			afterErrorAt := anthropicEventPosition(events, "after_error")
			if afterErrorAt < 0 || anthropicEventPosition(events, "write_header") < afterErrorAt {
				t.Fatalf("events=%v, response committed before failed Service.After", events)
			}
			receipt := decodeAnthropicPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
			if receipt.Result != outcome.wantResult {
				t.Fatalf("receipt=%+v", receipt)
			}
			if strings.Contains(writer.body.String(), "private-output-panic-detail") {
				t.Fatalf("response leaked internal detail: %s", writer.body.String())
			}
		})
	}
}

func TestAnthropicPrivacy_StandardStreamRetainsIncrementalFlushPath(t *testing.T) {
	var events []string
	eng := &anthropicPrivacyStreamingEngine{
		service: newAnthropicStreamingPrivacyService(t, privacy.ProfileStandard, nil),
		chunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "first"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "second"}},
		},
		events: &events,
	}
	writer := newAnthropicEventWriter(&events)
	serveAnthropicPrivacyStream(t, writer, eng, "")

	if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusOK {
		t.Fatalf("statuses=%v body=%s", writer.statuses, writer.body.String())
	}
	if writer.flushes < 7 {
		t.Fatalf("flushes=%d, want message, per-chunk, and terminal event flushes", writer.flushes)
	}
	if writeAt, afterAt := anthropicEventPosition(events, "write_header"), anthropicEventPosition(events, "after"); writeAt < 0 || afterAt < 0 || writeAt > afterAt {
		t.Fatalf("events=%v, standard stream did not write incrementally before After", events)
	}
	receipt := decodeAnthropicPrivacyReceipt(t, writer.Header().Get("X-GW-Privacy-Receipt"))
	if receipt.Profile != privacy.ProfileStandard || receipt.Coverage != "input" || receipt.Result != "pass" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func anthropicEventPosition(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}
