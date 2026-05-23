// Whitebox unit tests for translateUpdate (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"testing"

	"loop24-gateway/internal/canonical"
)

func TestTranslateText(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "text", Content: "hello world"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindText {
		t.Fatalf("kind: got %v, want ChunkKindText", ch.Kind)
	}
	if ch.Text == nil || ch.Text.Content != "hello world" {
		t.Errorf("Text.Content: got %v, want hello world", ch.Text)
	}
}

func TestTranslateThought(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "thought", Content: "reasoning step"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindThought {
		t.Fatalf("kind: got %v, want ChunkKindThought", ch.Kind)
	}
	if ch.Thought == nil || ch.Thought.Content != "reasoning step" {
		t.Errorf("Thought.Content: got %v, want reasoning step", ch.Thought)
	}
}

func TestTranslateToolCall(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{
		Type:     "tool_call",
		ToolName: "bash",
		Args:     map[string]any{"cmd": "ls"},
	}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindToolCall {
		t.Fatalf("kind: got %v, want ChunkKindToolCall", ch.Kind)
	}
	if ch.ToolCall == nil {
		t.Fatal("ToolCall is nil")
	}
	if ch.ToolCall.Name != "bash" {
		t.Errorf("ToolCall.Name: got %q, want bash", ch.ToolCall.Name)
	}
	if ch.ToolCall.Args["cmd"] != "ls" {
		t.Errorf("ToolCall.Args[cmd]: got %v, want ls", ch.ToolCall.Args["cmd"])
	}
}

func TestTranslatePlan(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "plan", Content: "plan content"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindPlan {
		t.Fatalf("kind: got %v, want ChunkKindPlan", ch.Kind)
	}
	if ch.Plan == nil || ch.Plan.Content != "plan content" {
		t.Errorf("Plan.Content: got %v, want plan content", ch.Plan)
	}
}

func TestTranslateUnknown(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "unknown_type", Content: "data"}
	ch := translateUpdate(u)
	// Unknown types fall back to text to avoid data loss.
	if ch.Kind != canonical.ChunkKindText {
		t.Errorf("kind: got %v, want ChunkKindText (fallback for unknown)", ch.Kind)
	}
	if ch.Text == nil || ch.Text.Content != "data" {
		t.Errorf("Text.Content fallback: got %v, want data", ch.Text)
	}
}
