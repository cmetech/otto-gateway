// Package engine — Collect text aggregation tests + chunk-kind drop
// tests + stop-reason propagation tests (D-01).
package engine

import (
	"context"
	"testing"

	"otto-gateway/internal/canonical"
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

// TestCollect_AggregatesThoughtsAsThinkingPart (Phase 3.1 D-02) asserts
// that ChunkKindThought chunks now contribute to a SECOND content part
// of Kind == ContentKindThinking on the assembled Message.Content. Phase
// 2's "intentionally drop" behaviour is replaced — the dormant
// ContentKindThinking seam goes live so the Anthropic adapter can render
// thinking content blocks (ANTH-07 foundation).
//
// Text part comes first (zero index for Anthropic block sequencing per
// D-03/D-04); thinking part comes second when non-empty.
func TestCollect_AggregatesThoughtsAsThinkingPart(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-thought-aggregate",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
			{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "reasoning "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("Content parts: got %d, want 2 (text + thinking)", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %q, want 'Hello world'", resp.Message.Content[0].Text)
	}
	if resp.Message.Content[1].Kind != canonical.ContentKindThinking {
		t.Errorf("Content[1].Kind: got %v, want ContentKindThinking", resp.Message.Content[1].Kind)
	}
	if resp.Message.Content[1].Text != "reasoning " {
		t.Errorf("Content[1].Text: got %q, want 'reasoning '", resp.Message.Content[1].Text)
	}
}

// TestCollect_TextOnly_NoThinkingPart_Appended asserts that the
// thinking part is appended ONLY when at least one ChunkKindThought
// chunk arrives — a text-only stream keeps Content len == 1 so the
// Phase 2 Ollama shape (which expects len(Content) == 1 for plain
// text responses) is preserved as a regression guard.
func TestCollect_TextOnly_NoThinkingPart_Appended(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-text-only",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "answer"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content parts (no thoughts emitted): got %d, want 1", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Text != "answer" {
		t.Errorf("Content[0].Text: got %q, want 'answer'", resp.Message.Content[0].Text)
	}
}

// TestCollect_ThoughtOnly_StillEmitsEmptyTextPart — a stream that
// emits ONLY ChunkKindThought (no text at all) still produces a
// stable two-part shape: the leading text part is preserved as an
// empty-string ContentKindText (defensive — keeps Phase 2 Ollama's
// joinTextContent path returning ""; the Anthropic adapter renders
// only the thinking content block in that case).
func TestCollect_ThoughtOnly_StillEmitsEmptyTextPart(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-thought-only",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "pure reasoning"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("Content parts: got %d, want 2 (empty text + thinking)", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "" {
		t.Errorf("Content[0].Text: got %q, want empty (text builder produced no chars)", resp.Message.Content[0].Text)
	}
	if resp.Message.Content[1].Kind != canonical.ContentKindThinking {
		t.Errorf("Content[1].Kind: got %v, want ContentKindThinking", resp.Message.Content[1].Kind)
	}
	if resp.Message.Content[1].Text != "pure reasoning" {
		t.Errorf("Content[1].Text: got %q, want 'pure reasoning'", resp.Message.Content[1].Text)
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
