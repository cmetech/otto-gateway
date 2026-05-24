// Package engine — whitebox tests for Engine.Run + Engine.Collect with
// a fake ACPClient harness. Covers:
//
//   - Run happy path
//   - Cancel-on-error contract (PromptError, SetModelError) — D-05
//   - Model="" / "auto" skip-SetModel path
//   - PreHook short-circuit (no ACP touched) — Codex H-4
//   - PreHook empty-slice pass-through
//   - Codex H-4: PreHook short-circuit response body preserved end-to-end
//   - Codex H-5: PostHook executes, can mutate, errors propagate, runs
//     on a PreHook short-circuit response too
//
// The fakeACP harness implements engine.ACPClient and records call
// counts/args so tests can assert the contract. fakeStream implements
// engine.Stream with a controllable Chunks channel and canned Result.
//
// goleak.VerifyTestMain in testmain_test.go catches any test that
// leaves a goroutine running.
package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/testutil"
)

// --- fakeACP harness ---

type fakeACP struct {
	mu sync.Mutex

	// programmable behavior
	newSessionID    string
	newSessionErr   error
	setModelErr     error
	promptErr       error
	chunksToEmit    []canonical.Chunk
	finalResult     *canonical.FinalResult
	resultErr       error

	// recorded calls
	newSessionCalls []string  // cwds
	setModelCalls   []string  // model ids
	promptCalls     []string  // session ids
	cancelCalls     []string  // session ids
}

func (f *fakeACP) NewSession(_ context.Context, cwd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSessionCalls = append(f.newSessionCalls, cwd)
	if f.newSessionErr != nil {
		return "", f.newSessionErr
	}
	sid := f.newSessionID
	if sid == "" {
		sid = "fake-sid-1"
	}
	return sid, nil
}

func (f *fakeACP) SetModel(_ context.Context, _, modelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModelCalls = append(f.setModelCalls, modelID)
	return f.setModelErr
}

func (f *fakeACP) Prompt(_ context.Context, sessionID string, _ []canonical.Block) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptCalls = append(f.promptCalls, sessionID)
	if f.promptErr != nil {
		return nil, f.promptErr
	}

	// Build an in-memory closed-channel stream from the canned chunks.
	ch := make(chan canonical.Chunk, len(f.chunksToEmit))
	for _, c := range f.chunksToEmit {
		ch <- c
	}
	close(ch)

	final := f.finalResult
	if final == nil {
		final = &canonical.FinalResult{
			SessionID:  sessionID,
			ChunkCount: len(f.chunksToEmit),
			StopReason: canonical.StopEndTurn,
		}
	}
	return &fakeStream{
		ch:     ch,
		final:  final,
		resErr: f.resultErr,
	}, nil
}

func (f *fakeACP) Cancel(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls = append(f.cancelCalls, sessionID)
}

// --- fakeStream ---

type fakeStream struct {
	ch     chan canonical.Chunk
	final  *canonical.FinalResult
	resErr error
}

func (s *fakeStream) Chunks() <-chan canonical.Chunk { return s.ch }
func (s *fakeStream) Result() (*canonical.FinalResult, error) {
	return s.final, s.resErr
}

// --- fakePreHook / fakePostHook ---

type fakePreHook struct {
	resp   *canonical.ChatResponse
	err    error
	called bool
}

func (h *fakePreHook) Before(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	h.called = true
	return h.resp, h.err
}

type fakePostHook struct {
	mutate func(*canonical.ChatResponse)
	err    error
	called bool
}

func (h *fakePostHook) After(_ context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	h.called = true
	if h.mutate != nil {
		h.mutate(resp)
	}
	return h.err
}

// --- helpers ---

func newTestEngine(t *testing.T, ack *fakeACP, opts ...func(*Config)) *Engine {
	t.Helper()
	cfg := Config{
		Logger:     testutil.Logger(t),
		ACP:        ack,
		DefaultCWD: "/test/default/cwd",
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return New(cfg)
}

func withPreHooks(hooks ...PreHook) func(*Config) {
	return func(c *Config) { c.PreHooks = hooks }
}

func withPostHooks(hooks ...PostHook) func(*Config) {
	return func(c *Config) { c.PostHooks = hooks }
}

func simpleUserReq(text, model string) *canonical.ChatRequest {
	return &canonical.ChatRequest{
		Model: model,
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: text},
			}},
		},
	}
}

// --- Engine.Run tests ---

func TestEngineRun_Happy(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-happy",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	ctx := context.Background()

	resp, err := e.Collect(ctx, simpleUserReq("hi", "claude-sonnet-4-7"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(ack.newSessionCalls) != 1 {
		t.Errorf("NewSession calls: got %d, want 1", len(ack.newSessionCalls))
	}
	if len(ack.setModelCalls) != 1 || ack.setModelCalls[0] != "claude-sonnet-4-7" {
		t.Errorf("SetModel calls: got %v, want [claude-sonnet-4-7]", ack.setModelCalls)
	}
	if len(ack.promptCalls) != 1 || ack.promptCalls[0] != "sid-happy" {
		t.Errorf("Prompt calls: got %v, want [sid-happy]", ack.promptCalls)
	}
	if len(ack.cancelCalls) != 0 {
		t.Errorf("Cancel calls on happy path: got %v, want []", ack.cancelCalls)
	}
	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
	if len(resp.Message.Content) != 1 || resp.Message.Content[0].Text != "hello world" {
		t.Errorf("aggregated text: got %v, want 'hello world'", resp.Message.Content)
	}
}

func TestEngineRun_PromptError_CancelsSession(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-cancel-on-prompt",
		promptErr:    errors.New("simulated prompt failure"),
	}
	e := newTestEngine(t, ack)
	_, err := e.Run(context.Background(), simpleUserReq("hi", "model-x"))
	if err == nil {
		t.Fatal("Run: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("error message: got %q, want substring 'prompt'", err.Error())
	}
	if len(ack.cancelCalls) != 1 || ack.cancelCalls[0] != "sid-cancel-on-prompt" {
		t.Errorf("Cancel calls: got %v, want [sid-cancel-on-prompt] (D-05 + Pitfall 6)", ack.cancelCalls)
	}
}

func TestEngineRun_SetModelError_CancelsSession(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-cancel-on-setmodel",
		setModelErr:  errors.New("simulated set-model failure"),
	}
	e := newTestEngine(t, ack)
	_, err := e.Run(context.Background(), simpleUserReq("hi", "model-x"))
	if err == nil {
		t.Fatal("Run: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "set model") {
		t.Errorf("error message: got %q, want substring 'set model'", err.Error())
	}
	if len(ack.cancelCalls) != 1 || ack.cancelCalls[0] != "sid-cancel-on-setmodel" {
		t.Errorf("Cancel calls: got %v, want [sid-cancel-on-setmodel] (D-05 + Pitfall 6)", ack.cancelCalls)
	}
}

func TestEngineRun_ModelAuto_SkipsSetModel(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-auto",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
	}
	e := newTestEngine(t, ack)
	_, err := e.Collect(context.Background(), simpleUserReq("hi", "auto"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(ack.setModelCalls) != 0 {
		t.Errorf("SetModel was called for model='auto'; got %v (D-05: must skip)", ack.setModelCalls)
	}
}

func TestEngineRun_ModelEmpty_SkipsSetModel(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-empty",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
	}
	e := newTestEngine(t, ack)
	_, err := e.Collect(context.Background(), simpleUserReq("hi", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(ack.setModelCalls) != 0 {
		t.Errorf("SetModel was called for model=''; got %v (D-05: must skip)", ack.setModelCalls)
	}
}

func TestEngineRun_PreHookShortCircuits(t *testing.T) {
	hookResp := &canonical.ChatResponse{
		ID:    "from-hook",
		Model: "cached-model",
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "cached"},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	pre := &fakePreHook{resp: hookResp}
	ack := &fakeACP{}
	e := newTestEngine(t, ack, withPreHooks(pre))

	run, err := e.Run(context.Background(), simpleUserReq("hi", "anything"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !pre.called {
		t.Error("PreHook.Before was not called")
	}
	if len(ack.newSessionCalls) != 0 {
		t.Errorf("NewSession was called despite short-circuit; got %v", ack.newSessionCalls)
	}
	if run.response != hookResp {
		t.Error("Run.response is not the PreHook's response (short-circuit body not preserved on the Run handle)")
	}
}

func TestEngineRun_PreHookEmptySlice_PassesThrough(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-empty-prehooks",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
	}
	e := newTestEngine(t, ack, withPreHooks() /* zero hooks */)
	_, err := e.Collect(context.Background(), simpleUserReq("hi", "auto"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(ack.newSessionCalls) != 1 {
		t.Errorf("empty PreHooks should pass through; NewSession calls: %v", ack.newSessionCalls)
	}
}

// --- Codex H-4 + H-5 tests ---

// TestEngine_PreHookShortCircuit_ResponseBodyPreserved asserts that when
// a PreHook returns a non-nil ChatResponse, Engine.Collect returns
// that response body verbatim — proving chunk-assembly was bypassed
// AND the hook's payload is preserved through the Run handle's
// response field (Codex H-4 fix).
func TestEngine_PreHookShortCircuit_ResponseBodyPreserved(t *testing.T) {
	hookResp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "from hook"},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	pre := &fakePreHook{resp: hookResp}
	ack := &fakeACP{
		newSessionID: "should-not-be-used",
		chunksToEmit: []canonical.Chunk{
			// If chunk assembly ever runs, the response would be "leaked"
			// rather than "from hook".
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "leaked"}},
		},
	}
	e := newTestEngine(t, ack, withPreHooks(pre))

	resp, err := e.Collect(context.Background(), simpleUserReq("hi", "anything"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 || resp.Message.Content[0].Text != "from hook" {
		t.Errorf("short-circuit body NOT preserved; got %v, want 'from hook' (Codex H-4 regression)", resp.Message.Content)
	}
	if len(ack.newSessionCalls) != 0 {
		t.Errorf("ACP.NewSession was called despite PreHook short-circuit; got %v", ack.newSessionCalls)
	}
}

// TestEngine_PostHook_ResponseReplacement asserts that a PostHook's
// in-place mutation of the response is visible to the caller (Codex
// H-5 — D-04 contract that engine ranges PostHooks).
func TestEngine_PostHook_ResponseReplacement(t *testing.T) {
	post := &fakePostHook{
		mutate: func(resp *canonical.ChatResponse) {
			if len(resp.Message.Content) > 0 {
				resp.Message.Content[0].Text = "mutated by posthook"
			}
		},
	}
	ack := &fakeACP{
		newSessionID: "sid-posthook-mut",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "original"}},
		},
	}
	e := newTestEngine(t, ack, withPostHooks(post))

	resp, err := e.Collect(context.Background(), simpleUserReq("hi", "auto"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !post.called {
		t.Error("PostHook.After was not called")
	}
	if resp.Message.Content[0].Text != "mutated by posthook" {
		t.Errorf("PostHook mutation not visible; got %q, want 'mutated by posthook' (Codex H-5)", resp.Message.Content[0].Text)
	}
}

// TestEngine_PostHook_ErrorPropagation asserts that a PostHook returning
// a non-nil error aborts Collect with that error wrapped (Codex H-5).
func TestEngine_PostHook_ErrorPropagation(t *testing.T) {
	post := &fakePostHook{err: errors.New("posthook failed")}
	ack := &fakeACP{
		newSessionID: "sid-posthook-err",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
	}
	e := newTestEngine(t, ack, withPostHooks(post))

	_, err := e.Collect(context.Background(), simpleUserReq("hi", "auto"))
	if err == nil {
		t.Fatal("Collect: expected posthook error, got nil")
	}
	if !strings.Contains(err.Error(), "posthook failed") {
		t.Errorf("error did not include posthook message; got %q", err.Error())
	}
}

// TestEngine_PostHook_RunsOnPreHookShortCircuit asserts that PostHooks
// run AFTER a PreHook short-circuit response too — so logging/audit
// hooks still see the synthesized response (Codex H-5).
func TestEngine_PostHook_RunsOnPreHookShortCircuit(t *testing.T) {
	hookResp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "from prehook"},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	pre := &fakePreHook{resp: hookResp}
	post := &fakePostHook{
		mutate: func(resp *canonical.ChatResponse) {
			if len(resp.Message.Content) > 0 {
				resp.Message.Content[0].Text = "wrapped: " + resp.Message.Content[0].Text
			}
		},
	}
	ack := &fakeACP{}
	e := newTestEngine(t, ack, withPreHooks(pre), withPostHooks(post))

	resp, err := e.Collect(context.Background(), simpleUserReq("hi", "anything"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !post.called {
		t.Error("PostHook.After was not called on the short-circuit response (Codex H-5)")
	}
	if resp.Message.Content[0].Text != "wrapped: from prehook" {
		t.Errorf("PostHook didn't see the short-circuit body; got %q, want 'wrapped: from prehook'", resp.Message.Content[0].Text)
	}
	if len(ack.newSessionCalls) != 0 {
		t.Errorf("ACP was touched despite short-circuit; got %v", ack.newSessionCalls)
	}
}
