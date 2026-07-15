package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestCreateEntry_ForwardsCapture: Config.Capture is wired onto the entry's
// acp.Config.OnRawFrame in createEntry.
func TestCreateEntry_ForwardsCapture(t *testing.T) {
	var capturedCfg acp.Config
	cf := &capturingFactory{cfgSink: &capturedCfg, client: newFake("kiro-1")}

	var gotMethod string
	r := session.New(session.Config{
		Logger:  testutil.Logger(t),
		Factory: cf,
		Capture: func(method string, _ json.RawMessage) { gotMethod = method },
	})
	t.Cleanup(func() { _ = r.Close() })

	if _, err := r.Get(context.Background(), "sid", "/tmp"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedCfg.OnRawFrame == nil {
		t.Fatal("createEntry did not wire OnRawFrame")
	}
	capturedCfg.OnRawFrame("session/update", json.RawMessage(`{}`))
	if gotMethod != "session/update" {
		t.Errorf("capture not forwarded: got %q", gotMethod)
	}
}
