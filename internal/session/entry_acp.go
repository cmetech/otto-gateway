package session

import (
	"context"
	"fmt"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// Entry method set implementing engine.ACPClient. Surface handlers in
// plan 05-03 pass *Entry as the ACPClient to engine.Run, so the Phase 4
// D-06 watchdog applies to stateful sessions identically to pool ones.

// NewSession returns the cached ACP session id created during
// Registry.createEntry. Unlike *acp.Client.NewSession, this is a pure
// accessor — the registry pre-creates the session once and engine.Run
// reuses it across requests for the same sid (D-04 dedicated session
// per X-Session-Id). The ctx and cwd arguments are intentionally
// ignored: the engine.ACPClient interface predates the per-entry
// session model and the cwd handshake already happened in createEntry.
//
// Plan 05-04 H-A reverse-regression note (see
// .planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md
// "Rejected Hypotheses" → H-A): do NOT "fix" this method to call
// e.Client.NewSession per request. The wire-diff investigation
// confirmed (via two-turn continuity Experiment 3) that the cached
// SessionID supports multiple session/prompt calls without -32603. The
// actual SC3 bug was H-B (empty cwd at createEntry's session/new) —
// patched in registry.createEntry, not here. Recreating the session
// per request would break conversation-level continuity (turn 2 would
// not see turn 1's context because the model is bound per session id).
func (e *Entry) NewSession(_ context.Context, _ string) (string, error) {
	return e.SessionID, nil
}

// SetModel implements the D-09 diff-skip: if modelID matches the cached
// LastModel, return nil without an RPC. Otherwise forward to the
// underlying Client and update the cache on success. Cache is updated
// AFTER the RPC succeeds so a failed SetModel does not poison the
// cache (per resolved Open Question 1: SetModel failure surfaces 4xx,
// entry stays alive).
func (e *Entry) SetModel(ctx context.Context, sessionID, modelID string) error {
	if modelID == e.LastModel {
		return nil
	}
	if err := e.Client.SetModel(ctx, sessionID, modelID); err != nil {
		return fmt.Errorf("session: set-model: %w", err)
	}
	e.LastModel = modelID
	return nil
}

// Prompt wraps Client.Prompt's *acp.Stream return value in an
// acpStreamShim so the engine.Stream interface is satisfied. Caller
// holds e.Mu for the lifetime of the stream (D-07).
func (e *Entry) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (engine.Stream, error) {
	raw, err := e.Client.Prompt(ctx, sessionID, blocks)
	if err != nil {
		return nil, fmt.Errorf("session: prompt: %w", err)
	}
	return &acpStreamShim{s: raw}, nil
}

// Cancel is best-effort and forwards to Client.Cancel. The caller (or
// engine.Run's Phase 4 D-06 watchdog) owns the e.Mu lifecycle.
func (e *Entry) Cancel(sessionID string) {
	e.Client.Cancel(sessionID)
}

// MarkUsed updates LastUsed to time.Now(). Per D-11, surface handlers
// call this in a defer AFTER stream.Result() returns — NEVER at request
// start. Combined with D-12 (reaper TryLock skip), this means a session
// streaming continuously will never be reaped, even past TTL.
func (e *Entry) MarkUsed() {
	e.LastUsed = time.Now()
}

// acpStreamShim adapts *acp.Stream (Chunks is a FIELD; Result returns
// *acp.FinalResult) to engine.Stream (Chunks is a METHOD; Result
// returns *canonical.FinalResult). Copied verbatim from
// internal/engine/acp_adapter.go:63-90.
//
// Entry does NOT need pool's poolStreamWrapper — there is no slot to
// release because the entry IS the slot for the duration of the
// session. Lifecycle is managed by the surface handler's e.Mu.Lock +
// e.Mu.Unlock around engine.Run.
type acpStreamShim struct {
	s *acp.Stream
}

// Chunks returns the underlying *acp.Stream.Chunks field as a method.
// Pointer-equality of the channel with the underlying field is preserved
// (no copy / no buffering).
func (a *acpStreamShim) Chunks() <-chan canonical.Chunk { return a.s.Chunks }

// Result delegates to *acp.Stream.Result and translates the returned
// *acp.FinalResult into a *canonical.FinalResult. When the underlying
// FinalResult is nil (e.g., terminal error before any result was set),
// returns nil with the underlying err to preserve the call's signature.
func (a *acpStreamShim) Result() (*canonical.FinalResult, error) {
	fr, err := a.s.Result()
	if fr == nil {
		return nil, err //nolint:wrapcheck // pure delegation
	}
	return &canonical.FinalResult{
		SessionID:  fr.SessionID,
		ChunkCount: fr.ChunkCount,
		StopReason: fr.StopReason,
	}, err //nolint:wrapcheck // pure delegation
}

// Compile-time interface satisfaction checks. Build failure here means
// either *Entry no longer matches engine.ACPClient (method signature
// drift) or acpStreamShim no longer matches engine.Stream — surface
// the missing method to the executor. THIS IS THE LOAD-BEARING GATE
// for plan 05-03's surface-handler wiring.
var (
	_ engine.ACPClient = (*Entry)(nil)
	_ engine.Stream    = (*acpStreamShim)(nil)
)
