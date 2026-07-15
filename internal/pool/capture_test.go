package pool

import (
	"encoding/json"
	"testing"
)

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
