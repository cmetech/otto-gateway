// Package engine — buildBlocks golden bracketed-section tests +
// image-block emission tests + runnable Example. D-02 + D-09 footnote +
// Codex M-1.
package engine

import (
	"encoding/base64"
	"fmt"
	"reflect"
	"testing"

	"loop24-gateway/internal/canonical"
)

// TestBuildBlocks_GoldenSystemUserAssistant verifies the bracketed-
// section text output for a standard system/user/assistant transcript.
// The text block must be element [0] of the returned slice (image
// blocks, when present, follow).
func TestBuildBlocks_GoldenSystemUserAssistant(t *testing.T) {
	req := &canonical.ChatRequest{
		System: "You are helpful.",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hello!"},
			}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hi there."},
			}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "How are you?"},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) == 0 {
		t.Fatal("buildBlocks returned empty slice; expected at least the text block")
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Fatalf("first block kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[0].Text == nil {
		t.Fatal("first block Text is nil")
	}
	want := "[System]\nYou are helpful.\n\n[User]\nHello!\n\n[Assistant]\nHi there.\n\n[User]\nHow are you?"
	if got[0].Text.Content != want {
		t.Errorf("bracketed text mismatch.\n got: %q\nwant: %q", got[0].Text.Content, want)
	}
}

// TestBuildBlocks_ThinkBlock verifies the [Reasoning] section emits when
// req.Think is true.
func TestBuildBlocks_ThinkBlock(t *testing.T) {
	req := &canonical.ChatRequest{
		Think: true,
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Help me debug."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if !contains(got[0].Text.Content, "[Reasoning]") {
		t.Errorf("expected [Reasoning] block in output; got %q", got[0].Text.Content)
	}
	if !contains(got[0].Text.Content, "[User]") {
		t.Errorf("expected [User] block in output; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_FormatBlock verifies the [Output format] section
// emits when req.Format is non-nil.
func TestBuildBlocks_FormatBlock(t *testing.T) {
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json"},
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Give me JSON."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if !contains(got[0].Text.Content, "[Output format]") {
		t.Errorf("expected [Output format] block; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_DropsSystemMessage verifies that RoleSystem messages
// do NOT appear in the transcript body (System field is the canonical
// source and already extracted into the [System] header).
func TestBuildBlocks_DropsSystemMessage(t *testing.T) {
	req := &canonical.ChatRequest{
		System: "Already extracted.",
		Messages: []canonical.Message{
			// This RoleSystem message body must NOT appear in transcript.
			{Role: canonical.RoleSystem, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "SHOULD-NOT-APPEAR"},
			}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hello."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if contains(got[0].Text.Content, "SHOULD-NOT-APPEAR") {
		t.Errorf("system message body leaked into transcript: %q", got[0].Text.Content)
	}
	if !contains(got[0].Text.Content, "[System]\nAlready extracted.") {
		t.Errorf("expected [System] header with the System field value; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_EmitsImageBlock_ForContentKindImage (Codex M-1 / D-09
// footnote) — proves ContentKindImage parts produce BlockKindImage
// blocks, not silently dropped.
func TestBuildBlocks_EmitsImageBlock_ForContentKindImage(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	dataB64 := base64.StdEncoding.EncodeToString(pngBytes)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "describe this"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{
					MIME:       "image/png",
					DataBase64: dataB64,
				}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks (text + image), got %d", len(got))
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Errorf("block[0] kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[1].Kind != canonical.BlockKindImage {
		t.Errorf("block[1] kind: got %v, want BlockKindImage", got[1].Kind)
	}
	if got[1].Image == nil {
		t.Fatal("block[1].Image is nil")
	}
	if got[1].Image.MIMEType != "image/png" {
		t.Errorf("block[1].Image.MIMEType: got %q, want image/png", got[1].Image.MIMEType)
	}
	if !reflect.DeepEqual(got[1].Image.Data, pngBytes) {
		t.Errorf("block[1].Image.Data: got %v, want %v", got[1].Image.Data, pngBytes)
	}
}

// TestBuildBlocks_SkipsMalformedBase64 (defensive — Codex M-1) — a
// single corrupt base64 image must NOT abort buildBlocks; the text
// block survives and no image block is emitted.
func TestBuildBlocks_SkipsMalformedBase64(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "look"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{
					MIME:       "image/png",
					DataBase64: "not-valid-base64-!@#$",
				}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 1 {
		t.Fatalf("expected 1 block (text only — malformed image skipped), got %d", len(got))
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Errorf("block[0] kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[0].Text == nil {
		t.Fatal("text block has nil Text")
	}
	if !contains(got[0].Text.Content, "look") {
		t.Errorf("text content lost: got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_MultipleImages_PreservesOrder — two ContentKindImage
// parts in the same message produce three blocks (text + 2 images)
// with the images in message order.
func TestBuildBlocks_MultipleImages_PreservesOrder(t *testing.T) {
	img1 := []byte{0x01, 0x02}
	img2 := []byte{0x03, 0x04}
	b1 := base64.StdEncoding.EncodeToString(img1)
	b2 := base64.StdEncoding.EncodeToString(img2)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "two images"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{MIME: "image/png", DataBase64: b1}},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{MIME: "image/jpeg", DataBase64: b2}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 3 {
		t.Fatalf("expected 3 blocks (text + 2 images), got %d", len(got))
	}
	if !reflect.DeepEqual(got[1].Image.Data, img1) {
		t.Errorf("block[1] data: got %v, want %v", got[1].Image.Data, img1)
	}
	if got[1].Image.MIMEType != "image/png" {
		t.Errorf("block[1] MIME: got %q, want image/png", got[1].Image.MIMEType)
	}
	if !reflect.DeepEqual(got[2].Image.Data, img2) {
		t.Errorf("block[2] data: got %v, want %v", got[2].Image.Data, img2)
	}
	if got[2].Image.MIMEType != "image/jpeg" {
		t.Errorf("block[2] MIME: got %q, want image/jpeg", got[2].Image.MIMEType)
	}
}

// Example_buildBlocks is a runnable godoc example (TRST-07). The
// Output: block is validated by `go test -run Example`. Lowercase
// suffix style because buildBlocks is unexported.
func Example_buildBlocks() {
	req := &canonical.ChatRequest{
		System: "Be brief.",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hi."},
			}},
		},
	}
	blocks := buildBlocks(req)
	fmt.Println(blocks[0].Text.Content)
	// Output:
	// [System]
	// Be brief.
	//
	// [User]
	// Hi.
}

// contains is a tiny helper to keep test-string assertions readable
// without importing strings in every test file.
func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
