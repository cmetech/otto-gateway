package acp

import (
	"testing"

	"otto-gateway/internal/canonical"
)

func TestStreamResultIncludesToolDenials(t *testing.T) {
	s := newStream(nil, "session-denial")
	s.recordDenial()
	s.recordDenial()
	s.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)

	first, err := s.Result()
	if err != nil {
		t.Fatal(err)
	}
	if first.ToolDenials != 2 {
		t.Fatalf("ToolDenials = %d, want 2", first.ToolDenials)
	}
	first.ToolDenials = 99
	second, err := s.Result()
	if err != nil {
		t.Fatal(err)
	}
	if second.ToolDenials != 2 {
		t.Fatalf("snapshot alias: ToolDenials = %d, want 2", second.ToolDenials)
	}
}

func TestStreamResultDefaultsToolDenialsToZero(t *testing.T) {
	s := newStream(nil, "session-no-denial")
	s.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)

	result, err := s.Result()
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolDenials != 0 {
		t.Fatalf("ToolDenials = %d, want 0", result.ToolDenials)
	}
}
