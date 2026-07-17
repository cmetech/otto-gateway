package capture

import (
	"encoding/json"
	"testing"
)

func TestController_RecordGatedByEnabled(t *testing.T) {
	c := NewController(8, 1024, false /*initialEnabled*/, true /*allowToggle*/)

	// Disabled: Record is a no-op.
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	if c.Count() != 0 {
		t.Fatalf("disabled Record buffered a frame: count=%d", c.Count())
	}
	if c.Enabled() {
		t.Fatal("initialEnabled=false but Enabled()=true")
	}

	// Enable, then record.
	c.Enable()
	if !c.Enabled() {
		t.Fatal("after Enable, Enabled()=false")
	}
	c.Record("session/update", json.RawMessage(`{"b":2}`))
	if c.Count() != 1 {
		t.Fatalf("enabled Record: count=%d, want 1", c.Count())
	}

	// Disable keeps the buffer readable.
	c.Disable()
	if c.Enabled() {
		t.Fatal("after Disable, Enabled()=true")
	}
	if c.Count() != 1 {
		t.Fatalf("Disable dropped the buffer: count=%d, want 1", c.Count())
	}
	c.Record("session/update", json.RawMessage(`{"c":3}`))
	if c.Count() != 1 {
		t.Fatalf("disabled Record after Disable buffered: count=%d, want 1", c.Count())
	}
}

func TestController_EnableAutoClears(t *testing.T) {
	c := NewController(8, 1024, true, true)
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	c.Record("session/update", json.RawMessage(`{"b":2}`))
	if c.Count() != 2 {
		t.Fatalf("pre-enable count=%d, want 2", c.Count())
	}
	// Enable starts a fresh session.
	c.Enable()
	if c.Count() != 0 {
		t.Fatalf("Enable did not auto-clear: count=%d, want 0", c.Count())
	}
}

func TestController_ClearAndMeta(t *testing.T) {
	c := NewController(16, 1024, true, false)
	if c.AllowRuntimeToggle() {
		t.Fatal("allowToggle=false but AllowRuntimeToggle()=true")
	}
	if c.Size() != 16 {
		t.Fatalf("Size: got %d, want 16", c.Size())
	}
	c.Record("session/update", json.RawMessage(`{"a":1}`))
	c.Clear()
	if c.Count() != 0 {
		t.Fatalf("Clear: count=%d, want 0", c.Count())
	}
}
