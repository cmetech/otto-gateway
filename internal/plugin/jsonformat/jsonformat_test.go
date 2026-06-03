package jsonformat

import (
	"context"
	"strings"
	"sync"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// Compile-time interface assertion mirroring the package-level var.
var _ engine.PreHook = (*JSONFormatSteeringHook)(nil)

// TestGenRulesBlock_VerbatimText pins the exact GEN_RULES text so that any
// accidental mutation of the constant is caught by CI. The substrings match
// key phrases from the Node shim verbatim (D-03).
func TestGenRulesBlock_VerbatimText(t *testing.T) {
	required := []string{
		"Generate the COMPLETE result for EVERY item requested",
		"do not summarize, truncate, abbreviate",
		"do NOT offer to export, save, or write to a file",
	}
	for _, sub := range required {
		if !strings.Contains(genRulesBlock, sub) {
			t.Errorf("genRulesBlock missing required substring: %q", sub)
		}
	}
	// Verify the em-dash is present (not a plain hyphen).
	if !strings.Contains(genRulesBlock, "—") {
		t.Error("genRulesBlock: em-dash missing (should be — not -)")
	}
}

// TestBefore_NilFormat passes through without modification when Format is nil.
func TestBefore_NilFormat(t *testing.T) {
	h := New(true)
	req := &canonical.ChatRequest{System: "original system prompt"}
	resp, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil resp, got %+v", resp)
	}
	if req.System != "original system prompt" {
		t.Errorf("System mutated despite nil Format: got %q", req.System)
	}
}

// TestBefore_FormatJSON_EmptySystem sets System to genRulesBlock when System is empty.
func TestBefore_FormatJSON_EmptySystem(t *testing.T) {
	h := New(true)
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json"},
	}
	_, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.System != genRulesBlock {
		t.Errorf("System: got %q, want genRulesBlock", req.System)
	}
}

// TestBefore_FormatJSON_NonEmptySystem appends with "\n\n" separator.
func TestBefore_FormatJSON_NonEmptySystem(t *testing.T) {
	h := New(true)
	original := "You are a helpful assistant."
	req := &canonical.ChatRequest{
		System: original,
		Format: &canonical.Format{Type: "json"},
	}
	_, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := original + "\n\n" + genRulesBlock
	if req.System != want {
		t.Errorf("System: got %q, want %q", req.System, want)
	}
}

// TestBefore_FormatJSONSchema_WithSchema appends both GEN_RULES and schema line.
func TestBefore_FormatJSONSchema_WithSchema(t *testing.T) {
	h := New(true)
	schema := map[string]any{
		"type":     "object",
		"required": []any{"a"},
	}
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json_schema", Schema: schema},
	}
	_, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(req.System, genRulesBlock) {
		t.Errorf("System missing genRulesBlock: %q", req.System)
	}
	if !strings.Contains(req.System, "The output must match this JSON schema:") {
		t.Errorf("System missing schema description line: %q", req.System)
	}
}

// TestBefore_FormatJSONSchema_NilSchema appends only GEN_RULES (no schema line).
func TestBefore_FormatJSONSchema_NilSchema(t *testing.T) {
	h := New(true)
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json_schema", Schema: nil},
	}
	_, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(req.System, genRulesBlock) {
		t.Errorf("System missing genRulesBlock: %q", req.System)
	}
	if strings.Contains(req.System, "match this JSON schema") {
		t.Errorf("System should NOT contain schema line when Schema is nil: %q", req.System)
	}
}

// TestBefore_Disabled passes through completely even when Format is non-nil.
func TestBefore_Disabled(t *testing.T) {
	h := New(false)
	req := &canonical.ChatRequest{
		System: "keep this",
		Format: &canonical.Format{Type: "json"},
	}
	resp, err := h.Before(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil resp")
	}
	if req.System != "keep this" {
		t.Errorf("System mutated despite Enabled=false: got %q", req.System)
	}
}

// TestBefore_NotIdempotent documents that calling Before twice appends the
// block twice — the hook is NOT idempotent (chain ordering guarantees
// single Pre-run per request; this test documents the known behavior).
func TestBefore_NotIdempotent(t *testing.T) {
	h := New(true)
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json"},
	}
	_, _ = h.Before(context.Background(), req)
	firstSystem := req.System
	_, _ = h.Before(context.Background(), req)
	secondSystem := req.System

	// Second call appended a second copy → System contains TWO GEN_RULES blocks.
	count := strings.Count(secondSystem, genRulesBlock)
	if count != 2 {
		t.Errorf("expected 2 genRulesBlock occurrences after two Before calls, got %d\nfirst: %q\nsecond: %q",
			count, firstSystem, secondSystem)
	}
}

// TestBefore_Concurrency spawns 100 goroutines calling Before on independent
// requests and asserts no data race (run with -race).
func TestBefore_Concurrency(t *testing.T) {
	h := New(true)
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req := &canonical.ChatRequest{
				Format: &canonical.Format{Type: "json"},
			}
			_, err := h.Before(context.Background(), req)
			if err != nil {
				t.Errorf("unexpected error in goroutine: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestBefore_NilRequest is defensive: Before on a nil *ChatRequest must
// return (nil, nil) without panicking.
func TestBefore_NilRequest(t *testing.T) {
	h := New(true)
	resp, err := h.Before(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil resp")
	}
}
