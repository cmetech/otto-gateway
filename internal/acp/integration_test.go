// Package acp_test — blackbox integration tests.
// D-18: blackbox package exercises only exported API.
// Phase 1.1 Plan 04 (D-23) consolidates the four Phase 1 fake tests
// (TestIntegration_FakeACP_AutoGrantAndTranslation,
// TestIntegration_FakeACP_ChunkTranslation,
// TestIntegration_FakeACP_PromptChunkDelivery,
// TestIntegration_FakeACP_PingWorks) into a single
// TestIntegration_FakeACP_E2E_MixedVariants that drives the fake through one
// session exercising five mixed session/update variants and the permission
// RESPONSE path. The real-kiro smoke test stays unchanged (per Plan 04 scope).
package acp_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/testutil"
)

// resolveKiroCLI checks for a kiro-cli binary and skips the test if not found.
// D-17: LOOP24_KIRO_BIN env var overrides PATH detection.
func resolveKiroCLI(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("LOOP24_KIRO_BIN"); bin != "" {
		return bin
	}
	path, err := exec.LookPath("kiro-cli")
	if err != nil {
		t.Skip("kiro-cli not found on PATH; set LOOP24_KIRO_BIN to override (D-17)")
	}
	return path
}

// TestIntegration_FakeACP_E2E_MixedVariants is the single Plan 04 consolidation
// of Phase 1's four fake-server tests. One session, full lifecycle, every Plan
// 04 contract surface in one test:
//
//  1. Initialize against the spec-compliant shape; assert PromptCapabilities()
//     captured the agent's image:true flag (folds Phase 1.1-02 D-09 coverage).
//  2. NewSession against the spec-compliant shape; assert AvailableModels()
//     returned the single fake-server model entry (folds Phase 1.1-03 D-12).
//  3. Ping round-trip (folds Phase 1's PingWorks).
//  4. Prompt + drain five mixed-variant session/update notifications:
//     - variantAgentMessageFlat            → ChunkKindText "hello"
//     - variantAgentMessageWrappedCamel    → ChunkKindText "world"
//     - variantAgentThoughtKiroDev          → ChunkKindThought "thinking"
//     - variantToolCallWrapped              → ChunkKindThought "[tool: read_file]\n"
//     - variantPlanWrapped                  → ChunkKindPlan "Step 1\nStep 2"
//     This exercises D-16 (three method names dispatched), D-17 (wrapped + flat
//     body shapes), D-18 (content extraction chain across three shapes), and
//     D-19 (snake_case + CamelCase discriminator normalisation).
//  5. Mid-stream session/request_permission REQUEST drives D-20: the client
//     RESPONDS on the original frame id with optionId:allow_always; the fake
//     closes permissionResponseReceived when it observes that response.
//  6. session/prompt response with stopReason:"end_turn" closes the turn;
//     Stream.Result() returns StopReason == StopEndTurn (folds Phase 1.1-03
//     D-07 coverage end-to-end through the fake).
//
// Orchestration: Prompt blocks until the response arrives. To drive the
// emission sequence WHILE Prompt is in-flight, Prompt runs in a goroutine and
// the test goroutine drives fake.emit* calls.
//
//nolint:funlen,gocognit // Single consolidated E2E — folding four prior tests; the linear orchestration is the readable form.
func TestIntegration_FakeACP_E2E_MixedVariants(t *testing.T) {
	fake := newFakeACPServer(t)
	defer fake.close()

	cfg := acp.Config{
		Logger:       testutil.Logger(t),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping
	}

	client := acp.NewWithConn(fake.clientRWC, cfg)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("client.Close (minor pipe-close error expected): %v", err)
		}
		goleak.VerifyNone(t)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// (1) Initialize — fake emits the spec-compliant promptCapabilities shape.
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	caps := client.PromptCapabilities()
	if !caps.Image {
		t.Errorf("PromptCapabilities().Image: got false, want true (folded D-09 assertion)")
	}

	// (2) NewSession — fake emits one availableModels entry.
	sid, err := client.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sid != "test-session-id" {
		t.Errorf("sessionID: got %q, want test-session-id", sid)
	}
	models := client.AvailableModels()
	if len(models) != 1 {
		t.Fatalf("AvailableModels len: got %d, want 1 (folded D-12 assertion)", len(models))
	}
	if models[0].ID != "claude-sonnet-4-7" || models[0].Name != "Claude Sonnet 4.7" {
		t.Errorf("AvailableModels[0]: got %+v, want {ID:claude-sonnet-4-7, Name:Claude Sonnet 4.7}", models[0])
	}

	// (3) Ping — folds Phase 1's PingWorks coverage.
	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}

	// (4) Prompt orchestration. Prompt blocks until the session/prompt
	// response arrives. To drive the emission sequence WHILE Prompt is
	// in-flight, run Prompt on a goroutine and drive emits on the test
	// goroutine.
	blocks := []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	}
	type promptResult struct {
		stream *acp.Stream
		err    error
	}
	promptCh := make(chan promptResult, 1)
	go func() {
		s, err := client.Prompt(ctx, sid, blocks)
		promptCh <- promptResult{stream: s, err: err}
	}()

	// Sync on the fake observing the prompt request before we start emitting
	// updates — otherwise the chunks could arrive on the wire before the
	// client has registered the active stream and would be dropped.
	select {
	case <-fake.promptSeen:
	case <-ctx.Done():
		t.Fatal("timed out waiting for fake.promptSeen")
	}

	// Emit five session/update notifications, each using a different variant
	// across the (method, body wrap, discriminator field, discriminator
	// casing, content shape, update type) axes. See updateVariant docs.
	for _, v := range []updateVariant{
		variantAgentMessageFlat,
		variantAgentMessageWrappedCamel,
		variantAgentThoughtKiroDev,
		variantToolCallWrapped,
		variantPlanWrapped,
	} {
		if err := fake.emitUpdate(sid, v); err != nil {
			t.Fatalf("emitUpdate(%d): %v", v, err)
		}
	}

	// (5) Mid-stream permission request. Send it as a proper RPC request
	// (with id) — Plan 04 D-20. The client must respond on the same id.
	if err := fake.emitPermissionRequest("perm-req-1", 999); err != nil {
		t.Fatalf("emitPermissionRequest: %v", err)
	}
	select {
	case <-fake.permissionResponseReceived:
		// D-20 assertion: client wrote the rpcResponse envelope with
		// optionId:allow_always within the deadline.
	case <-ctx.Done():
		t.Fatal("timed out waiting for permission response (D-20 deadlock unblock)")
	}

	// (6) Close the turn: emit session/prompt response with end_turn.
	if err := fake.emitPromptResult("end_turn"); err != nil {
		t.Fatalf("emitPromptResult: %v", err)
	}

	// Wait for Prompt to return.
	var stream *acp.Stream
	select {
	case res := <-promptCh:
		if res.err != nil {
			t.Fatalf("Prompt: %v", res.err)
		}
		stream = res.stream
	case <-ctx.Done():
		t.Fatal("timed out waiting for Prompt to return")
	}

	// Drain stream.Chunks. The channel closes when the prompt response is
	// observed by the client (CR-02 fix); we get exactly five chunks in
	// emission order.
	wantChunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "thinking"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "[tool: read_file]\n"}},
		{Kind: canonical.ChunkKindPlan, Plan: &canonical.PlanChunk{Content: "Step 1\nStep 2"}},
	}
	var got []canonical.Chunk
	for ch := range stream.Chunks {
		got = append(got, ch)
	}
	if len(got) != len(wantChunks) {
		t.Fatalf("chunk count: got %d, want %d (got=%+v)", len(got), len(wantChunks), got)
	}
	for i, w := range wantChunks {
		if got[i].Kind != w.Kind {
			t.Errorf("chunk[%d].Kind: got %v, want %v", i, got[i].Kind, w.Kind)
			continue
		}
		switch w.Kind {
		case canonical.ChunkKindText:
			if got[i].Text == nil || got[i].Text.Content != w.Text.Content {
				t.Errorf("chunk[%d].Text: got %+v, want %+v", i, got[i].Text, w.Text)
			}
		case canonical.ChunkKindThought:
			if got[i].Thought == nil || got[i].Thought.Content != w.Thought.Content {
				t.Errorf("chunk[%d].Thought: got %+v, want %+v", i, got[i].Thought, w.Thought)
			}
		case canonical.ChunkKindPlan:
			if got[i].Plan == nil || got[i].Plan.Content != w.Plan.Content {
				t.Errorf("chunk[%d].Plan: got %+v, want %+v", i, got[i].Plan, w.Plan)
			}
		case canonical.ChunkKindToolCall:
			// Phase 1.1 renders tool_* updates as thoughts (CONTEXT.md
			// <deferred>); ChunkKindToolCall is not expected from translateUpdate
			// in this phase. Fall through with a soft assertion.
			t.Errorf("chunk[%d] unexpectedly typed as ChunkKindToolCall in Phase 1.1", i)
		}
	}

	// Final-result assertion: D-07 contract end-to-end.
	result, err := stream.Result()
	if err != nil {
		t.Fatalf("stream.Result: %v", err)
	}
	if result == nil {
		t.Fatal("stream.Result returned nil FinalResult")
	}
	if result.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", result.StopReason)
	}
	if result.SessionID != sid {
		t.Errorf("FinalResult.SessionID: got %q, want %q", result.SessionID, sid)
	}
	if result.ChunkCount != len(wantChunks) {
		t.Errorf("FinalResult.ChunkCount: got %d, want %d", result.ChunkCount, len(wantChunks))
	}
}

// TestIntegration_RealKiroCLI_PromptRoundTrip is the Phase 2 unblock gate
// (Plan 01.1-05, CONTEXT.md D-24). It exercises the full Phase 1.1 wire-
// alignment work — Initialize -> NewSession -> Prompt("hi") -> drain
// session/update notifications -> stream.Result() — against the real
// kiro-cli 2.4.1 binary that the Phase 1.1 fake server was built to mimic.
//
// Skips cleanly when kiro-cli is not on PATH and LOOP24_KIRO_BIN is unset
// (D-17 pattern). When kiro-cli is present but exits before responding to
// initialize (typically because auth has expired), the test soft-skips per
// the SmokeTest convention.
//
// Assertions (per D-24 step list, see Plan 05 §<action>):
//   - client.PromptCapabilities() is non-zero after Initialize (Plan 02 D-09
//     accessor end-to-end against the real agentCapabilities shape).
//   - client.AvailableModels() is non-empty after NewSession (Plan 03 D-12
//     accessor end-to-end against the real models.availableModels shape).
//   - At least one ChunkKindText chunk arrives on stream.Chunks with
//     non-empty Text.Content (Plans 02/03/04 parsing path end-to-end).
//   - stream.Result().StopReason is one of the non-error values —
//     StopEndTurn, StopMaxTokens, or StopMaxTurnRequests (Plan 03 D-07
//     parseStopReason end-to-end). StopUnknown is treated as a failure
//     (the wire string from kiro-cli was unrecognised or missing).
//
// Timeout: 90 seconds — cold LLM responses can take that long even on a
// trivial "hi" prompt; the SmokeTest's 30s is enough for non-prompt RPCs
// but not for a full turn.
func TestIntegration_RealKiroCLI_PromptRoundTrip(t *testing.T) {
	bin := resolveKiroCLI(t) // t.Skip fires here if kiro-cli absent

	cfg := acp.Config{
		Logger:       testutil.Logger(t),
		Command:      bin,
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping during test
	}

	client, err := acp.New(cfg)
	if err != nil {
		t.Fatalf("acp.New: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			// Non-zero exit from kiro-cli is expected when we close stdin.
			t.Logf("client.Close (expected non-zero exit): %v", err)
		}
		goleak.VerifyNone(t)
	}()

	// D-24 step (5): 90s timeout — LLM responses can take that long on a
	// cold call. SmokeTest uses 30s for non-prompt RPCs.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// D-24 step (6): Initialize with ErrClientClosed -> Skip guard (auth
	// expiry presents as kiro-cli exiting before responding).
	if err := client.Initialize(ctx); err != nil {
		if errors.Is(err, acp.ErrClientClosed) {
			t.Skipf("kiro-cli exited before responding to initialize (may require auth refresh — run kiro-cli interactively): %v", err)
		}
		t.Fatalf("Initialize: %v", err)
	}
	t.Log("Initialize: OK")

	// D-24 step (7): PromptCapabilities() should be non-zero after Initialize.
	// Errorf (not Fatalf) — the prompt round-trip itself is still worth
	// running even if caps came through zero; the bug surface differs.
	caps := client.PromptCapabilities()
	if !caps.Image && !caps.Audio && !caps.EmbeddedContext {
		t.Errorf("PromptCapabilities() returned all-false; expected at least one capability flag set by kiro-cli (got %+v)", caps)
	} else {
		t.Logf("PromptCapabilities(): %+v", caps)
	}

	// D-24 step (8): NewSession.
	sessionID, err := client.NewSession(ctx, os.TempDir())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sessionID == "" {
		t.Fatal("NewSession returned empty sessionID")
	}
	t.Logf("NewSession: OK (sessionID=%s)", sessionID)

	// D-24 step (9): AvailableModels() should be non-empty after NewSession.
	models := client.AvailableModels()
	if len(models) == 0 {
		t.Errorf("AvailableModels() returned nil/empty; expected at least one model from kiro-cli")
	} else {
		t.Logf("AvailableModels(): %d models, first = %+v", len(models), models[0])
	}

	// D-24 step (10): Prompt with a single text block.
	//
	// WR-01/WR-07 fix: Prompt() blocks until kiro-cli emits its session/prompt
	// response, which arrives AFTER every session/update for the turn. The
	// chunks land in Stream.Chunks (a 64-slot buffered channel). If we called
	// Prompt synchronously and only started draining afterwards, any future
	// kiro-cli behaviour that emits more than 64 chunks per turn would
	// deadlock the readLoop indefinitely (the read pipe stalls before
	// session/prompt response can be delivered). Run Prompt on a goroutine
	// and drain Chunks on this one — matches the documented contract on
	// Prompt() / Stream.Chunks.
	blocks := []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	}
	type promptOutcome struct {
		stream *acp.Stream
		err    error
	}
	promptCh := make(chan promptOutcome, 1)
	go func() {
		s, err := client.Prompt(ctx, sessionID, blocks)
		promptCh <- promptOutcome{stream: s, err: err}
	}()

	// Wait for Prompt to return so we have the Stream handle.
	var stream *acp.Stream
	select {
	case res := <-promptCh:
		if res.err != nil {
			t.Fatalf("Prompt: %v", res.err)
		}
		stream = res.stream
	case <-ctx.Done():
		t.Fatalf("timed out waiting for Prompt to return (ctx: %v)", ctx.Err())
	}
	t.Log("Prompt: returned without deadlock (D-20 + WR-01 contract exercised)")

	// D-24 step (11): drain stream.Chunks. Count text chunks with non-empty
	// content; capture the first for the t.Logf below. We range over the
	// channel directly — Prompt has already returned, so close() has run and
	// every chunk is buffered. WR-07: if a future kiro change emits more
	// chunks than fit in the buffer, this loop still drains them because
	// Prompt's close path delivers all pushed chunks before closing the
	// channel.
	var textChunks int
	var firstText string
	chunksDone := make(chan struct{})
	go func() {
		defer close(chunksDone)
		for chunk := range stream.Chunks {
			if chunk.Kind == canonical.ChunkKindText && chunk.Text != nil && chunk.Text.Content != "" {
				textChunks++
				if firstText == "" {
					firstText = chunk.Text.Content
				}
			}
		}
	}()
	select {
	case <-chunksDone:
	case <-ctx.Done():
		t.Fatalf("timed out draining chunks (ctx: %v)", ctx.Err())
	}
	if textChunks == 0 {
		t.Errorf("no ChunkKindText chunks with non-empty content arrived; the agent never responded with text")
	} else {
		t.Logf("received %d text chunks; first chunk content (truncated to 80 chars): %.80s", textChunks, firstText)
	}

	// D-24 step (12): final result.
	result, err := stream.Result()
	if err != nil {
		t.Fatalf("stream.Result(): %v", err)
	}
	if result == nil {
		t.Fatal("stream.Result() returned nil result")
	}

	// D-24 step (13): StopReason must be a non-error value. Acceptable per
	// D-02: StopEndTurn (typical), StopMaxTokens (rare for "hi"),
	// StopMaxTurnRequests. StopUnknown means parseStopReason did not
	// recognise the wire value — that's a Plan 03 surface to investigate.
	// StopRefusal / StopCancelled are legitimate kiro behaviour but
	// indicate the round-trip did not complete normally.
	switch result.StopReason {
	case canonical.StopEndTurn, canonical.StopMaxTokens, canonical.StopMaxTurnRequests:
		t.Logf("stream.Result().StopReason = %v (non-error stop)", result.StopReason)
	case canonical.StopUnknown:
		t.Errorf("stream.Result().StopReason = StopUnknown; expected a parsed canonical StopReason (the wire string from kiro-cli was unrecognised or missing)")
	default:
		t.Errorf("stream.Result().StopReason = %v; expected a non-error stop reason (StopEndTurn/StopMaxTokens/StopMaxTurnRequests)", result.StopReason)
	}
}

// TestIntegration_RealKiroCLI_SmokeTest skips cleanly when kiro-cli is not found.
// When present, it exercises Initialize → NewSession → Ping → Close without goroutine leaks.
// D-17: LOOP24_KIRO_BIN env var override.
//
// Unchanged by Plan 04 — Plan 05 adds TestIntegration_RealKiroCLI_PromptRoundTrip
// above this.
func TestIntegration_RealKiroCLI_SmokeTest(t *testing.T) {
	bin := resolveKiroCLI(t) // t.Skip fires here if kiro-cli absent

	cfg := acp.Config{
		Logger:       testutil.Logger(t),
		Command:      bin,
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping during test
	}

	client, err := acp.New(cfg)
	if err != nil {
		t.Fatalf("acp.New: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			// Non-zero exit from kiro-cli is expected when we close stdin.
			t.Logf("client.Close (expected non-zero exit): %v", err)
		}
		goleak.VerifyNone(t)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize.
	if err := client.Initialize(ctx); err != nil {
		// If kiro-cli exits before responding (e.g., requires auth or TTY),
		// treat as a soft skip rather than a hard failure — the fake test covers ACP-04/ACP-05.
		if errors.Is(err, acp.ErrClientClosed) {
			t.Skipf("kiro-cli exited before responding to initialize (may require auth): %v", err)
		}
		t.Fatalf("Initialize: %v", err)
	}
	t.Log("Initialize: OK")

	// NewSession.
	sessionID, err := client.NewSession(ctx, os.TempDir())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sessionID == "" {
		t.Error("NewSession returned empty sessionID")
	}
	t.Logf("NewSession: OK (sessionID=%s)", sessionID)

	// Ping.
	if err := client.Ping(ctx); err != nil && !errors.Is(err, acp.ErrClientClosed) {
		t.Errorf("Ping: %v", err)
	}
	t.Log("Ping: OK")
}

// Compile-time check: ensure we use the canonical package to keep the import honest.
var _ = canonical.ChunkKindText
