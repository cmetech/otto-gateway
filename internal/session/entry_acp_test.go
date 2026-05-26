// Package session_test — blackbox tests for the Entry / Registry
// protocol-sequence guards added in plan 05-04.
//
// Scope (binding, per plan 05-04 MEDIUM-3): these tests GUARD the chosen
// protocol sequence against regression. They do NOT prove the fix works
// against real kiro-cli — the live e2e suite is the authority. The same
// blind spot that hid the original SC3 bug (fake-ACP doesn't exercise
// real kiro-cli) means fake-ACP tests alone cannot be the verification
// gate.
//
// Test set is shaped for the H-B outcome confirmed in
// .planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md:
//
//   - The pool path passes a non-empty cwd to kiro-cli's session/new
//     (resolved via engine.pickCwd's os.Getwd() fallback).
//   - The session path was passing cwd="" because resolveEngine forwards
//     cfg.KiroCWD verbatim and KIRO_CWD defaults to "". kiro-cli's
//     session/prompt rejects every prompt against an empty-cwd session
//     with rpc error -32603 "Improperly formed request".
//   - Fix: registry.createEntry must resolve empty cwd to os.Getwd() so
//     the registry's session/new always carries a non-empty cwd, matching
//     the pool path's behaviour.
//
// H-A (cached-sid reuse) was explicitly REJECTED — Entry.NewSession's
// cached-SessionID accessor stays. Test 4 below pins that behaviour so
// a future refactor cannot silently "fix" it and break two-turn
// continuity.
package session_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// recordingClient is a PoolClient-shaped fake that records every call's
// arguments under a mutex so concurrent assertions are race-free. It is
// distinct from registry_test.go's fakeClient so tests in this file can
// own their own scripting without coupling.
type recordingClient struct {
	newSessionFn func(ctx context.Context, cwd string) (string, error)

	mu               sync.Mutex
	newSessionCalls  []string // recorded cwd args
	promptCalls      []string // recorded sessionID args
	cancelCalls      []string // recorded sessionID args
	initializeCalls  int
	setModelCalls    int
	closeCallCount   int
	doneCh           chan struct{}
	doneInitialiseOK bool
}

func (rc *recordingClient) Initialize(_ context.Context) error {
	rc.mu.Lock()
	rc.initializeCalls++
	rc.mu.Unlock()
	return nil
}

func (rc *recordingClient) NewSession(ctx context.Context, cwd string) (string, error) {
	rc.mu.Lock()
	rc.newSessionCalls = append(rc.newSessionCalls, cwd)
	fn := rc.newSessionFn
	rc.mu.Unlock()
	if fn != nil {
		return fn(ctx, cwd)
	}
	return "fake-sess-1", nil
}

func (rc *recordingClient) SetModel(_ context.Context, _ string, _ string) error {
	rc.mu.Lock()
	rc.setModelCalls++
	rc.mu.Unlock()
	return nil
}

func (rc *recordingClient) Prompt(_ context.Context, sid string, _ []canonical.Block) (*acp.Stream, error) {
	rc.mu.Lock()
	rc.promptCalls = append(rc.promptCalls, sid)
	rc.mu.Unlock()
	s := acp.NewStreamForTest(sid)
	s.CloseForTest(&acp.FinalResult{StopReason: canonical.StopEndTurn}, nil)
	return s, nil
}

func (rc *recordingClient) Cancel(sid string) {
	rc.mu.Lock()
	rc.cancelCalls = append(rc.cancelCalls, sid)
	rc.mu.Unlock()
}

func (rc *recordingClient) Close() error {
	rc.mu.Lock()
	rc.closeCallCount++
	rc.mu.Unlock()
	return nil
}

func (rc *recordingClient) AvailableModels() []canonical.ModelInfo { return nil }

func (rc *recordingClient) Done() <-chan struct{} {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !rc.doneInitialiseOK {
		rc.doneCh = make(chan struct{})
		rc.doneInitialiseOK = true
	}
	return rc.doneCh
}

func (rc *recordingClient) snapshotNewSessionCalls() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]string, len(rc.newSessionCalls))
	copy(out, rc.newSessionCalls)
	return out
}

// recordingFactory hands out a single recordingClient per Spawn call.
// Tests pre-populate clients in order.
type recordingFactory struct {
	mu      sync.Mutex
	clients []*recordingClient
	idx     int
}

func (rf *recordingFactory) Spawn(_ context.Context, _ acp.Config) (session.PoolClient, error) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.idx >= len(rf.clients) {
		return nil, errors.New("recordingFactory: no more clients in script")
	}
	c := rf.clients[rf.idx]
	rf.idx++
	return c, nil
}

// TestRegistry_CreateEntry_ResolvesEmptyCwdToOSGetwd is the load-bearing
// regression guard for plan 05-04's H-B fix. A Registry constructed with
// no KiroCWD and Get called with cwd="" MUST cause the fake's NewSession
// to be invoked with a NON-empty cwd (the test process's os.Getwd()) —
// matching the pool path's behaviour via engine.pickCwd's os.Getwd()
// fallback.
//
// If this test ever fails, the SC3 wire-protocol bug from
// 05-04-WIRE-DIFF.md has regressed: kiro-cli's session/prompt will
// return -32603 against every stateful request.
func TestRegistry_CreateEntry_ResolvesEmptyCwdToOSGetwd(t *testing.T) {
	rc := &recordingClient{}
	rf := &recordingFactory{clients: []*recordingClient{rc}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: rf,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Caller-supplied cwd is empty (as resolveEngine would do when
	// KIRO_CWD env var is unset).
	if _, err := r.Get(ctx, "sid-empty-cwd", ""); err != nil {
		t.Fatalf("Get: %v", err)
	}

	calls := rc.snapshotNewSessionCalls()
	if len(calls) != 1 {
		t.Fatalf("NewSession called %d times; want 1", len(calls))
	}
	if calls[0] == "" {
		t.Errorf("createEntry forwarded empty cwd to client.NewSession (H-B regression — SC3 will break against real kiro-cli). See .planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md")
	}
	// And verify it resolved to the test process's working directory
	// (the os.Getwd() fallback path).
	wantCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if calls[0] != wantCwd {
		t.Errorf("createEntry cwd = %q; want %q (os.Getwd fallback)", calls[0], wantCwd)
	}
}

// TestRegistry_CreateEntry_PassesNonEmptyCwdVerbatim guards against
// over-correction: when the caller DOES supply a non-empty cwd, the
// registry must forward it verbatim (no normalisation, no fallback
// substitution). This is the symmetric guard to the empty-cwd test.
func TestRegistry_CreateEntry_PassesNonEmptyCwdVerbatim(t *testing.T) {
	rc := &recordingClient{}
	rf := &recordingFactory{clients: []*recordingClient{rc}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: rf,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wantCwd := "/some/explicit/cwd"
	if _, err := r.Get(ctx, "sid-explicit", wantCwd); err != nil {
		t.Fatalf("Get: %v", err)
	}

	calls := rc.snapshotNewSessionCalls()
	if len(calls) != 1 {
		t.Fatalf("NewSession called %d times; want 1", len(calls))
	}
	if calls[0] != wantCwd {
		t.Errorf("createEntry cwd = %q; want %q (must forward non-empty cwd verbatim)", calls[0], wantCwd)
	}
}

// TestEntry_NewSession_ReturnsCachedSessionID is the H-A REVERSE
// regression guard: Entry.NewSession MUST stay as a pure accessor that
// returns the cached SessionID without issuing a fresh kiro-cli
// session/new. A future refactor that "fixes" this to call
// Client.NewSession per request would break two-turn continuity (turn 2
// would not be able to reference turn 1's content because the model is
// bound to a different session id).
//
// The confirmatory experiment in 05-04-WIRE-DIFF.md proves that a single
// cached SessionID supports multiple prompts on the same kiro-cli child;
// H-A is the wrong fix for SC3.
func TestEntry_NewSession_ReturnsCachedSessionID(t *testing.T) {
	rc := &recordingClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "kiro-sess-cached", nil
		},
	}
	rf := &recordingFactory{clients: []*recordingClient{rc}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: rf,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-cached", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Reset NewSession call count so we can detect any subsequent invocations.
	rc.mu.Lock()
	rc.newSessionCalls = nil
	rc.mu.Unlock()

	// Call Entry.NewSession twice. Both MUST return the cached sid AND
	// MUST NOT issue any additional Client.NewSession RPCs.
	sid1, err := e.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("Entry.NewSession #1: %v", err)
	}
	sid2, err := e.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("Entry.NewSession #2: %v", err)
	}
	if sid1 != "kiro-sess-cached" || sid2 != "kiro-sess-cached" {
		t.Errorf("Entry.NewSession returned sid1=%q sid2=%q; want both = %q", sid1, sid2, "kiro-sess-cached")
	}
	if got := rc.snapshotNewSessionCalls(); len(got) != 0 {
		t.Errorf("Entry.NewSession issued %d additional Client.NewSession calls; want 0 (H-A REVERSE regression — would break two-turn continuity)", len(got))
	}
}

// TestEntry_Prompt_PassesCachedSessionID confirms Entry.Prompt forwards
// the cached SessionID to Client.Prompt. The H-A reverse guard: do NOT
// "fix" Entry.Prompt to recreate a session per prompt.
func TestEntry_Prompt_PassesCachedSessionID(t *testing.T) {
	rc := &recordingClient{
		newSessionFn: func(_ context.Context, _ string) (string, error) {
			return "kiro-sess-prompt", nil
		},
	}
	rf := &recordingFactory{clients: []*recordingClient{rc}}
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: rf,
	})
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	e, err := r.Get(ctx, "sid-p", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	blocks := []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}}}
	if _, err := e.Prompt(ctx, e.SessionID, blocks); err != nil {
		t.Fatalf("Entry.Prompt: %v", err)
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.promptCalls) != 1 {
		t.Fatalf("Client.Prompt called %d times; want 1", len(rc.promptCalls))
	}
	if rc.promptCalls[0] != "kiro-sess-prompt" {
		t.Errorf("Client.Prompt sid = %q; want %q (must forward cached SessionID verbatim)", rc.promptCalls[0], "kiro-sess-prompt")
	}
}
