package session_test

import (
	"context"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/session"
	"otto-gateway/internal/testutil"
)

// TestCreateEntry_ForwardsMaxToolDenials: createEntry wires the
// session.Config.MaxToolDenials onto the acp.Config.MaxToolDenials it passes
// to the factory. Uses a capturingFactory to observe the wired value.
func TestCreateEntry_ForwardsMaxToolDenials(t *testing.T) {
	var capturedCfg acp.Config
	cf := &capturingFactory{
		cfgSink: &capturedCfg,
		client:  newFake("kiro-1"),
	}
	r := session.New(session.Config{
		Logger:         testutil.Logger(t),
		Factory:        cf,
		MaxToolDenials: 7,
	})
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := r.Get(ctx, "sid", "/tmp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if capturedCfg.MaxToolDenials != 7 {
		t.Errorf("acp.Config.MaxToolDenials: got %d, want 7", capturedCfg.MaxToolDenials)
	}
}
