// Package acp_test — ACP cancel-frame wire integration test (STRM-04).
//
// TestIntegration_CancelFrame verifies that acp.Client.Cancel(sid) produces
// an observable session/cancel JSON-RPC notification with the correct
// sessionId on the fake-ACP wire. The test uses the fakeACPServer extended
// in fakeacp_test.go with cancelSeen + lastCancelSID (Plan 04-03 D-10).
//
// The goleak gate in testmain_test.go (package acp whitebox) covers all
// test files in this directory, including this blackbox file.
package acp_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/testutil"
)

// TestIntegration_CancelFrame proves STRM-04 at the ACP-wire level:
// acp.Client.Cancel(sid) emits a session/cancel notification with the
// correct sessionId param within a 2-second deadline.
//
// Steps:
//  1. Spin up a fakeACPServer and connect a real acp.Client via NewWithConn.
//  2. Initialize the client (ACP handshake).
//  3. Call client.Cancel("test-cancel-sid") — a fire-and-forget notification.
//  4. Wait on f.cancelSeen (select-with-deadline) — asserts the notification
//     was received by the fake.
//  5. Assert f.lastCancelSID == "test-cancel-sid".
func TestIntegration_CancelFrame(t *testing.T) {
	fake := newFakeACPServer(t)
	defer fake.close()

	cfg := acp.Config{
		Logger:       testutil.Logger(t),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping during test
	}

	client := acp.NewWithConn(fake.clientRWC, cfg)
	defer func() {
		if err := client.Close(); err != nil {
			t.Logf("client.Close (pipe-close expected): %v", err)
		}
		goleak.VerifyNone(t)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initialize — required before any session operations. The fake responds
	// with the spec-compliant initialize result.
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Cancel is a notification and can be sent at any point after the
	// connection is established — no active session required. Fire it.
	client.Cancel("test-cancel-sid")

	// Wait for the fake to observe the session/cancel notification.
	select {
	case <-fake.cancelSeen:
		// pass — session/cancel frame observed on fake-ACP wire
	case <-time.After(2 * time.Second):
		t.Fatal("session/cancel not observed on fake ACP wire within 2s")
	}

	// Assert the correct sessionId was carried in the notification params.
	if fake.lastCancelSID != "test-cancel-sid" {
		t.Errorf("session/cancel sessionId: got %q, want %q", fake.lastCancelSID, "test-cancel-sid")
	}
}
