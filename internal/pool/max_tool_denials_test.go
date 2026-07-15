package pool

import (
	"testing"
)

// TestAcpSlotConfig_ForwardsMaxToolDenials: Config.MaxToolDenials is wired onto
// each slot's acp.Config.MaxToolDenials; unset (0) leaves it at 0 (handler
// falls back to 4 defensively).
func TestAcpSlotConfig_ForwardsMaxToolDenials(t *testing.T) {
	p := New(Config{MaxToolDenials: 7})
	cfg := p.acpSlotConfig()
	if cfg.MaxToolDenials != 7 {
		t.Errorf("acpSlotConfig MaxToolDenials: got %d, want 7", cfg.MaxToolDenials)
	}

	// Unset (0) leaves it at 0 — handler falls back defensively.
	p2 := New(Config{})
	cfg2 := p2.acpSlotConfig()
	if cfg2.MaxToolDenials != 0 {
		t.Errorf("acpSlotConfig MaxToolDenials when unset: got %d, want 0", cfg2.MaxToolDenials)
	}
}
