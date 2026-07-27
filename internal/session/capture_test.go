package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestCreateEntry_ForwardsKiroEnv ensures each stateful Kiro subprocess gets
// the launch-specific environment prepared by the gateway.
func TestCreateEntry_ForwardsKiroEnv(t *testing.T) {
	var captured acp.Config
	want := []string{"KIRO_CHAT_LOG_FILE=/tmp/kiro-chat.log"}
	r := session.New(session.Config{
		Factory: &capturingFactory{cfgSink: &captured, client: newFake("kiro-env")},
		KiroCWD: t.TempDir(), KiroEnv: want,
	})
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Get(context.Background(), "session-env", ""); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, captured.Env); diff != "" {
		t.Fatalf("acp env mismatch (-want +got):\n%s", diff)
	}
}

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
