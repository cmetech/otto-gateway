// Phase 14 Plan 14-04 Task 1 — Regression test for REL-HOOKS-01 (G-1 Medium).
//
// Finding G-1: Non-streaming aggregation error paths skip PostHooks, causing
// LoggingHook.startTimes and ChatTraceHook.startTimes sync.Map entries to leak
// unboundedly on every idle-timeout or Result() error.
//
// Phase 16 fix (this commit): in engine.Collect and
// adapter/anthropic.CollectAnthropicChat, invoke the PostHook chain with a
// nil resp on the idle-timeout and Result()-error returns before propagating
// the error (mirrors the streaming discipline). LoggingHook.After and
// ChatTraceHook.After both nil-guard resp so they do not panic when called
// with nil — and they still LoadAndDelete the startTimes entry so the
// per-request bookkeeping is reclaimed on every code path.
package plugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin/compress"
	piiplugin "otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/privacy"
)

// TestRegression_REL_HOOKS_01_StartTimesLeak asserts the post-fix behavior
// of the After() methods on both Pre+Post hooks (LoggingHook and
// ChatTraceHook): when After() is invoked with a nil resp (the shape used
// by engine.Collect / anthropic.CollectAnthropicChat on the idle-timeout
// and Result()-error error paths), the startTimes sync.Map entry is
// reclaimed and no panic occurs.
//
// This is the regression-locking shape of the G-1 fix. Pre-fix, the
// error-path return at engine/collect.go:165, :171, :174 (and
// anthropic/collect.go:177, :184) skipped the PostHook traversal entirely,
// so startTimes entries leaked one-per-failed-request. Post-fix the
// adapter loop calls callPostHookSafe(ctx, h, req, nil) before returning,
// which is what this test exercises directly against the hooks.
func TestRegression_REL_HOOKS_01_StartTimesLeak(t *testing.T) {
	t.Run("LoggingHook reclaims startTimes on nil resp", func(t *testing.T) {
		logger, buf := captureSlog(t)
		hook := &LoggingHook{Logger: logger}
		ctx := WithRequestID(context.Background(), "TEST-REL-HOOKS-01-LOG")
		req := &canonical.ChatRequest{
			Model: "auto",
			Messages: []canonical.Message{
				{Role: canonical.RoleUser},
			},
		}

		if _, err := hook.Before(ctx, req); err != nil {
			t.Fatalf("Before: unexpected err: %v", err)
		}
		// Sanity: Before stored the entry.
		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-LOG"); !ok {
			t.Fatalf("startTimes entry not present after Before — test scaffold drift")
		}

		// Post-fix engine error path calls After with nil resp. Must
		// not panic and must reclaim the entry via LoadAndDelete.
		if err := hook.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil resp): unexpected err: %v", err)
		}

		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-LOG"); ok {
			t.Errorf("startTimes entry leaked after After(nil resp) — G-1 regression")
		}

		// Sanity: After did emit a plugin.after record (so observability
		// is preserved on the error path).
		recs := decodeRecords(t, buf)
		sawAfter := false
		for _, r := range recs {
			if r["msg"] == "plugin.after" {
				sawAfter = true
				// stop_reason MUST be absent when resp is nil — the After
				// implementation must guard the attr append on resp != nil.
				if _, present := r["stop_reason"]; present {
					t.Errorf("plugin.after carries stop_reason when resp was nil: %+v", r)
				}
			}
		}
		if !sawAfter {
			t.Errorf("expected plugin.after record on error path After(nil resp); got: %+v", recs)
		}
		buf.Reset()
	})

	t.Run("ChatTraceHook reclaims startTimes on nil resp", func(t *testing.T) {
		var w bytes.Buffer
		hook := &ChatTraceHook{Writer: &w, Enabled: true}
		ctx := WithRequestID(context.Background(), "TEST-REL-HOOKS-01-TRACE")
		req := &canonical.ChatRequest{
			Model: "auto",
			Messages: []canonical.Message{
				{Role: canonical.RoleUser},
			},
		}

		if _, err := hook.Before(ctx, req); err != nil {
			t.Fatalf("Before: unexpected err: %v", err)
		}
		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-TRACE"); !ok {
			t.Fatalf("startTimes entry not present after Before — test scaffold drift")
		}

		// Post-fix engine error path calls After with nil resp. Must
		// not panic and must reclaim the entry via LoadAndDelete.
		if err := hook.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil resp): unexpected err: %v", err)
		}

		if _, ok := hook.startTimes.Load("TEST-REL-HOOKS-01-TRACE"); ok {
			t.Errorf("startTimes entry leaked after After(nil resp) — G-1 regression")
		}

		// Sanity: trace writer received the post_chain_out record (so
		// chat-trace.log is complete for failed requests too).
		if w.Len() == 0 {
			t.Errorf("expected chat-trace post_chain_out write on After(nil resp); got empty buffer")
		}
		// Discard writer contents to avoid bleed across subtests.
		_, _ = io.Copy(io.Discard, &w)
	})
}

type hookOncePre struct {
	hook  engine.PreHook
	calls int
}

func (h *hookOncePre) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	h.calls++
	return h.hook.Before(ctx, req)
}

type hookOncePost struct {
	hook      engine.PostHook
	calls     int
	lastResp  *canonical.ChatResponse
	lastIsNil bool
}

func (h *hookOncePost) After(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	h.calls++
	h.lastResp = resp
	h.lastIsNil = resp == nil
	return h.hook.After(ctx, req, resp)
}

type hookOnceStream struct {
	chunks <-chan canonical.Chunk
	final  *canonical.FinalResult
}

func newHookOnceStream(chunks ...canonical.Chunk) *hookOnceStream {
	ch := make(chan canonical.Chunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return &hookOnceStream{
		chunks: ch,
		final: &canonical.FinalResult{
			SessionID:  "hook-once-session",
			ChunkCount: len(chunks),
			StopReason: canonical.StopEndTurn,
		},
	}
}

func (s *hookOnceStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *hookOnceStream) Result() (*canonical.FinalResult, error) { return s.final, nil }

type hookOnceACP struct {
	mu sync.Mutex

	failCorrection bool
	promptBlocks   [][]canonical.Block
	transformedPII string
	newSessions    int
	setModels      int
	sequenceBegins int
	sequenceEnds   int
	cancels        int
}

func (a *hookOnceACP) NewSession(context.Context, string) (string, error) {
	a.mu.Lock()
	a.newSessions++
	a.mu.Unlock()
	return "hook-once-session", nil
}

func (a *hookOnceACP) SetModel(context.Context, string, string) error {
	a.mu.Lock()
	a.setModels++
	a.mu.Unlock()
	return nil
}

func (a *hookOnceACP) BeginPromptSequence(string) (func(), error) {
	a.mu.Lock()
	a.sequenceBegins++
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		a.sequenceEnds++
		a.mu.Unlock()
	}, nil
}

func (a *hookOnceACP) Prompt(_ context.Context, _ string, blocks []canonical.Block) (engine.Stream, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.promptBlocks = append(a.promptBlocks, cloneHookOnceBlocks(blocks))
	promptNumber := len(a.promptBlocks)
	if promptNumber == 1 {
		token := encryptedEmailToken(hookOnceBlocksText(blocks))
		if token == "" {
			return nil, errors.New("first prompt did not contain transformed email token")
		}
		a.transformedPII = token
		return newHookOnceStream(canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: "no required call for " + token},
		}), nil
	}
	if promptNumber != 2 || a.transformedPII == "" {
		return nil, errors.New("unexpected corrective prompt sequence")
	}
	token := a.transformedPII
	if a.failCorrection {
		return newHookOnceStream(canonical.Chunk{
			Kind: canonical.ChunkKindText,
			Text: &canonical.TextChunk{Content: "still no required call for " + token},
		}), nil
	}
	return newHookOnceStream(canonical.Chunk{
		Kind: canonical.ChunkKindToolCall,
		ToolCall: &canonical.ToolCallChunk{
			ID:   "hook-once-call",
			Name: "send_status",
			Args: map[string]any{"email": token},
		},
	}), nil
}

func (a *hookOnceACP) Cancel(string) {
	a.mu.Lock()
	a.cancels++
	a.mu.Unlock()
}

func cloneHookOnceBlocks(blocks []canonical.Block) []canonical.Block {
	cloned := make([]canonical.Block, len(blocks))
	for i, block := range blocks {
		cloned[i] = block
		if block.Text != nil {
			text := *block.Text
			cloned[i].Text = &text
		}
	}
	return cloned
}

func hookOnceBlocksText(blocks []canonical.Block) string {
	var text strings.Builder
	for _, block := range blocks {
		if block.Text != nil {
			text.WriteString(block.Text.Content)
		}
	}
	return text.String()
}

func encryptedEmailToken(text string) string {
	const prefix = "[PII:Email:"
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(text[start:], ']')
	if end < 0 {
		return ""
	}
	return text[start : start+end+1]
}

type hookOnceHarness struct {
	engine         *engine.Engine
	ctx            context.Context
	requestID      string
	request        *canonical.ChatRequest
	acp            *hookOnceACP
	compress       *compress.Hook
	logging        *LoggingHook
	trace          *ChatTraceHook
	traceOutput    *bytes.Buffer
	pre            map[string]*hookOncePre
	post           map[string]*hookOncePost
	restorations   *int
	modelRequests  *int
	protocolEvents *int
}

func newHookOnceHarness(t *testing.T, failCorrection bool) *hookOnceHarness {
	t.Helper()
	restorations := 0
	modelRequests := 0
	protocolEvents := 0
	service, err := privacy.NewService(privacy.Config{
		DefaultProfile:  privacy.ProfileStandard,
		RequestProfiles: []privacy.Profile{privacy.ProfileStandard},
		PIIEnabled:      true,
		PIIMode:         privacy.ActionEncrypt,
		PIIEncryptKey:   []byte("0123456789abcdef0123456789abcdef"),
		Recognizers:     piiplugin.SourceAuditNames(),
		Classifier:      piiplugin.NewPIIClassifier(piiplugin.Recognizers, nil, false),
		Observers: privacy.Observers{Restoration: func(_ privacy.Profile, _ string, result string) {
			if result == "pass" {
				restorations++
			}
		}},
	})
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)

	logger, _ := captureSlog(t)
	compression := &compress.Hook{
		Enabled:       true,
		TriggerTokens: 1,
		BudgetTokens:  32,
		ProtectTail:   1,
		ToolKeep:      16,
		Logger:        logger,
	}
	logging := &LoggingHook{Logger: logger}
	traceOutput := &bytes.Buffer{}
	trace := &ChatTraceHook{Writer: traceOutput, Enabled: true, Logger: logger, Privacy: service}
	piiHook := &piiplugin.PIIRedactionHook{Service: service}
	requestIDHook := &RequestIDHook{Logger: logger}

	pre := map[string]*hookOncePre{
		"trace":       {hook: trace},
		"request_id":  {hook: requestIDHook},
		"compression": {hook: compression},
		"pii":         {hook: piiHook},
		"logging":     {hook: logging},
	}
	post := map[string]*hookOncePost{
		"pii":     {hook: piiHook},
		"logging": {hook: logging},
		"trace":   {hook: trace},
	}
	acpClient := &hookOnceACP{failCorrection: failCorrection}
	eng := engine.New(engine.Config{
		Logger: logger,
		ACP:    acpClient,
		OnModelRequest: func(string) {
			modelRequests++
		},
		OnToolProtocolEvent: func(engine.ToolProtocolEvent) {
			protocolEvents++
		},
		PreHooks: []engine.PreHook{
			pre["trace"], pre["request_id"], pre["compression"], pre["pii"], pre["logging"],
		},
		PostHooks: []engine.PostHook{post["pii"], post["logging"], post["trace"]},
	})

	requestID := NewRequestID()
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "standard",
		ScopeID:          "hook-once-recovery",
		Surface:          "openai",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	ctx = piiplugin.WithSummary(ctx, piiplugin.NewSummary())
	ctx = WithSurface(WithRequestID(ctx, requestID), "openai")
	request := &canonical.ChatRequest{
		Model: "selected-model",
		Messages: []canonical.Message{
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: strings.Repeat("old context with blank lines\n\n\n\n", 80),
				}},
			},
			{
				Role:       canonical.RoleTool,
				ToolCallID: "old-call",
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: strings.Repeat("stale tool output ", 200),
				}},
			},
			{
				Role: canonical.RoleUser,
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: "Send status to corey@example.com",
				}},
			},
		},
		Tools:      []canonical.ToolSpec{{Name: "send_status", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: &canonical.ToolChoice{Type: "required"},
	}

	return &hookOnceHarness{
		engine: eng, ctx: ctx, requestID: requestID, request: request,
		acp: acpClient, compress: compression, logging: logging, trace: trace,
		traceOutput: traceOutput, pre: pre, post: post, restorations: &restorations,
		modelRequests: &modelRequests, protocolEvents: &protocolEvents,
	}
}

func TestToolProtocolRecovery_HookOnceRealChainCorrectedResponse(t *testing.T) {
	const sensitiveMarker = "corey@example.com"
	h := newHookOnceHarness(t, false)

	resp, err := h.engine.Collect(h.ctx, h.request)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	assertHookOnceCounts(t, h, false, resp)

	stats := h.compress.Stats()
	if stats.Eligible != 1 || stats.Runs != 1 || stats.SavedTokens <= 0 {
		t.Fatalf("compression stats = %+v, want one eligible run with savings", stats)
	}
	token := encryptedEmailToken(hookOnceBlocksText(h.acp.promptBlocks[0]))
	if token == "" || h.acp.transformedPII != token {
		t.Fatalf("PII token captured by ACP = %q, want first transformed prompt token %q", h.acp.transformedPII, token)
	}
	if got := h.request.Messages[len(h.request.Messages)-1].Content[0].Text; got != "Send status to "+token {
		t.Fatalf("transformed canonical request changed during two-prompt recovery: %q", got)
	}
	if len(h.acp.promptBlocks) != 2 {
		t.Fatalf("ACP prompts = %d, want first attempt plus one correction", len(h.acp.promptBlocks))
	}
	if corrective := hookOnceBlocksText(h.acp.promptBlocks[1]); strings.Contains(corrective, sensitiveMarker) || strings.Contains(corrective, token) {
		t.Fatalf("corrective prompt carried request-sensitive material: %q", corrective)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Arguments["email"] != sensitiveMarker {
		t.Fatalf("corrected response was not restored exactly once: %+v", resp.Message.ToolCalls)
	}
	if *h.restorations != 1 {
		t.Fatalf("successful PII restorations = %d, want 1", *h.restorations)
	}
}

func TestToolProtocolRecovery_HookOnceRealChainTerminalErrorCleanup(t *testing.T) {
	const sensitiveMarker = "corey@example.com"
	h := newHookOnceHarness(t, true)

	resp, err := h.engine.Collect(h.ctx, h.request)
	if resp != nil || err == nil {
		t.Fatalf("Collect = (%v, %v), want nil terminal protocol error", resp, err)
	}
	var selected *canonical.SelectedModelError
	if !errors.As(err, &selected) || selected.Code != canonical.CodeSelectedModelToolProtocolFailed {
		t.Fatalf("Collect error = %v, want selected-model protocol error", err)
	}
	assertHookOnceCounts(t, h, true, nil)
	if *h.restorations != 0 {
		t.Fatalf("terminal nil response performed %d restorations, want 0", *h.restorations)
	}
	if len(h.acp.promptBlocks) != 2 || strings.Contains(hookOnceBlocksText(h.acp.promptBlocks[1]), sensitiveMarker) {
		t.Fatalf("terminal corrective prompt leaked sensitive marker: %+v", h.acp.promptBlocks)
	}
}

func assertHookOnceCounts(t *testing.T, h *hookOnceHarness, wantNilPost bool, wantResp *canonical.ChatResponse) {
	t.Helper()
	for name, hook := range h.pre {
		if hook.calls != 1 {
			t.Errorf("%s PreHook calls = %d, want 1", name, hook.calls)
		}
	}
	for name, hook := range h.post {
		if hook.calls != 1 || hook.lastIsNil != wantNilPost || hook.lastResp != wantResp {
			t.Errorf("%s PostHook = calls:%d nil:%t resp:%p, want calls:1 nil:%t resp:%p", name, hook.calls, hook.lastIsNil, hook.lastResp, wantNilPost, wantResp)
		}
	}
	if _, ok := h.logging.startTimes.Load(h.requestID); ok {
		t.Error("LoggingHook start-time entry leaked")
	}
	if _, ok := h.trace.startTimes.Load(h.requestID); ok {
		t.Error("ChatTraceHook start-time entry leaked")
	}
	if got := bytes.Count(h.traceOutput.Bytes(), []byte("\n")); got != 2 {
		t.Errorf("trace records = %d, want one Pre and one Post record", got)
	}
	if h.acp.newSessions != 1 || h.acp.setModels != 1 || h.acp.sequenceBegins != 1 || h.acp.sequenceEnds != 1 {
		t.Errorf("ACP lifecycle = sessions:%d models:%d begin:%d end:%d, want all 1", h.acp.newSessions, h.acp.setModels, h.acp.sequenceBegins, h.acp.sequenceEnds)
	}
	if *h.modelRequests != 1 || *h.protocolEvents != 1 {
		t.Errorf("recovery observations = model requests:%d protocol events:%d, want both 1", *h.modelRequests, *h.protocolEvents)
	}
}
