package pool

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestAcpSlotConfig_ForwardsKiroEnv ensures each pooled Kiro subprocess gets
// the launch-specific environment prepared by the gateway.
func TestAcpSlotConfig_ForwardsKiroEnv(t *testing.T) {
	want := []string{"KIRO_CHAT_LOG_FILE=/tmp/kiro-chat.log"}
	cfg := New(Config{KiroEnv: want}).acpSlotConfig()
	if diff := cmp.Diff(want, cfg.Env); diff != "" {
		t.Fatalf("acp env mismatch (-want +got):\n%s", diff)
	}
}

// TestAcpSlotConfig_ForwardsCapture: Config.Capture is wired onto each slot's
// acp.Config.OnRawFrame; unset leaves it nil.
func TestAcpSlotConfig_ForwardsCapture(t *testing.T) {
	var gotMethod string
	p := New(Config{Capture: func(method string, _ json.RawMessage) { gotMethod = method }})

	cfg := p.acpSlotConfig()
	if cfg.OnRawFrame == nil {
		t.Fatal("acpSlotConfig must wire OnRawFrame when Config.Capture is set")
	}
	cfg.OnRawFrame("session/update", json.RawMessage(`{}`))
	if gotMethod != "session/update" {
		t.Errorf("capture not forwarded: got %q", gotMethod)
	}

	if New(Config{}).acpSlotConfig().OnRawFrame != nil {
		t.Error("OnRawFrame must be nil when Config.Capture is unset")
	}
}
