package engine

import (
	"testing"

	"otto-gateway/internal/canonical"
)

func TestResolveNativeToolName(t *testing.T) {
	tools := []canonical.ToolSpec{{Name: "run_shell"}, {Name: "read_file"}}
	aliases := map[string]string{"execute": "run_shell", "fs_read": "read_file", "danglingfoo": "not_offered"}

	cases := []struct {
		name        string
		in          string
		tools       []canonical.ToolSpec
		wantName    string
		wantSurface bool
	}{
		{"no_tools_surfaces_as_is", "execute", nil, "execute", true},
		{"alias_to_offered", "execute", tools, "run_shell", true},
		{"alias_fs_read", "fs_read", tools, "read_file", true},
		{"direct_offered_name", "run_shell", tools, "run_shell", true},
		{"no_alias_no_match_dropped", "fs_write", tools, "", false},
		{"alias_target_not_offered_dropped", "danglingfoo", tools, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, surface := ResolveNativeToolName(tc.in, tc.tools, aliases)
			if surface != tc.wantSurface {
				t.Fatalf("surface: got %v, want %v", surface, tc.wantSurface)
			}
			if got != tc.wantName {
				t.Errorf("name: got %q, want %q", got, tc.wantName)
			}
		})
	}
}

// TestDedupToolCalls_CaptureShape reproduces the exact sequence a real kiro
// capture produced for a denied-then-retried shell command: a chunk (no args)
// + a full (args) for id A, then the same pair for a retry id B — all aliased
// to run_shell with identical {command}. The dedup must yield ONE call.
func TestDedupToolCalls_CaptureShape(t *testing.T) {
	args := map[string]any{"command": "python3 -c \"print(2+2)\""}
	in := []canonical.ToolCall{
		{ID: "tooluse_A", Name: "run_shell", Arguments: nil},  // tool_call_chunk
		{ID: "tooluse_A", Name: "run_shell", Arguments: args}, // tool_call (full)
		{ID: "tooluse_B", Name: "run_shell", Arguments: nil},  // retry chunk
		{ID: "tooluse_B", Name: "run_shell", Arguments: args}, // retry full
	}
	out := DedupToolCalls(in)
	if len(out) != 1 {
		t.Fatalf("dedup: got %d calls, want 1: %+v", len(out), out)
	}
	if out[0].ID != "tooluse_A" || out[0].Name != "run_shell" {
		t.Errorf("dedup[0]: got id=%q name=%q, want tooluse_A/run_shell", out[0].ID, out[0].Name)
	}
	if out[0].Arguments["command"] != args["command"] {
		t.Errorf("dedup[0].Arguments merged wrong: %+v", out[0].Arguments)
	}
}

func TestDedupToolCalls_DistinctCallsPreserved(t *testing.T) {
	in := []canonical.ToolCall{
		{ID: "a", Name: "run_shell", Arguments: map[string]any{"command": "ls"}},
		{ID: "b", Name: "read_file", Arguments: map[string]any{"path": "/x"}},
		{ID: "c", Name: "run_shell", Arguments: map[string]any{"command": "pwd"}},
	}
	out := DedupToolCalls(in)
	if len(out) != 3 {
		t.Fatalf("distinct calls collapsed: got %d, want 3: %+v", len(out), out)
	}
}

func TestDedupToolCalls_MergeChunkThenFull_SingleID(t *testing.T) {
	in := []canonical.ToolCall{
		{ID: "x", Name: "run_shell", Arguments: nil},
		{ID: "x", Name: "run_shell", Arguments: map[string]any{"command": "ls"}},
	}
	out := DedupToolCalls(in)
	if len(out) != 1 || out[0].Arguments["command"] != "ls" {
		t.Fatalf("chunk+full merge failed: %+v", out)
	}
}
