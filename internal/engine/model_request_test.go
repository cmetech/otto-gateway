// Whitebox: package engine — the OnModelRequest attribution hook fires once per
// Run with the canonical request's Model (kiro usage-metrics parity build).
package engine

import (
	"context"
	"log/slog"
	"testing"

	"otto-gateway/internal/canonical"
)

func modelReqTestEngine(t *testing.T, onModel func(string)) (*Engine, *fakeACP) {
	t.Helper()
	ack := &fakeACP{}
	e := New(Config{
		Logger:         slog.Default(),
		ACP:            ack,
		DefaultCWD:     "/test/cwd",
		OnModelRequest: onModel,
	})
	return e, ack
}

func userReq(model string) *canonical.ChatRequest {
	return &canonical.ChatRequest{
		Model: model,
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "hi"}}},
		},
	}
}

// TestRun_FiresOnModelRequest: Run reports the requested model exactly once.
func TestRun_FiresOnModelRequest(t *testing.T) {
	var got []string
	e, _ := modelReqTestEngine(t, func(m string) { got = append(got, m) })

	run, err := e.Run(context.Background(), userReq("claude-sonnet-4-7"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	if len(got) != 1 || got[0] != "claude-sonnet-4-7" {
		t.Errorf("OnModelRequest calls = %v; want exactly [claude-sonnet-4-7]", got)
	}
}

// TestRun_OnModelRequest_EmptyModel: an empty/auto model still fires the hook
// (the recorder buckets it as "auto"); the engine passes the raw value through.
func TestRun_OnModelRequest_EmptyModel(t *testing.T) {
	var got []string
	e, _ := modelReqTestEngine(t, func(m string) { got = append(got, m) })

	run, err := e.Run(context.Background(), userReq(""))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}

	if len(got) != 1 || got[0] != "" {
		t.Errorf("OnModelRequest calls = %v; want exactly [\"\"]", got)
	}
}

// TestRun_NilOnModelRequest_NoPanic: a nil hook is a no-op.
func TestRun_NilOnModelRequest_NoPanic(t *testing.T) {
	e, _ := modelReqTestEngine(t, nil)
	run, err := e.Run(context.Background(), userReq("auto"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}
}
