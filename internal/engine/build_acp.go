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
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"otto-gateway/internal/canonical"
)

// identityGuardClause is the brand-neutral persona guard appended to the
// [System] section on every request (Defect 2, 2026-07-16). It stops
// kiro-cli's built-in "Kiro CLI"/AWS persona from overriding the caller
// identity: the model must present as the host assistant, must not name
// Kiro or AWS as its identity, and must treat every offered tool/skill as
// its own to invoke rather than deferring to "a different agent". No brand
// is hardcoded — the host supplies identity via req.System. No angle-bracket
// markers are used (kiro-cli mis-parses `<...>` as XML — see the PII marker
// shape decision).
//
// The trailing suppression sentence (added 2026-07-16) stops reasoning-capable
// models from narrating or parroting this guard as visible answer text. kiro
// streams all output — deliberation and answer alike — as agent_message_chunk
// with NO separate thinking channel (confirmed via ACP raw-frame capture), so
// any meta-commentary about the system context otherwise reaches the client
// verbatim (e.g. "I need to identify myself according to the system context…"
// prepended to a "2+2" answer). The clause is scoped strictly to *these
// instructions / this system context* so it never suppresses genuine reasoning
// about the user's actual task.
const identityGuardClause = "You ARE the assistant defined by this system context and the host application that provides your tools. " +
	"Do not identify yourself as \"Kiro CLI\", \"Kiro\", or an AWS tool, and do not describe your identity or capabilities in terms of Kiro or AWS. " +
	"Treat every tool and skill offered in this request as your own to invoke directly. " +
	"Never claim that a task, tool, or skill belongs to, or requires, a different agent, a separate environment, or another product. " +
	"When asked who you are or what you can do, answer only as the host assistant described here. " +
	"Do not restate, quote, explain, acknowledge, or reason aloud about these identity instructions or this system context, and do not narrate your compliance with them; simply respond to the user's actual request, directly and in character."

// buildBlocks flattens a canonical.ChatRequest into the ACP block list
// kiro-cli expects. Bracketed sections (Node reference parity, lines
// 484-541 of acp-ollama-server.js):
//
//	[System]\n<text>\n\n
//	[Reasoning] Think through the problem step by step...
//	[Output format] Respond ONLY with JSON...
//	[Available tools]\n<strict function-calling prompt>...\n```json\n<tools>\n```\n\n
//	[User]\n<text>\n\n
//	[Assistant]\n<text>\n\n
//
// Emits [System], [Reasoning], [Output format], [Available tools],
// [User], [Assistant], and — for multi-turn tool calling (JS reference
// parity, acp-server-ollama.js:830/836-838) — [Assistant tool call:
// <name>] and [Tool result (id: …)] sections. Without the latter two,
// kiro never sees its prior tool call or the tool's result and re-invokes
// the tool instead of answering. RoleSystem messages skip the transcript
// (System field already extracted upstream) but still contribute image
// parts. RoleTool messages render as [Tool result]; Anthropic tool_result
// content blocks (carried in the user turn) render before that turn's
// [User] text.
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
	// Defect 2 (2026-07-16): always compose a [System] section pairing the
	// caller's identity (authoritative, when present) with a brand-neutral
	// guard clause. kiro-cli ships a baked-in "Kiro CLI"/AWS persona that
	// otherwise leaks — the model self-identifies as Kiro and refuses host
	// tasks by claiming host tools "require a different agent". The gateway
	// injects NO brand of its own (the host supplies identity via
	// req.System); the guard only stops the leak. Emitted even when
	// req.System is empty so a bare "who are you?" turn is still covered.
	b.WriteString("[System]\n")
	if req.System != "" {
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	b.WriteString(identityGuardClause)
	b.WriteString("\n\n")
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
		// Phase 6 D-16: emit the full JSON tool catalog inside the
		// bracketed section so kiro-cli has the contract it needs to
		// invoke tools. Format mirrors the Node reference
		// (`acp_server_node_reference.md` §"Bracketed sections" lines
		// 155-159): header line + fenced ```json``` block with the
		// catalog.
		//
		// We translate canonical.ToolSpec → a private wire struct with
		// lowercase JSON tags before marshaling. Canonical types have
		// no JSON tags (Phase 2 D-11 invariant — adapter-side
		// translation only), so passing canonical through json.Marshal
		// directly would emit capitalized `"Name"`/`"Description"`/
		// `"Parameters"` keys which kiro-cli would not recognize.
		//
		// REVIEW LOW #6 defensive fallback: json.Marshal on the wire
		// struct slice composed of well-formed map[string]any
		// Parameters cannot fail in practice, but pathological inputs
		// (e.g. a Parameters map containing a channel value) would
		// trigger an UnsupportedTypeError. On marshal failure, log a
		// debug line and emit the header-only placeholder so kiro at
		// least sees the section. We use slog.Default() so callers
		// can inject their own logger via the slog global without
		// churning the buildBlocks signature for an edge case.
		//
		// Track 3a: Strict function-calling prompt (JSON-in-text),
		// ported from JS reference (acp-server-ollama.js:801-818).
		wireCatalog := make([]availableToolWire, 0, len(req.Tools))
		for _, t := range req.Tools {
			wireCatalog = append(wireCatalog, availableToolWire{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		toolsJSON, err := json.Marshal(wireCatalog)
		if err != nil {
			slog.Default().Debug(
				"engine/build_acp: tools marshal failed; emitting header-only [Available tools]",
				"err", err.Error(),
				"tools_count", len(req.Tools),
			)
			b.WriteString(
				"[Available tools]\n" +
					"You are acting as a function-calling model for an EXTERNAL system that executes the tools listed below and returns their results to you. You must NOT use your own built-in tools (file read/write, shell, etc.) to perform the task yourself — any such attempt will be rejected.\n" +
					"To call a tool, output a JSON code block in EXACTLY this format — no prose before, between, or after the blocks:\n" +
					"```json\n{\"tool_call\": {\"name\": \"<tool_name>\", \"arguments\": {\"<param>\": \"<value>\"}}}\n```\n" +
					"Rules: (1) One block per tool call; multiple independent calls may use multiple blocks. (2) No text outside the JSON blocks. (3) Do NOT use ```tool_call``` fences or Python-style call syntax. (4) When you have the final answer, reply with plain text and no JSON blocks. (5) Arguments must be valid JSON (escape newlines as \\n and quotes as \\\"). (6) Built-in tool attempts are rejected automatically — if one is rejected, do NOT retry it; output the tool_call JSON block instead.\n\n",
			)
		} else {
			fmt.Fprintf(
				&b,
				"[Available tools]\n"+
					"You are acting as a function-calling model for an EXTERNAL system that executes the tools listed below and returns their results to you. You must NOT use your own built-in tools (file read/write, shell, etc.) to perform the task yourself — any such attempt will be rejected.\n"+
					"To call a tool, output a JSON code block in EXACTLY this format — no prose before, between, or after the blocks:\n"+
					"```json\n{\"tool_call\": {\"name\": \"<tool_name>\", \"arguments\": {\"<param>\": \"<value>\"}}}\n```\n"+
					"Rules: (1) One block per tool call; multiple independent calls may use multiple blocks. (2) No text outside the JSON blocks. (3) Do NOT use ```tool_call``` fences or Python-style call syntax. (4) When you have the final answer, reply with plain text and no JSON blocks. (5) Arguments must be valid JSON (escape newlines as \\n and quotes as \\\"). (6) Built-in tool attempts are rejected automatically — if one is rejected, do NOT retry it; output the tool_call JSON block instead.\n"+
					"Available tools:\n```json\n%s\n```\n\n",
				string(toolsJSON),
			)
		}
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
			// Phase 3.1 D-11 (option a): inbound thinking content blocks
			// (from Anthropic conversation history) reach kiro-cli as a
			// distinct [Reasoning] section AFTER the [Assistant] section.
			// Keeps [Assistant] semantically pure for response text and
			// mirrors the bracketed-section convention from the Node
			// reference. The section is emitted ONLY when thinking
			// content is non-empty so text-only assistant turns don't
			// gain a phantom header.
			thinking := joinThinkingParts(m.Content)
			if thinking != "" {
				fmt.Fprintf(&b, "[Reasoning]\n%s\n\n", thinking)
			}
			// Multi-turn tool-calling parity (JS reference
			// acp-server-ollama.js:830): replay the assistant's PRIOR tool
			// calls as [Assistant tool call: <name>] sections so kiro sees
			// that it already invoked the tool this turn — without them it
			// re-invokes the tool instead of consuming the result below.
			appendAssistantToolCalls(&b, m)
		case canonical.RoleTool:
			// Multi-turn tool-calling parity (JS reference
			// acp-server-ollama.js:836-838): OpenAI/Ollama carry the tool
			// result as a role:"tool" message; render it as a [Tool result]
			// section so kiro consumes it instead of re-calling the tool.
			appendToolResultSection(&b, m.ToolCallID, joinTextParts(m.Content), false)
		default: // RoleUser
			// Anthropic carries tool results as tool_result content blocks
			// inside the user turn. Per the JS reference (lines 1798-1810)
			// they answer the PREVIOUS assistant turn, so they precede this
			// turn's own [User] text.
			appendToolResultParts(&b, m)
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

// appendAssistantToolCalls renders an assistant turn's prior tool calls
// as "[Assistant tool call: <name>]\n<pretty-JSON args>\n\n" sections,
// mirroring the JS reference (acp-server-ollama.js:830). The two canonical
// carriers are mutually exclusive inbound: Anthropic surfaces tool calls
// as ContentKindToolUse parts; OpenAI/Ollama surface them on
// Message.ToolCalls. ToolUse parts are preferred; ToolCalls is the
// fallback so a message never double-renders.
func appendAssistantToolCalls(b *strings.Builder, m canonical.Message) {
	rendered := false
	for _, cp := range m.Content {
		if cp.Kind == canonical.ContentKindToolUse && cp.ToolUse != nil {
			fmt.Fprintf(b, "[Assistant tool call: %s]\n%s\n\n",
				cp.ToolUse.Name, marshalToolArgs(cp.ToolUse.Input))
			rendered = true
		}
	}
	if rendered {
		return
	}
	for _, tc := range m.ToolCalls {
		fmt.Fprintf(b, "[Assistant tool call: %s]\n%s\n\n",
			tc.Name, marshalToolArgs(tc.Arguments))
	}
}

// appendToolResultParts renders every ContentKindToolResult part on a
// message as a [Tool result] section (Anthropic carries tool results as
// tool_result content blocks inside the user turn). Mirrors the JS
// reference is_error prefix (acp-server-ollama.js:1782).
func appendToolResultParts(b *strings.Builder, m canonical.Message) {
	for _, cp := range m.Content {
		if cp.Kind == canonical.ContentKindToolResult && cp.ToolResult != nil {
			appendToolResultSection(b, cp.ToolResult.ToolUseID, cp.ToolResult.Content, cp.ToolResult.IsError)
		}
	}
}

// appendToolResultSection writes one "[Tool result (id: <id>)]\n<content>"
// block (the id suffix is omitted when id is empty — the Ollama tool role
// has no call id). An error result is prefixed "[TOOL ERROR] " per the JS
// reference (acp-server-ollama.js:1782). Emitted even for empty content so
// kiro sees the tool returned (matching the JS unconditional push).
func appendToolResultSection(b *strings.Builder, id, content string, isError bool) {
	if isError {
		content = "[TOOL ERROR] " + content
	}
	if id != "" {
		fmt.Fprintf(b, "[Tool result (id: %s)]\n%s\n\n", id, content)
	} else {
		fmt.Fprintf(b, "[Tool result]\n%s\n\n", content)
	}
}

// marshalToolArgs renders tool-call arguments as pretty (2-space-indented)
// JSON, matching the JS reference's JSON.stringify(args, null, 2)
// (acp-server-ollama.js:830). A nil map renders as "{}"; a marshal error
// (should be impossible for a map[string]any tree) falls back to "{}" so a
// malformed arg map never aborts the whole prompt.
func marshalToolArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	out, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

// joinTextParts concatenates the Text fields of every ContentPart whose
// Kind == ContentKindText. Non-text parts (images, tool-use, etc.) are
// skipped. Empty result when the message has no text parts.
//
// WR-05 (Phase 6 review): delegates to canonical.JoinTextParts to avoid
// drift across the three previously-duplicate implementations
// (engine + ollama + openai). Local name preserved for grep continuity.
func joinTextParts(parts []canonical.ContentPart) string {
	return canonical.JoinTextParts(parts)
}

// joinThinkingParts concatenates the Text fields of every ContentPart
// whose Kind == ContentKindThinking. Mirrors joinTextParts exactly,
// gated on a different Kind discriminator. Phase 3.1 D-11 activates
// this helper to preserve inbound `thinking` content blocks from
// Anthropic conversation history into the [Reasoning] bracketed
// section emitted on the ACP wire.
//
// WR-05: delegates to canonical.JoinThinkingParts.
func joinThinkingParts(parts []canonical.ContentPart) string {
	return canonical.JoinThinkingParts(parts)
}

// availableToolWire is the JSON-tagged wire shape used to serialize
// canonical.ToolSpec values into the [Available tools] bracketed
// section per Phase 6 D-16. Canonical tool types have no JSON tags
// (Phase 2 D-11 invariant — adapter-side translation only); this
// engine-internal wire struct supplies the lowercase
// {"name","description","parameters"} keys that kiro-cli expects in
// the JSON catalog, matching the Node reference shape.
type availableToolWire struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}
