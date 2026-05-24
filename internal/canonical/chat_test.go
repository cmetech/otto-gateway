package canonical

import (
	"reflect"
	"testing"
)

// TestChatRequest_ZeroValue locks the zero-value semantics of the
// forward-design ChatRequest so adapters can rely on "unset" === nil
// for every optional field (D-08).
func TestChatRequest_ZeroValue(t *testing.T) {
	var r ChatRequest
	if r.Model != "" {
		t.Errorf("Model: got %q, want empty", r.Model)
	}
	if r.System != "" {
		t.Errorf("System: got %q, want empty", r.System)
	}
	if r.Messages != nil {
		t.Errorf("Messages: got %v, want nil", r.Messages)
	}
	if r.Tools != nil {
		t.Errorf("Tools: got %v, want nil", r.Tools)
	}
	if r.ToolChoice != nil {
		t.Errorf("ToolChoice: got %v, want nil", r.ToolChoice)
	}
	if r.MaxTokens != 0 {
		t.Errorf("MaxTokens: got %d, want 0", r.MaxTokens)
	}
	if r.Temperature != nil {
		t.Errorf("Temperature: got %v, want nil", r.Temperature)
	}
	if r.TopP != nil {
		t.Errorf("TopP: got %v, want nil", r.TopP)
	}
	if r.StopSequences != nil {
		t.Errorf("StopSequences: got %v, want nil", r.StopSequences)
	}
	if r.Stream {
		t.Errorf("Stream: got true, want false")
	}
	if r.Think {
		t.Errorf("Think: got true, want false")
	}
	if r.Format != nil {
		t.Errorf("Format: got %v, want nil", r.Format)
	}
	if r.Metadata != nil {
		t.Errorf("Metadata: got %v, want nil", r.Metadata)
	}
	if r.WorkingDirOverride != "" {
		t.Errorf("WorkingDirOverride: got %q, want empty", r.WorkingDirOverride)
	}
	if r.ResourceLinks != nil {
		t.Errorf("ResourceLinks: got %v, want nil", r.ResourceLinks)
	}
}

// TestMessageRole_DiscriminatorCoverage locks the iota values that
// adapters depend on for wire mapping. Any reordering breaks every
// downstream surface — surface as a test failure here.
func TestMessageRole_DiscriminatorCoverage(t *testing.T) {
	cases := []struct {
		name string
		got  MessageRole
		want MessageRole
	}{
		{"RoleUser", RoleUser, 0},
		{"RoleSystem", RoleSystem, 1},
		{"RoleAssistant", RoleAssistant, 2},
		{"RoleTool", RoleTool, 3},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestContentKind_DiscriminatorCoverage locks the iota values for the
// ContentPart discriminator. Phase 2 only populates ContentKindText
// and ContentKindImage; the others are dormant seams whose positions
// are part of the contract Plan 04/06 will lean on.
func TestContentKind_DiscriminatorCoverage(t *testing.T) {
	cases := []struct {
		name string
		got  ContentKind
		want ContentKind
	}{
		{"ContentKindText", ContentKindText, 0},
		{"ContentKindImage", ContentKindImage, 1},
		{"ContentKindToolUse", ContentKindToolUse, 2},
		{"ContentKindToolResult", ContentKindToolResult, 3},
		{"ContentKindThinking", ContentKindThinking, 4},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestChatResponse_AssemblyShape constructs a populated ChatResponse
// with a single text ContentPart and StopEndTurn and asserts every
// populated field round-trips via reflect.DeepEqual (no JSON; D-11).
func TestChatResponse_AssemblyShape(t *testing.T) {
	got := ChatResponse{
		ID:    "msg_abc",
		Model: "claude-sonnet-4-7",
		Message: Message{
			Role: RoleAssistant,
			Content: []ContentPart{
				{Kind: ContentKindText, Text: "hello world"},
			},
		},
		StopReason: StopEndTurn,
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}
	want := ChatResponse{
		ID:    "msg_abc",
		Model: "claude-sonnet-4-7",
		Message: Message{
			Role: RoleAssistant,
			Content: []ContentPart{
				{Kind: ContentKindText, Text: "hello world"},
			},
		},
		StopReason: StopEndTurn,
		Usage:      Usage{InputTokens: 10, OutputTokens: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChatResponse round-trip: got %+v, want %+v", got, want)
	}
}

// TestNoJSONTags is a reflective sweep across every exported struct
// type declared in chat.go (D-11 defense — any future PR that adds
// json:"..." tags to canonical fails here, not silently on the wire).
func TestNoJSONTags(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(ChatRequest{}),
		reflect.TypeOf(ChatResponse{}),
		reflect.TypeOf(Message{}),
		reflect.TypeOf(ContentPart{}),
		reflect.TypeOf(Usage{}),
		reflect.TypeOf(ToolCall{}),
		reflect.TypeOf(ToolSpec{}),
		reflect.TypeOf(ToolChoice{}),
		reflect.TypeOf(Format{}),
		reflect.TypeOf(ImagePart{}),
		reflect.TypeOf(ToolUsePart{}),
		reflect.TypeOf(ToolResultPart{}),
		reflect.TypeOf(FinalResult{}),
	}
	for _, tp := range types {
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			if tag := f.Tag.Get("json"); tag != "" {
				t.Errorf("%s.%s has forbidden json tag %q (D-11)", tp.Name(), f.Name, tag)
			}
		}
	}
}

// TestFinalResult_ZeroValue locks the canonical mirror shape so Plan
// 04's wrapACPStream shim has a stable target. StopReason zero must
// be StopUnknown (Phase 1.1 D-02 forward-compat default).
func TestFinalResult_ZeroValue(t *testing.T) {
	var r FinalResult
	if r.SessionID != "" {
		t.Errorf("SessionID: got %q, want empty", r.SessionID)
	}
	if r.ChunkCount != 0 {
		t.Errorf("ChunkCount: got %d, want 0", r.ChunkCount)
	}
	if r.StopReason != StopUnknown {
		t.Errorf("StopReason: got %v, want StopUnknown", r.StopReason)
	}
}

// TestChatRequest_ResourceLinks asserts the H-2 field exists with the
// expected zero value, accepts multiple ResourceLinkBlock entries, and
// reuses the ResourceLinkBlock type from chunk.go (not a redeclared
// sibling type). Codex review H-2 coverage.
func TestChatRequest_ResourceLinks(t *testing.T) {
	t.Run("zero value is nil", func(t *testing.T) {
		var r ChatRequest
		if r.ResourceLinks != nil {
			t.Errorf("ResourceLinks zero value: got %v, want nil", r.ResourceLinks)
		}
	})

	t.Run("preserves multiple entries", func(t *testing.T) {
		got := ChatRequest{
			ResourceLinks: []ResourceLinkBlock{
				{URI: "file:///tmp/a"},
				{URI: "file:///tmp/b"},
			},
		}
		want := ChatRequest{
			ResourceLinks: []ResourceLinkBlock{
				{URI: "file:///tmp/a"},
				{URI: "file:///tmp/b"},
			},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ResourceLinks round-trip: got %+v, want %+v", got, want)
		}
	})

	t.Run("element type is canonical.ResourceLinkBlock", func(t *testing.T) {
		fieldType := reflect.TypeOf(ChatRequest{}.ResourceLinks)
		if fieldType.Kind() != reflect.Slice {
			t.Fatalf("ResourceLinks: got kind %v, want slice", fieldType.Kind())
		}
		elem := fieldType.Elem()
		if elem.Name() != "ResourceLinkBlock" {
			t.Errorf("ResourceLinks element type name: got %q, want %q", elem.Name(), "ResourceLinkBlock")
		}
	})
}
