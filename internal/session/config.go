package session

import (
	"context"
	"log/slog"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
)

// PoolClient is the session-internal interface that mirrors the subset of
// *acp.Client methods used by the registry. Production *acp.Client satisfies
// it structurally (see the compile-time assertion at the bottom of this
// file); tests inject fake implementations via fakeClientFactory.
//
// The interface is duplicated from internal/pool/config.go (rather than
// re-exported) so the two packages remain disjoint at the type level —
// session entries are dedicated subprocesses, not pool slots, and the two
// lifecycles must not be conflated by sharing a type alias.
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
	// the client's subprocess has exited (Close() or readLoop EOF).
	Done() <-chan struct{}
}

// ClientFactory constructs a PoolClient per session entry. The default
// implementation (acpClientFactory) wraps acp.New and returns the real
// *acp.Client. Tests inject fakeClientFactory to drive registry behaviour
// without spawning real kiro-cli subprocesses (Codex M-2 pattern).
type ClientFactory interface {
	Spawn(ctx context.Context, cfg acp.Config) (PoolClient, error)
}

// acpClientFactory is the default ClientFactory used when Config.Factory
// is nil. It delegates straight to acp.New, returning the real *acp.Client
// (which structurally satisfies PoolClient).
type acpClientFactory struct{}

// Spawn implements ClientFactory by delegating to acp.New.
func (acpClientFactory) Spawn(_ context.Context, cfg acp.Config) (PoolClient, error) {
	c, err := acp.New(cfg)
	if err != nil {
		return nil, err //nolint:wrapcheck // pure delegation; caller (Registry.createEntry) wraps with sid context
	}
	return c, nil
}

// Config bundles all registry dependencies.
//
// TTL/TickInterval/MaxSessions are the per-test injection seams: production
// callers leave them zero and applyDefaults fills in Node-parity defaults
// (TTL=30min, TickInterval=60s, MaxSessions=32). Reaper tests inject
// TTL=200ms + TickInterval=50ms for deterministic real-time SESS-02
// verification (D-13).
type Config struct {
	// Logger is recommended but not required. A nil Logger is tolerated
	// for tests that don't inspect log output; production callers must
	// pass a real logger.
	Logger *slog.Logger
	// Factory is the ClientFactory used to construct each entry's
	// PoolClient. Defaults to acpClientFactory{} which wraps acp.New.
	Factory ClientFactory
	// TTL is the idle-session reap threshold (D-10). Defaults to
	// 30 * time.Minute (Node parity — SESSION_TTL_MS=1_800_000).
	TTL time.Duration
	// TickInterval is the reaper tick cadence (D-10). Defaults to
	// 60 * time.Second. Tests inject 50ms for D-13 real-time tests.
	TickInterval time.Duration
	// MaxSessions is the SESSION_MAX cap (D-06). Defaults to 32.
	// Lazy-create that would exceed the cap returns ErrSessionMaxExceeded
	// for surface adapters to render as 503.
	MaxSessions int
	// KiroCmd is the kiro-cli binary path or name. Forwarded to acp.Config.
	KiroCmd string
	// KiroArgs are the arguments passed to kiro-cli. Forwarded to acp.Config.
	KiroArgs []string
	// KiroCWD is the default working directory passed to NewSession when
	// the surface handler does not supply a per-request cwd.
	KiroCWD string
	// PingInterval is the heartbeat interval forwarded to acp.Config.
	PingInterval time.Duration
}

// applyDefaults fills in zero-value Config fields. TTL/TickInterval/
// MaxSessions defaults match D-06/D-10/D-13. Factory defaults to
// acpClientFactory{} when nil. A nil Logger is left alone — the registry
// itself tolerates nil-Logger paths via a small helper, but production
// callers should pass a real logger.
func (c *Config) applyDefaults() {
	if c.TTL <= 0 {
		c.TTL = 30 * time.Minute
	}
	if c.TickInterval <= 0 {
		c.TickInterval = 60 * time.Second
	}
	if c.MaxSessions <= 0 {
		c.MaxSessions = 32
	}
	if c.Factory == nil {
		c.Factory = acpClientFactory{}
	}
}

// Compile-time assertion that *acp.Client structurally satisfies
// PoolClient. Build failure here means an acp.Client method signature
// drifted away from what the registry depends on — fix the interface or
// fix the client.
var _ PoolClient = (*acp.Client)(nil)
