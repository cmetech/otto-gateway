// Package engine — Collect text aggregation tests + chunk-kind drop
// tests + stop-reason propagation tests (D-01).
package engine

import (
	"context"
	"testing"

	"loop24-gateway/internal/canonical"
)

// TestCollect_AggregatesText asserts that Collect concatenates multiple
// ChunkKindText chunks into a single response Content.Text field.
func TestCollect_AggregatesText(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-aggregate",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("greet", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected exactly one content part; got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Text != "hello world" {
		t.Errorf("aggregated text: got %q, want 'hello world'", resp.Message.Content[0].Text)
	}
}

// TestCollect_DropsThoughtChunks asserts that ChunkKindThought chunks
// do NOT contribute to the assistant Content.Text (Phase 2 drop).
func TestCollect_DropsThoughtChunks(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-thought-drop",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "internal-reasoning"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "answer"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.Message.Content[0].Text != "answer" {
		t.Errorf("thought-chunk leaked into response; got %q, want 'answer'", resp.Message.Content[0].Text)
	}
}

// TestCollect_PropagatesStopReason asserts that final.StopReason from
// the stream flows into the assembled ChatResponse.StopReason.
func TestCollect_PropagatesStopReason(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-stop",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "truncated"}},
		},
		finalResult: &canonical.FinalResult{
			SessionID:  "sid-stop",
			ChunkCount: 1,
			StopReason: canonical.StopMaxTokens,
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.StopReason != canonical.StopMaxTokens {
		t.Errorf("StopReason: got %v, want StopMaxTokens", resp.StopReason)
	}
}
