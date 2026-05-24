// Package engine — explicit *acp.Client → engine.ACPClient wrapper
// (Codex H-3 Option B).
//
// This is the ONLY file in package engine that imports internal/acp;
// engine.go itself stays acp-free so the engine package depends only on
// canonical types and its own interfaces. The wrapper is required
// because *acp.Client does NOT structurally satisfy engine.ACPClient:
// acp.Client.Prompt returns *acp.Stream (concrete) whereas
// engine.ACPClient.Prompt returns engine.Stream (interface). The
// acpStreamShim bridges this: it adapts *acp.Stream.Chunks (a FIELD) to
// engine.Stream.Chunks() (a METHOD) and translates *acp.FinalResult to
// *canonical.FinalResult.
package engine

import (
	"context"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
)

// NewACPClientAdapter returns an ACPClient that delegates to client.
// client must be non-nil for production use (the *acp.Client(nil) form is
// permitted ONLY by the compile-time test in acp_adapter_test.go).
func NewACPClientAdapter(client *acp.Client) ACPClient {
	return &acpClientAdapter{client: client}
}

// acpClientAdapter wraps *acp.Client and implements engine.ACPClient.
type acpClientAdapter struct {
	client *acp.Client
}

// NewSession delegates to *acp.Client.NewSession.
// Errors are returned unwrapped so callers see the acp package's
// classified errors (ErrClientClosed, ErrSessionClosed, …) directly.
func (a *acpClientAdapter) NewSession(ctx context.Context, cwd string) (string, error) {
	return a.client.NewSession(ctx, cwd) //nolint:wrapcheck // pure delegation
}

// SetModel delegates to *acp.Client.SetModel. Errors unwrapped (see
// NewSession rationale).
func (a *acpClientAdapter) SetModel(ctx context.Context, sessionID, modelID string) error {
	return a.client.SetModel(ctx, sessionID, modelID) //nolint:wrapcheck // pure delegation
}

// Prompt delegates to *acp.Client.Prompt and wraps the returned
// *acp.Stream in an acpStreamShim so the engine.Stream interface is
// satisfied. Errors unwrapped (see NewSession rationale).
func (a *acpClientAdapter) Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (Stream, error) {
	s, err := a.client.Prompt(ctx, sessionID, blocks)
	if err != nil {
		return nil, err //nolint:wrapcheck // pure delegation
	}
	return &acpStreamShim{s: s}, nil
}

// Cancel delegates to *acp.Client.Cancel.
func (a *acpClientAdapter) Cancel(sessionID string) {
	a.client.Cancel(sessionID)
}

// acpStreamShim adapts *acp.Stream (Chunks is a FIELD; Result returns
// *acp.FinalResult) to engine.Stream (Chunks is a METHOD; Result
// returns *canonical.FinalResult).
type acpStreamShim struct {
	s *acp.Stream
}

// Chunks returns the underlying *acp.Stream.Chunks field as a method.
// Pointer-equality of the channel with the underlying field is preserved
// (no copy / no buffering) so the engine and the underlying acp readLoop
// continue to share the same channel.
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

// Production-path compile-time interface satisfaction check. Build
// failure here means acpClientAdapter no longer implements ACPClient —
// surface the missing method to the executor.
var (
	_ ACPClient = (*acpClientAdapter)(nil)
	_ Stream    = (*acpStreamShim)(nil)
)
