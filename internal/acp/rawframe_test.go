package acp

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestReadLoop_FiresOnRawFrame: every inbound frame that unmarshals is handed to
// OnRawFrame with its method + raw params, before dispatch.
func TestReadLoop_FiresOnRawFrame(t *testing.T) {
	var mu sync.Mutex
	type got struct {
		method string
		params string
	}
	var frames []got

	cfg := Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnRawFrame: func(method string, params json.RawMessage) {
			mu.Lock()
			frames = append(frames, got{method, string(params)})
			mu.Unlock()
		},
	}
	mock := newMockRWC()
	c := NewWithConn(mock, cfg)
	t.Cleanup(func() { _ = c.Close() })

	// Feed one notification frame through the reader side of the mock.
	mock.pushInbound(t, `{"jsonrpc":"2.0","method":"_kiro.dev/metadata","params":{"contextUsagePercentage":5}}`)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(frames) >= 1
	})

	mu.Lock()
	defer mu.Unlock()
	if frames[0].method != "_kiro.dev/metadata" {
		t.Errorf("method = %q", frames[0].method)
	}
	if frames[0].params != `{"contextUsagePercentage":5}` {
		t.Errorf("params = %q", frames[0].params)
	}
}

// pushInbound writes a raw JSON line to the mock's read side (simulating kiro → client).
func (m *mockRWC) pushInbound(t *testing.T, line string) {
	t.Helper()
	if _, err := m.serverWrite.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("pushInbound: %v", err)
	}
}

// waitFor polls a condition with 10ms ticks, up to 2s timeout.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("waitFor: condition not met within 2s")
}
