// internal/plugin/compress/stages_test.go
package compress

import (
	"strings"
	"testing"
	"unicode/utf8"

	"otto-gateway/internal/canonical"
)

func TestNormalizeWhitespace(t *testing.T) {
	// Trailing space/tab stripped at line ends; 5 newlines → 2. INTERIOR
	// horizontal whitespace is untouched — no [ \t]{2,} collapse (that
	// would corrupt indented code; deliberate divergence from Node).
	in := "line1   \nline2\n\n\n\n\nline3\ta  \t b"
	want := "line1\nline2\n\nline3\ta  \t b"
	if got := normalizeWhitespace(in); got != want {
		t.Errorf("normalizeWhitespace = %q, want %q", got, want)
	}
}

func TestNormalizeWhitespace_PreservesCodeIndentation(t *testing.T) {
	// Regression lock for review MAJOR-1: indentation-significant code in
	// old transcript messages must survive stage 1 byte-for-byte.
	code := "def f():\n    if x:\n        return {\n            \"k\": 1,\n        }\nkey:\n  nested: true\n\ttab-indented"
	if got := normalizeWhitespace(code); got != code {
		t.Errorf("indentation mutated:\ngot  %q\nwant %q", got, code)
	}
}

func TestNormalizeWhitespace_AcceptedLossBoundary(t *testing.T) {
	// Third-pass MINOR: stage 1 is LOW-LOSS, not lossless. These fixtures
	// pin the exact accepted mutation boundary so it can never silently
	// widen (and so the docs' honesty claims stay checkable).
	cases := []struct{ name, in, want string }{
		// ACCEPTED loss: Markdown hard break (two trailing spaces) → soft break.
		{"markdown-hard-break", "line one  \nline two", "line one\nline two"},
		// ACCEPTED loss: 3+ blank-line runs collapse, even inside what a
		// client meant as an exact fixture.
		{"blank-run", "a\n\n\n\nb", "a\n\nb"},
		// PRESERVED: single and double newlines, interior runs, tabs.
		{"exact-double", "a\n\nb", "a\n\nb"},
		{"interior-runs", "col1    col2\tcol3", "col1    col2\tcol3"},
	}
	for _, c := range cases {
		if got := normalizeWhitespace(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizeMessageWhitespace_MultipartJoinPreserved(t *testing.T) {
	// Review HIGH-1: same-kind parts are joined DIRECTLY by ACP
	// (canonical.JoinTextParts / JoinThinkingParts) — normalizing each
	// part independently must not strip the trailing whitespace that
	// glues a non-final part to the next one.
	m := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "foo "},
			{Kind: canonical.ContentKindText, Text: "bar"},
			{Kind: canonical.ContentKindThinking, Text: "alpha "},
			{Kind: canonical.ContentKindThinking, Text: "beta"},
		},
	}
	normalizeMessageWhitespace(&m)
	if got := canonical.JoinTextParts(m.Content); got != "foo bar" {
		t.Errorf("joined text = %q, want %q", got, "foo bar")
	}
	if got := canonical.JoinThinkingParts(m.Content); got != "alpha beta" {
		t.Errorf("joined thinking = %q, want %q", got, "alpha beta")
	}
}

func TestNormalizeMessageWhitespace_InteriorLinesOfNonFinalPartNormalized(t *testing.T) {
	// The non-final part's COMPLETE lines (through the last '\n') are
	// still normalized; only its final PARTIAL line (continuing into the
	// next part) is left untouched.
	m := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "l1  \nl2 "},
			{Kind: canonical.ContentKindText, Text: "x"},
		},
	}
	normalizeMessageWhitespace(&m)
	if got := canonical.JoinTextParts(m.Content); got != "l1\nl2 x" {
		t.Errorf("joined text = %q, want %q", got, "l1\nl2 x")
	}
}

func TestNormalizeMessageWhitespace_LastPartFullyNormalized(t *testing.T) {
	m := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "end  "},
		},
	}
	normalizeMessageWhitespace(&m)
	if got := canonical.JoinTextParts(m.Content); got != "end" {
		t.Errorf("joined text = %q, want %q", got, "end")
	}
}

func TestMiddleTruncate(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := middleTruncate(short, 50); got != short {
		t.Errorf("short text mutated: %q", got)
	}

	long := strings.Repeat("a", 1000) + strings.Repeat("b", 1000)
	got := middleTruncate(long, 100)
	if !strings.HasPrefix(got, strings.Repeat("a", 100)) {
		t.Error("head not preserved")
	}
	if !strings.HasSuffix(got, strings.Repeat("b", 100)) {
		t.Error("tail not preserved")
	}
	if !strings.Contains(got, "chars omitted") {
		t.Error("elision marker missing")
	}
	if len(got) >= len(long) {
		t.Error("no shrinkage")
	}
}

func TestMiddleTruncate_RuneSafe(t *testing.T) {
	// Multibyte runes positioned to straddle the cut points.
	long := strings.Repeat("é", 2000) // 2 bytes each
	got := middleTruncate(long, 101)  // 101 lands mid-rune
	if !utf8.ValidString(got) {
		t.Error("middleTruncate produced invalid UTF-8")
	}
}

func TestTruncateToolResults(t *testing.T) {
	big := strings.Repeat("x", 5000)
	m := canonical.Message{
		Role: canonical.RoleTool,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: big},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: big}},
		},
	}
	truncateToolResults(&m, 100)
	if len(m.Content[0].Text) >= 5000 {
		t.Error("RoleTool text part not truncated")
	}
	if len(m.Content[1].ToolResult.Content) >= 5000 {
		t.Error("ToolResult content not truncated")
	}
	if m.Content[1].ToolResult.ToolUseID != "t1" {
		t.Error("ToolUseID lost — tool pairing broken")
	}
}

func TestTruncateToolResults_NonToolTextUntouched(t *testing.T) {
	big := strings.Repeat("x", 5000)
	m := textMsg(canonical.RoleAssistant, big)
	truncateToolResults(&m, 100)
	if m.Content[0].Text != big {
		t.Error("assistant text part truncated — stage 2 must only touch tool output")
	}
}

func TestCollapseDuplicates(t *testing.T) {
	big := strings.Repeat("payload ", 50) // > minDupLen
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, big),
		textMsg(canonical.RoleAssistant, "short"), // < minDupLen — never collapsed
		textMsg(canonical.RoleUser, big),          // duplicate of #0
		textMsg(canonical.RoleAssistant, big),     // same text, different role — NOT a duplicate
	}
	all := func(int) bool { return true }
	collapseDuplicates(msgs, all)

	if flattenText(msgs[0]) != big {
		t.Error("first occurrence must survive")
	}
	if got := flattenText(msgs[2]); !strings.Contains(got, "duplicate of earlier message #1") {
		t.Errorf("duplicate not collapsed: %q", got)
	}
	if flattenText(msgs[3]) != big {
		t.Error("different role collapsed — role must be part of the identity key")
	}
}

func TestCollapseDuplicates_RespectsMutable(t *testing.T) {
	big := strings.Repeat("payload ", 50)
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, big),
		textMsg(canonical.RoleUser, big),
	}
	collapseDuplicates(msgs, func(i int) bool { return false })
	if flattenText(msgs[1]) != big {
		t.Error("immutable message collapsed")
	}
}

func TestCollapseDuplicates_DelimiterInjectionSafe(t *testing.T) {
	// Review 2 MAJOR-6: canonical text may legally contain 0x1E/0x1F.
	// A single text part "A<RS>k<US>B" must NOT collide with separate
	// text "A" + thinking "B" parts — length prefixes make the encoding
	// injection-proof.
	a := strings.Repeat("A", 150)
	b := strings.Repeat("B", 150)
	forged := textMsg(canonical.RoleUser, a+"\x1ek\x1f"+b)
	genuine := canonical.Message{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: a},
			{Kind: canonical.ContentKindThinking, Text: b},
		},
	}
	if dupKey(forged) == dupKey(genuine) {
		t.Error("dupKey forged via embedded delimiters")
	}
}

func TestCollapseDuplicates_DifferentCiphertextIsNotDuplicate(t *testing.T) {
	// Third-pass MAJOR-2: different [PII:Email:...] payloads can encode
	// DIFFERENT email addresses. They must NEVER collapse as "exact
	// duplicates" — dupKey uses exact ciphertext, not entity-only
	// normalization.
	pre := strings.Repeat("same message body ", 15)
	m1 := textMsg(canonical.RoleUser, pre+"[PII:Email:AAAAaaaa1111_-]")
	m2 := textMsg(canonical.RoleUser, pre+"[PII:Email:BBBBbbbb2222_-]")
	msgs := []canonical.Message{m1, m2}
	full := flattenText(msgs[1])
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != full {
		t.Error("messages differing only in PII ciphertext were collapsed — the model could echo the wrong decryptable token")
	}
	// IDENTICAL ciphertext (kiro echoing the same token) still collapses.
	m3 := textMsg(canonical.RoleUser, pre+"[PII:Email:AAAAaaaa1111_-]")
	msgs2 := []canonical.Message{m1, m3}
	collapseDuplicates(msgs2, func(int) bool { return true })
	if !strings.Contains(flattenText(msgs2[1]), "duplicate of earlier message #1") {
		t.Error("byte-identical messages failed to collapse")
	}
}

func TestCollapseDuplicates_DifferentToolCallIDIsNotDuplicate(t *testing.T) {
	// Revision-4 MAJOR: byte-identical tool output satisfying DIFFERENT
	// invocations (call_A vs call_B) is not a duplicate — ACP renders
	// the ToolCallID into the [Tool result (id:…)] section.
	out := strings.Repeat("identical tool output ", 15)
	mk := func(id string) canonical.Message {
		m := textMsg(canonical.RoleTool, out)
		m.ToolCallID = id
		return m
	}
	msgs := []canonical.Message{mk("call_A"), mk("call_B")}
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != out {
		t.Error("results for different tool invocations collapsed")
	}
	// Same ID (a true resend) still collapses.
	msgs2 := []canonical.Message{mk("call_A"), mk("call_A")}
	collapseDuplicates(msgs2, func(int) bool { return true })
	if !strings.Contains(flattenText(msgs2[1]), "duplicate of earlier message #1") {
		t.Error("identical-ID duplicate failed to collapse")
	}
}

func TestCollapseDuplicates_MultipartNotConfusedWithFlatText(t *testing.T) {
	// Review MAJOR-5: a message {text "A", tool-result "B"} and a plain
	// text message "AB" flatten to the same string but are structurally
	// different (ACP serializes a [Tool result] section for one and not
	// the other). dupKey must keep them distinct.
	a := strings.Repeat("A", 150)
	b := strings.Repeat("B", 150)
	msgs := []canonical.Message{
		{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: a},
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "call-1", Content: b}},
			},
		},
		textMsg(canonical.RoleUser, a+b), // same flattened bytes, different structure
	}
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != a+b {
		t.Error("structurally different message collapsed as duplicate")
	}
}

func TestReplaceText_PreservesStructure(t *testing.T) {
	m := canonical.Message{
		Role:       canonical.RoleTool,
		ToolCallID: "call-9",
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "old text"},
			{Kind: canonical.ContentKindThinking, Text: "old thinking"}, // prose — dropped, not structural
			{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{DataBase64: "imgdata"}},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t2", Content: "old result"}},
		},
	}
	replaceText(&m, "[stub]")
	if m.ToolCallID != "call-9" {
		t.Error("ToolCallID lost")
	}
	if flattenText(m) != "[stub]" {
		t.Errorf("flattened = %q, want only the stub once", flattenText(m))
	}
	foundImage := false
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindImage && p.Image != nil && p.Image.DataBase64 == "imgdata" {
			foundImage = true
		}
	}
	if !foundImage {
		t.Error("image part dropped")
	}
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindToolResult && p.ToolResult.ToolUseID != "t2" {
			t.Error("ToolResult.ToolUseID lost")
		}
	}
}
