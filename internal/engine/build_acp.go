// Package engine — bracketed-section block flattening (D-02 + D-09 footnote).
//
// buildBlocks is the SINGLE SOURCE OF TRUTH across all three adapter
// surfaces (Ollama / OpenAI / Anthropic) for canonical → ACP block
// translation. Output is a leading text block (the bracketed-section
// transcript) followed by zero or more inline image blocks, one per
// ContentKindImage part encountered in req.Messages (Codex M-1 — without
// image emission, Ollama messages[].images would round-trip through
// canonical only to be silently dropped at the ACP boundary).
package engine

import (
	"encoding/base64"
	"fmt"
	"strings"

	"loop24-gateway/internal/canonical"
)

// buildBlocks flattens a canonical.ChatRequest into the ACP block list
// kiro-cli expects. Bracketed sections (Node reference parity, lines
// 484-541 of acp-ollama-server.js):
//
//	[System]\n<text>\n\n
//	[Reasoning] Think through the problem step by step...
//	[Output format] Respond ONLY with JSON...
//	[Available tools]\nEmit a tool_call ACP notification...\n```json\n<tools>\n```\n\n
//	[User]\n<text>\n\n
//	[Assistant]\n<text>\n\n
//
// Phase 2 emits [System], [Reasoning], [Output format], [User], and
// [Assistant] sections; [Available tools] / [Assistant tool call] /
// [Tool result] are forward-design seams kept here for Phase 3.1 / 6
// activation. RoleSystem messages skip the transcript (System field
// already extracted upstream) but still contribute image parts. RoleTool
// messages are dormant in Phase 2.
//
// Image emission: every ContentPart with Kind == ContentKindImage and
// Image != nil produces one BlockKindImage block appended AFTER the
// text block, in encounter order (message order, then content-part
// order within each message). The wire DataBase64 string is decoded;
// malformed base64 is skipped silently (defensive — a corrupt single
// image must not abort the whole prompt).
func buildBlocks(req *canonical.ChatRequest) []canonical.Block {
	if req == nil {
		return []canonical.Block{
			{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: ""}},
		}
	}

	var b strings.Builder
	if req.System != "" {
		fmt.Fprintf(&b, "[System]\n%s\n\n", req.System)
	}
	if req.Think {
		b.WriteString("[Reasoning] Think through the problem step by step before answering. Show your reasoning.\n\n")
	}
	if req.Format != nil {
		// Forward-design seam — Phase 2 Ollama does not produce Format,
		// but if upstream populates it, emit a bracketed-section hint.
		// Schema details are dormant; Phase 3.1 / 6 will flesh this out.
		fmt.Fprintf(&b, "[Output format] Respond ONLY in %s.\n\n", req.Format.Type)
	}
	if len(req.Tools) > 0 {
		// Forward-design seam — Phase 6 will flesh out the tool-catalog
		// emission. Phase 2 emits only a placeholder header so the
		// section exists in the bracketed-section format.
		b.WriteString("[Available tools]\nEmit a tool_call ACP notification to invoke any of the registered tools.\n\n")
	}

	for _, m := range req.Messages {
		switch m.Role {
		case canonical.RoleSystem:
			// Already extracted via req.System; skip transcript.
		case canonical.RoleAssistant:
			text := joinTextParts(m.Content)
			if text != "" {
				fmt.Fprintf(&b, "[Assistant]\n%s\n\n", text)
			}
		case canonical.RoleTool:
			// Dormant in Phase 2 (Phase 6 fills the [Tool result] body).
		default: // RoleUser
			text := joinTextParts(m.Content)
			if text != "" {
				fmt.Fprintf(&b, "[User]\n%s\n\n", text)
			}
		}
	}

	text := strings.TrimRight(b.String(), "\n")
	out := []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: text}},
	}

	// Image emission (Codex M-1 / D-09 footnote). Walk messages a
	// second time so the text section is fully assembled first; this
	// also keeps the per-role text aggregation simple (no inline
	// image-vs-text discriminator inside the switch above).
	for _, m := range req.Messages {
		for _, part := range m.Content {
			if part.Kind != canonical.ContentKindImage || part.Image == nil {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(part.Image.DataBase64)
			if err != nil {
				// Defensive — skip the malformed image but keep the
				// prompt flowing. A single bad base64 must not abort
				// buildBlocks.
				continue
			}
			out = append(out, canonical.Block{
				Kind: canonical.BlockKindImage,
				Image: &canonical.ImageBlock{
					Source:   "",
					MIMEType: part.Image.MIME,
					Data:     data,
				},
			})
		}
	}

	return out
}

// joinTextParts concatenates the Text fields of every ContentPart whose
// Kind == ContentKindText. Non-text parts (images, tool-use, etc.) are
// skipped. Empty result when the message has no text parts.
func joinTextParts(parts []canonical.ContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Kind == canonical.ContentKindText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
