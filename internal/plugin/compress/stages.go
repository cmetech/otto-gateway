// internal/plugin/compress/stages.go

package compress

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"otto-gateway/internal/canonical"
)

// minDupLen: messages shorter than this are never collapsed as duplicates
// (short acks like "ok" legitimately repeat). Node-parity value.
const minDupLen = 200

var (
	trailingWSRe = regexp.MustCompile(`(?m)[ \t]+$`)
	tripleNLRe   = regexp.MustCompile(`\n{3,}`)
)

// normalizeWhitespace is stage 1: strip trailing whitespace at line ends
// and collapse 3+ consecutive newlines to 2. That is ALL — interior
// horizontal whitespace is never touched. The Node gateway additionally
// collapses [ \t]{2,} runs to one space; that pass rewrites the
// indentation of any Python/YAML/Makefile snippet in older messages into
// semantically different code (review MAJOR-1), so it is deliberately
// omitted here.
func normalizeWhitespace(text string) string {
	text = trailingWSRe.ReplaceAllString(text, "")
	return tripleNLRe.ReplaceAllString(text, "\n\n")
}

// normalizeCarrierChunk applies stage 1 to one chunk of a multi-part
// ACP carrier. Same-kind parts are joined DIRECTLY on the wire
// (canonical.JoinTextParts), so a non-final chunk's last line CONTINUES
// into the next chunk — stripping its trailing whitespace would merge
// words in the joined prompt ("foo " + "bar" → "foobar"). Non-final
// chunks therefore keep their final partial line byte-for-byte; only
// complete lines (through the last '\n') are normalized.
func normalizeCarrierChunk(text string, lastChunk bool) string {
	if lastChunk {
		return normalizeWhitespace(text)
	}
	i := strings.LastIndexByte(text, '\n')
	if i < 0 {
		return text // single partial line continuing into the next chunk
	}
	return normalizeWhitespace(text[:i+1]) + text[i+1:]
}

// normalizeMessageWhitespace applies stage 1 to every prose-bearing part
// (Text, Thinking — both serialized as prose sections — and ToolResult
// content). Text and Thinking parts are carrier-aware (review HIGH-1):
// same-kind parts of one message are joined DIRECTLY by ACP
// (canonical.JoinTextParts / JoinThinkingParts), so only the LAST part of
// each kind gets full normalization; earlier parts keep their trailing
// partial line untouched so the joined prompt is unaffected. ToolResult
// content is its own ACP section per part, so it is always fully
// normalized.
func normalizeMessageWhitespace(m *canonical.Message) {
	lastText, lastThinking := -1, -1
	for j := range m.Content {
		switch m.Content[j].Kind {
		case canonical.ContentKindText:
			lastText = j
		case canonical.ContentKindThinking:
			lastThinking = j
		}
	}
	for j := range m.Content {
		p := &m.Content[j]
		switch p.Kind {
		case canonical.ContentKindText:
			p.Text = normalizeCarrierChunk(p.Text, j == lastText)
		case canonical.ContentKindThinking:
			p.Text = normalizeCarrierChunk(p.Text, j == lastThinking)
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				tr := *p.ToolResult // copy-on-write: alias-proof
				tr.Content = normalizeWhitespace(tr.Content)
				p.ToolResult = &tr
			}
		}
	}
}

// runeSafeCut returns s truncated to at most n bytes without splitting a
// UTF-8 rune (backs off to the previous rune start).
func runeSafeCut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// runeSafeTail returns the last (at most) n bytes of s without splitting
// a UTF-8 rune (advances past a partial leading rune).
func runeSafeTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// middleTruncate is stage 2's cut: keep the head and tail (the signal in
// tool output lives at the edges), elide the middle with a marker. The
// +64 slack means near-limit text is left alone rather than "truncated"
// into something the same size. keep is in BYTES (rune-safe cuts); the
// marker's omitted count is in runes so the label "chars" is honest.
// The keep > len/2 guard both short-circuits pointless truncation AND
// makes the keep*2 below overflow-safe for absurd (but representable)
// keep values.
func middleTruncate(text string, keep int) string {
	if keep > len(text)/2 {
		return text // head+tail would cover (nearly) everything anyway
	}
	if len(text) <= keep*2+64 {
		return text
	}
	head := runeSafeCut(text, keep)
	tail := runeSafeTail(text, keep)
	omitted := utf8.RuneCountInString(text) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	return fmt.Sprintf("%s\n…[compressed: %d chars omitted]…\n%s", head, omitted, tail)
}

// truncateToolResults is stage 2: middle-truncate stale tool output.
// Applies to ToolResultPart.Content in any message and to text parts of
// RoleTool messages. Assistant/user prose is NOT touched by this stage.
func truncateToolResults(m *canonical.Message, keep int) {
	for j := range m.Content {
		p := &m.Content[j]
		switch p.Kind {
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				tr := *p.ToolResult
				tr.Content = middleTruncate(tr.Content, keep)
				p.ToolResult = &tr
			}
		case canonical.ContentKindText:
			if m.Role == canonical.RoleTool {
				p.Text = middleTruncate(p.Text, keep)
			}
		}
	}
}

// dupKey builds a STRUCTURAL identity for duplicate detection: role plus
// every content part with an explicit kind discriminator, plus tool-call
// identities. Every variable-length field is LENGTH-PREFIXED
// ("<len>:<bytes>") — canonical text is an unrestricted string, so bare
// separator bytes could be forged by message content; length prefixes
// make the encoding injection-proof (review 2 MAJOR-6).
//
// All text is EXACT — including PII ciphertext. Never normalize PII
// tokens here: entity-only equivalence would collapse messages that
// differ only in their encrypted values (third-pass MAJOR-2).
// Encrypt-mode duplicates therefore rarely collapse (fresh nonces);
// that missed optimization is the accepted price of never producing a
// false "exact duplicate". The message-level ToolCallID participates
// too (revision-4 MAJOR): ACP renders it into the [Tool result (id:…)]
// section, so byte-identical output for DIFFERENT invocations is not a
// duplicate.
func dupKey(m canonical.Message) string {
	var b strings.Builder
	field := func(tag string, s string) {
		fmt.Fprintf(&b, "%s%d:%s", tag, len(s), s)
	}
	fmt.Fprintf(&b, "r%d", m.Role)
	field("I", m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindText:
			field("t", p.Text)
		case canonical.ContentKindThinking:
			field("k", p.Text)
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				field("rI", p.ToolResult.ToolUseID)
				fmt.Fprintf(&b, "e%t", p.ToolResult.IsError)
				field("rC", p.ToolResult.Content)
			}
		case canonical.ContentKindToolUse:
			if p.ToolUse != nil {
				inputJSON, _ := json.Marshal(p.ToolUse.Input) // best-effort identity
				field("uI", p.ToolUse.ID)
				field("uN", p.ToolUse.Name)
				field("uA", string(inputJSON))
			}
		case canonical.ContentKindImage:
			if p.Image != nil {
				field("iM", p.Image.MIME)
				field("iD", p.Image.DataBase64)
			}
		}
	}
	for _, tc := range m.ToolCalls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		field("cI", tc.ID)
		field("cN", tc.Name)
		field("cA", string(argsJSON))
	}
	return b.String()
}

// duplicateStubMarker replaces a collapsed message's prose. NON-referential
// on purpose (review LOW-5): the marker used to point at the earlier
// occurrence by index ("#N"), but stage 4 can later elide message #N,
// leaving a dangling reference in what the model sees. The stub only
// needs to tell the model "this content already appeared", not exactly
// where.
const duplicateStubMarker = "[duplicate of an earlier message omitted]"

// collapseDuplicates is stage 3: replace exact structural repeats (same
// dupKey, VISIBLE acpProse length >= minDupLen) with a short, non-
// referential stub. Agent loops re-send identical blobs turn after turn —
// this is where the big wins usually are. Eligibility is role-aware
// (review MEDIUM-3): a message whose only prose lives on a carrier ACP
// never renders for its role (e.g. RoleUser Thinking) has NO visible
// prose to collapse — stubbing it would ADD a visible section the wire
// previously rendered nothing for, which is compression making the
// prompt BIGGER. seen only needs first-occurrence REGISTRATION (not the
// index) now that the stub no longer references it by number.
func collapseDuplicates(msgs []canonical.Message, mutable func(int) bool) {
	seen := make(map[string]struct{})
	for i := range msgs {
		key := dupKey(msgs[i])
		if !mutable(i) || len(acpProse(msgs[i])) < minDupLen {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
			}
			continue
		}
		if _, ok := seen[key]; ok {
			replaceText(&msgs[i], duplicateStubMarker)
		} else {
			seen[key] = struct{}{}
		}
	}
}

// replaceText swaps a message's VISIBLE prose content for a stub while
// preserving everything structural: ToolCallID, ToolCalls, image parts,
// ToolUse parts, and ToolResult part identity (ToolUseID / IsError).
//
// Visibility is ROLE-AWARE (review HIGH-2 / MEDIUM-3), mirroring
// build_acp's per-role branches (acpProse, build_acp.go:171-215): Text is
// visible for every serialized role; Thinking is visible ONLY for
// RoleAssistant ([Reasoning] renders only there — never for RoleUser/
// RoleTool); ToolResult.Content is visible ONLY for RoleUser (build_acp's
// user branch renders ToolResult parts; a ToolResult part riding a
// RoleAssistant or RoleTool message is never serialized). The FIRST
// visible prose part encountered gets the stub (a Text-kind part for
// Text/Thinking, or tr.Content for ToolResult); subsequent visible prose
// is dropped (Text/Thinking) or emptied (ToolResult.Content = "") — the
// stub already stands in for the whole message's prose. Carriers
// INVISIBLE for this message's role pass through completely UNTOUCHED:
// they cost nothing on the wire, so turning one into a visible carrier
// would make the serialized prompt BIGGER, not smaller. There is no
// "nothing replaced" fallback — every caller only invokes replaceText on
// a message that already has >= minDupLen/minCandidateLen bytes of
// VISIBLE prose, so a visible carrier to stub always exists; introducing
// one that didn't previously exist on the wire would be pure regression.
func replaceText(m *canonical.Message, stub string) {
	replaced := false
	out := make([]canonical.ContentPart, 0, len(m.Content))
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindText:
			if !replaced {
				out = append(out, canonical.ContentPart{Kind: canonical.ContentKindText, Text: stub})
				replaced = true
			}
			// subsequent Text parts drop — the stub stands in
		case canonical.ContentKindThinking:
			if m.Role != canonical.RoleAssistant {
				out = append(out, p) // invisible on this role — untouched
				continue
			}
			if !replaced {
				out = append(out, canonical.ContentPart{Kind: canonical.ContentKindText, Text: stub})
				replaced = true
				continue
			}
			// subsequent visible Thinking parts drop
		case canonical.ContentKindToolResult:
			if p.ToolResult == nil || m.Role != canonical.RoleUser {
				out = append(out, p) // invisible on this role — untouched
				continue
			}
			tr := *p.ToolResult
			if !replaced {
				tr.Content = stub
				replaced = true
			} else {
				tr.Content = ""
			}
			p.ToolResult = &tr
			out = append(out, p)
		default:
			out = append(out, p) // images, ToolUse: structural, pass through
		}
	}
	m.Content = out
}
