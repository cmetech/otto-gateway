package capture

import (
	"encoding/json"
	"sync/atomic"
)

// Controller wraps a Ring with a runtime on/off gate (enabled) and an opt-in
// flag (allowToggle) that permits flipping that gate at runtime — e.g. from the
// admin UI. The OnRawFrame hook is wired to Record unconditionally whenever a
// Controller exists; the atomic gate makes the disabled path a single load.
type Controller struct {
	ring        *Ring
	enabled     atomic.Bool
	allowToggle bool
}

// NewController builds a Controller over a fresh Ring of the given size/cap.
// initialEnabled seeds the on/off state (from ACP_CAPTURE); allowToggle records
// whether runtime toggling is permitted (from ACP_CAPTURE_RUNTIME).
func NewController(size, capBytes int, initialEnabled, allowToggle bool) *Controller {
	c := &Controller{ring: NewRing(size, capBytes), allowToggle: allowToggle}
	c.enabled.Store(initialEnabled)
	return c
}

// Record forwards to the ring only while capture is enabled.
func (c *Controller) Record(method string, params json.RawMessage) {
	if c.enabled.Load() {
		c.ring.Record(method, params)
	}
}

// Enable clears the buffer (fresh capture session) then turns recording on.
func (c *Controller) Enable() {
	c.ring.Clear()
	c.enabled.Store(true)
}

// Disable turns recording off; the buffer is retained for inspection.
func (c *Controller) Disable() { c.enabled.Store(false) }

// Clear purges the buffered frames on demand.
func (c *Controller) Clear() { c.ring.Clear() }

// Enabled reports whether recording is currently on.
func (c *Controller) Enabled() bool { return c.enabled.Load() }

// AllowRuntimeToggle reports whether runtime toggling is permitted (the
// ACP_CAPTURE_RUNTIME opt-in).
func (c *Controller) AllowRuntimeToggle() bool { return c.allowToggle }

// Count returns the number of buffered frames.
func (c *Controller) Count() int { return c.ring.Len() }

// Size returns the ring's fixed frame capacity.
func (c *Controller) Size() int { return c.ring.Cap() }

// Snapshot returns a copy of the buffered frames, oldest first.
func (c *Controller) Snapshot() []Frame { return c.ring.Snapshot() }
