// Package pool implements a fixed-size warm pool of *acp.Client slots
// that satisfies engine.ACPClient and exposes the model catalog captured
// during warmup.
//
// Phase 2 default is POOL_SIZE=1 (D-07); Phase 5 will bump to 4 and add
// dead-slot detection + session registry.
//
// Codex M-2: ClientFactory + PoolClient interface seam in config.go lets
// tests inject fake clients without spawning real kiro-cli subprocesses.
package pool

import (
	"context"
	"log/slog"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
)

// PoolClient is the pool-internal interface that mirrors the subset of
// *acp.Client methods used by the pool. Production *acp.Client satisfies
// it structurally (see the compile-time assertion at the bottom of this
// file); tests inject fake implementations via fakeClientFactory.
//
// Codex M-2: previously the pool stored *acp.Client directly, making
// slot lifecycle (Prompt/Result/Cancel routing, session→slot tracking)
// impossible to exercise without a real kiro-cli subprocess. The
// interface seam fixes that without changing production behaviour.
//
// Prompt still returns *acp.Stream (concrete) — the pool wraps it in
// poolStreamWrapper before exposing the result to engine.ACPClient.
// The engine boundary is owned by Plan 04's NewACPClientAdapter; the
// pool boundary is owned by poolStreamWrapper in pool.go.
//
// Name choice (vs. revive's stutter rule): "PoolClient" is intentional.
// The naked name "Client" would collide visually with acp.Client in
// test bodies (`pool.Client` vs `acp.Client` reads ambiguously); the
// fake-injection harness — fakeClient implementing pool.PoolClient —
// is markedly clearer with the prefix kept.
//
//nolint:revive // PoolClient stutter is intentional — disambiguates from acp.Client in test bodies
type PoolClient interface {
	Initialize(ctx context.Context) error
	NewSession(ctx context.Context, cwd string) (string, error)
	SetModel(ctx context.Context, sessionID, modelID string) error
	Prompt(ctx context.Context, sessionID string, blocks []canonical.Block) (*acp.Stream, error)
	Cancel(sessionID string)
	Close() error
	AvailableModels() []canonical.ModelInfo
	// Done is the Phase 5 D-01 push-exit signal: the channel closes when
	// the client's subprocess has exited (Close() or readLoop EOF). The
	// per-slot exit-watcher in exit_watcher.go selects on this channel.
	Done() <-chan struct{}
}

// ClientFactory constructs a PoolClient per slot. The default
// implementation (acpClientFactory) wraps acp.New and returns the real
// *acp.Client. Tests inject fakeClientFactory to drive pool behaviour
// without real kiro-cli.
//
// Codex M-2: factory seam — letting Warmup / NewSession / Prompt /
// Cancel / Result paths be exercised in unit tests.
type ClientFactory interface {
	Spawn(ctx context.Context, cfg acp.Config) (PoolClient, error)
}

// acpClientFactory is the default ClientFactory used when Config.Factory
// is nil. It delegates straight to acp.New, returning the real
// *acp.Client (which structurally satisfies PoolClient).
//
// Note: acp.New does not currently take a context (the subprocess is
// bound to an internal client-lifetime context that Close cancels).
// The ctx argument here is for symmetry with future ctx-aware variants
// and to let Warmup propagate cancellation at the Initialize/NewSession
// level (those calls DO take ctx).
type acpClientFactory struct{}

// Spawn implements ClientFactory by delegating to acp.New.
func (acpClientFactory) Spawn(_ context.Context, cfg acp.Config) (PoolClient, error) {
	c, err := acp.New(cfg)
	if err != nil {
		return nil, err //nolint:wrapcheck // pure delegation; caller (Pool.initSlot) wraps with slot label context
	}
	return c, nil
}

// Config bundles all pool dependencies. Size defaults to 1 in Phase 2
// (D-07). KiroCmd / KiroArgs / KiroCWD / PingInterval are forwarded
// verbatim to acp.Config for each slot's subprocess.
type Config struct {
	// Logger is recommended but not required. A nil Logger is tolerated
	// (the pool itself does not log; *acp.Client tolerates nil too —
	// but production callers should pass a real logger).
	Logger *slog.Logger
	// Size is the number of warm slots. Defaults to 1 if zero or
	// negative (D-07 — Phase 2 default is POOL_SIZE=1).
	Size int
	// KiroCmd is the kiro-cli binary path or name. Forwarded to acp.Config.
	KiroCmd string
	// KiroArgs are the arguments passed to kiro-cli. Forwarded to acp.Config.
	KiroArgs []string
	// KiroCWD is the working directory for the kiro-cli subprocess AND
	// the cwd passed to slot.Client.NewSession during warmup (slot 0
	// only, for model-catalog capture per Codex H-6).
	KiroCWD string
	// PingInterval is the heartbeat interval forwarded to acp.Config.
	PingInterval time.Duration
	// Factory is the ClientFactory used to construct each slot's
	// PoolClient. Defaults to acpClientFactory{} which wraps acp.New.
	// Tests inject a fake factory to drive pool behaviour without
	// spawning real kiro-cli (Codex M-2).
	Factory ClientFactory
}

// applyDefaults fills in zero-value Config fields. Size floors to 1
// when zero or negative (D-07). Factory defaults to acpClientFactory{}
// when nil. A nil Logger is left alone — the pool itself does not log.
func (c *Config) applyDefaults() {
	if c.Size <= 0 {
		c.Size = 1
	}
	if c.Factory == nil {
		c.Factory = acpClientFactory{}
	}
}

// Compile-time assertion that *acp.Client structurally satisfies
// PoolClient. Build failure here means an acp.Client method signature
// drifted away from what the pool depends on — fix the interface or
// fix the client.
var _ PoolClient = (*acp.Client)(nil)
