package session

import (
	"context"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// NewEntryForTest is a test-only helper that returns a fully-constructed
// *Entry with Client wired to the caller-supplied engine.ACPClient,
// SessionID set to sid, and LastUsed initialised to time.Now(). The
// returned Entry has an unlocked Mu.
//
// This is the CANONICAL Entry-construction seam for cross-package
// adapter tests (plan 05-03 Task 3 calls session.NewEntryForTest from
// adapter tests in internal/adapter/{ollama,openai,anthropic}). Use
// this instead of standing up a real session.Registry + fakeClientFactory.
//
// Lives in a non-_test.go file so it is exported across package
// boundaries. The cost is one tiny exported helper in production code —
// cheaper than the alternative (a separate test-fixtures package).
//
// The Client parameter is engine.ACPClient (not PoolClient) because
// adapter tests already have engine.ACPClient fakes. The helper wraps
// the fake in a session.PoolClient adapter so Entry.Client has the
// right type; adapter tests interact with the entry through
// engine.ACPClient methods only, so the wiring is transparent.
func NewEntryForTest(client engine.ACPClient, sid string) *Entry {
	return &Entry{
		Client:    &acpClientForTestAdapter{c: client},
		SessionID: sid,
		LastUsed:  time.Now(),
	}
}

// acpClientForTestAdapter wraps an engine.ACPClient to satisfy the
// session-internal PoolClient interface for test scaffolding. The
// Initialize / Close / AvailableModels / Done methods are no-ops
// because NewEntryForTest skips Registry.createEntry's lifecycle —
// the caller is responsible for ensuring the underlying
// engine.ACPClient is in a usable state for the test.
//
// Prompt is the tricky path: engine.ACPClient.Prompt returns
// engine.Stream (interface) but session.PoolClient.Prompt returns
// *acp.Stream (concrete). NewEntryForTest is test-only, so the
// adapter returns a nil *acp.Stream — adapter tests that go through
// engine.Run interact with the engine.Stream via *Entry's Prompt
// method (defined in entry_acp.go), which goes through Client.Prompt
// here. That means adapter tests that drive Prompt through this seam
// would observe a nil *acp.Stream, which is fine because the production
// Entry.Prompt body (Task 1) wraps a non-nil acp.Stream in acpStreamShim.
// In tests for adapter surface handlers, the typical pattern is to
// inject a fake ACPClient that does not actually use the registry
// path — the helper is for *constructing* an Entry that can be passed
// as engine.ACPClient through the adapter wiring, not for driving
// Entry.Prompt itself.
type acpClientForTestAdapter struct {
	c engine.ACPClient
}

func (a *acpClientForTestAdapter) Initialize(_ context.Context) error { return nil }

func (a *acpClientForTestAdapter) NewSession(ctx context.Context, cwd string) (string, error) {
	return a.c.NewSession(ctx, cwd) //nolint:wrapcheck // test-only adapter; forward as-is
}

func (a *acpClientForTestAdapter) SetModel(ctx context.Context, sessionID, modelID string) error {
	return a.c.SetModel(ctx, sessionID, modelID) //nolint:wrapcheck // test-only adapter
}

// Prompt is intentionally a no-op in the test adapter because
// engine.ACPClient.Prompt returns engine.Stream (interface) and
// session.PoolClient.Prompt returns *acp.Stream (concrete). Adapter
// tests should NOT drive Entry.Prompt through this seam; they drive
// engine.Run with the fake engine.ACPClient directly. Returning a nil
// *acp.Stream + nil error is the contract.
func (a *acpClientForTestAdapter) Prompt(_ context.Context, _ string, _ []canonical.Block) (*acp.Stream, error) {
	return nil, nil //nolint:nilnil // intentional test-only adapter
}

func (a *acpClientForTestAdapter) Cancel(sessionID string) {
	a.c.Cancel(sessionID)
}

func (a *acpClientForTestAdapter) Close() error { return nil }

func (a *acpClientForTestAdapter) AvailableModels() []canonical.ModelInfo { return nil }

// closedDoneCh is a package-level already-closed channel returned by
// Done() so any test that selects on the channel observes "already
// exited" semantics. Tests that need a never-firing channel should
// inject the Entry differently — this is the cheap default.
var closedDoneCh = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func (a *acpClientForTestAdapter) Done() <-chan struct{} { return closedDoneCh }
