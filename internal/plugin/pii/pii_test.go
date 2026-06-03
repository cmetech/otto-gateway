// Phase 8 Plan 08-04 Task 1 — Wave 0 scaffold for PIIRedactionHook
// (the slice's payload).
//
// Tests exercise:
//   - Disabled hook is a no-op (deep-equal before/after).
//   - Enabled hook mutates ContentParts[].Text in place.
//   - ToolUse.Input map is recursed; KEYS preserved verbatim.
//   - ToolResult.Content (string) is walked.
//   - ChatRequest.System is walked (RESEARCH OQ-5 disposition).
//   - Per-request counter resets across calls (T-8-PII-COUNTER).
//   - Summary populated on ctx-stamped Summary (D-04 producer side).
//   - No-match still leaves Summary present-but-empty.
//   - Name() returns "PIIRedactionHook".
//   - Describe() exposes safe-only fields (no hash_key / patterns).
//
// IMPORTANT (planner deviation, deviation Rule 3):
//   - canonical.Message has Content []ContentPart, NOT a `Content string`
//     legacy field. The plan's Test 28 referencing req.Messages[0].Content
//     as a string is not realizable against the actual canonical types;
//     the test is repurposed to cover the ContentPart Text path with a
//     CreditCard recognizer fixture (the original intent).
//   - canonical.ToolUsePart.Input is map[string]any (NOT *map[string]any).
//   - canonical.ToolResultPart.Content is string (NOT any/map[string]any).
//   - The Summary seam contract per 08-03-SUMMARY's "Next Phase Readiness"
//     note: PIIRedactionHook reads pii.SummaryFromContext(ctx) and
//     populates an EXISTING *Summary pointer. Test fixtures must stamp
//     ctx = pii.WithSummary(ctx, pii.NewSummary()) BEFORE calling Before,
//     mirroring slice 3's logging_test.go pattern. Slice 5 adapter
//     middleware will perform this stamp on the production path.
//
// All tests must fail with `undefined: PIIRedactionHook` before Task 5
// implements pii.go.
package pii

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// textPart returns a ContentPart with Kind=Text and the given text.
func textPart(text string) canonical.ContentPart {
	return canonical.ContentPart{Kind: canonical.ContentKindText, Text: text}
}

// userMessage returns a Role=User message with a single text ContentPart.
func userMessage(text string) canonical.Message {
	return canonical.Message{
		Role:    canonical.RoleUser,
		Content: []canonical.ContentPart{textPart(text)},
	}
}

// freshHook constructs a fully-enabled PIIRedactionHook in replace mode
// for tests that don't care about other config knobs.
func freshHook(mode string) *PIIRedactionHook {
	return &PIIRedactionHook{
		Enabled:     true,
		Mode:        mode,
		Recognizers: Recognizers,
		HashKey:     testHashKey,
	}
}

// withCtxSummary returns a ctx carrying a freshly-stamped pii.Summary,
// mirroring the slice-5 adapter-middleware stamp that production uses.
func withCtxSummary(t *testing.T) (context.Context, *Summary) {
	t.Helper()
	s := NewSummary()
	ctx := WithSummary(context.Background(), s)
	return ctx, s
}

// TestPIIRedactionHook_DisabledIsNoop asserts that Enabled=false makes
// Before a no-op (req is bit-identical before/after).
func TestPIIRedactionHook_DisabledIsNoop(t *testing.T) {
	hook := &PIIRedactionHook{
		Enabled:     false,
		Mode:        "replace",
		Recognizers: Recognizers,
	}
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Model:    "auto",
		Messages: []canonical.Message{userMessage("Email me at corey@cmetech.io please")},
	}
	want := req.Messages[0].Content[0].Text
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if got := req.Messages[0].Content[0].Text; got != want {
		t.Errorf("disabled hook mutated text: got %q, want %q (unchanged)", got, want)
	}
}

// TestPIIRedactionHook_EnabledMutatesContentParts asserts the basic
// Pre-mutation path: an email in a ContentPart's Text gets replaced.
func TestPIIRedactionHook_EnabledMutatesContentParts(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{userMessage("Email me at corey@cmetech.io please")},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	got := req.Messages[0].Content[0].Text
	if strings.Contains(got, "corey@cmetech.io") {
		t.Errorf("expected raw email removed; got %q", got)
	}
	if !strings.Contains(got, "[EMAIL") {
		t.Errorf("expected [EMAIL token present; got %q", got)
	}
}

// TestPIIRedactionHook_LegacyMessageContent_Walked — re-purposed per the
// planner-deviation note above. Asserts a CreditCard in ContentParts[].Text
// is redacted (originally targeted a non-existent string field).
func TestPIIRedactionHook_LegacyMessageContent_Walked(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{userMessage("PIN: 4111111111111111")},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	got := req.Messages[0].Content[0].Text
	if !strings.Contains(got, "[CREDITCARD") {
		t.Errorf("expected [CREDITCARD token in %q", got)
	}
}

// TestPIIRedactionHook_ToolUseInputRecursed asserts ToolUse.Input is
// walked: map KEYS preserved verbatim, string LEAVES redacted, non-string
// values unchanged.
func TestPIIRedactionHook_ToolUseInputRecursed(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{
				{
					Kind: canonical.ContentKindToolUse,
					ToolUse: &canonical.ToolUsePart{
						ID:   "tu-1",
						Name: "send_email",
						Input: map[string]any{
							"to":       "corey@cmetech.io",
							"cc":       []any{"sam@x.com"},
							"priority": float64(1),
						},
					},
				},
			},
		}},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	got := req.Messages[0].Content[0].ToolUse.Input
	// Keys present verbatim.
	for _, k := range []string{"to", "cc", "priority"} {
		if _, ok := got[k]; !ok {
			t.Errorf("expected key %q in ToolUse.Input; got keys %v", k, keysOf(got))
		}
	}
	// String leaf "to" redacted.
	toStr, _ := got["to"].(string)
	if strings.Contains(toStr, "corey@cmetech.io") || !strings.Contains(toStr, "[EMAIL") {
		t.Errorf("ToolUse.Input.to: got %q, want [EMAIL token", toStr)
	}
	// String leaf inside cc slice redacted.
	ccSlice, _ := got["cc"].([]any)
	if len(ccSlice) != 1 {
		t.Fatalf("ToolUse.Input.cc: expected len 1, got %v", ccSlice)
	}
	ccStr, _ := ccSlice[0].(string)
	if strings.Contains(ccStr, "sam@x.com") || !strings.Contains(ccStr, "[EMAIL") {
		t.Errorf("ToolUse.Input.cc[0]: got %q, want [EMAIL token", ccStr)
	}
	// Non-string leaf unchanged.
	if got["priority"] != float64(1) {
		t.Errorf("ToolUse.Input.priority: got %v, want 1 (float64)", got["priority"])
	}
}

// TestPIIRedactionHook_ToolResultContentRecursed asserts ToolResult.Content
// (a string field per canonical/chat.go) is redacted.
func TestPIIRedactionHook_ToolResultContentRecursed(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{
					ToolUseID: "tu-1",
					Content:   "Customer email: corey@cmetech.io",
				},
			}},
		}},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	got := req.Messages[0].Content[0].ToolResult.Content
	if strings.Contains(got, "corey@cmetech.io") {
		t.Errorf("ToolResult.Content: raw email leaked: %q", got)
	}
	if !strings.Contains(got, "[EMAIL") {
		t.Errorf("ToolResult.Content: expected [EMAIL token: %q", got)
	}
}

// TestPIIRedactionHook_ChatRequestSystem_Walked asserts ChatRequest.System
// is walked (RESEARCH OQ-5 disposition: walk it; operator-side PII may
// exist).
func TestPIIRedactionHook_ChatRequestSystem_Walked(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		System: "Forward to corey@cmetech.io if urgent",
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if strings.Contains(req.System, "corey@cmetech.io") {
		t.Errorf("System: raw email leaked: %q", req.System)
	}
	if !strings.Contains(req.System, "[EMAIL") {
		t.Errorf("System: expected [EMAIL token: %q", req.System)
	}
}

// TestPIIRedactionHook_CounterScope_PerRequest asserts T-8-PII-COUNTER:
// within one request, same email twice shares the counter slot; across
// requests, the counter resets.
func TestPIIRedactionHook_CounterScope_PerRequest(t *testing.T) {
	hook := freshHook("replace")
	// Request A: same email twice → both should share the same token
	// (the planner spec is ambiguous on the exact shape: "[EMAIL_1]"
	// for both is the intra-request referential-identity property —
	// per RESEARCH Pitfall 4. Counter-suffix is active on FIRST match,
	// so subsequent identical values reuse the counter.)
	ctxA, _ := withCtxSummary(t)
	reqA := &canonical.ChatRequest{
		Messages: []canonical.Message{
			userMessage("Reply to corey@cmetech.io"),
			userMessage("Cc corey@cmetech.io too"),
		},
	}
	if _, err := hook.Before(ctxA, reqA); err != nil {
		t.Fatalf("Before A: %v", err)
	}
	got1 := reqA.Messages[0].Content[0].Text
	got2 := reqA.Messages[1].Content[0].Text
	if !strings.Contains(got1, "[EMAIL_1]") {
		t.Errorf("intra-request first match: got %q, want [EMAIL_1]", got1)
	}
	// Same value the second time → same canonical-token (per Pitfall 4
	// "preserves intra-prompt referential identity"). The simplest
	// canonical implementation reuses the first counter for an
	// identical canonical value within one request.
	if !strings.Contains(got2, "[EMAIL_1]") {
		t.Errorf("intra-request second identical match: got %q, want [EMAIL_1] (referential identity)", got2)
	}

	// Request B: fresh ctx + fresh request → counter MUST reset.
	ctxB, _ := withCtxSummary(t)
	reqB := &canonical.ChatRequest{
		Messages: []canonical.Message{userMessage("Reply to corey@cmetech.io")},
	}
	if _, err := hook.Before(ctxB, reqB); err != nil {
		t.Fatalf("Before B: %v", err)
	}
	gotB := reqB.Messages[0].Content[0].Text
	if !strings.Contains(gotB, "[EMAIL_1]") {
		t.Errorf("cross-request counter reset: got %q, want [EMAIL_1] (counter must reset)", gotB)
	}
}

// TestPIIRedactionHook_PopulatesSummary asserts the D-04 producer
// contract: after Before, the ctx-stamped Summary holds Counts() with
// per-entity tallies.
func TestPIIRedactionHook_PopulatesSummary(t *testing.T) {
	hook := freshHook("replace")
	ctx, summary := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			userMessage("Reach me at corey@cmetech.io"),
			userMessage("Also try sam@x.com"),
		},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	counts := summary.Counts()
	if counts["Email"] != 2 {
		t.Errorf("Counts[Email]: got %d, want 2", counts["Email"])
	}
	if counts["IPv4"] != 0 {
		t.Errorf("Counts[IPv4]: got %d, want 0", counts["IPv4"])
	}
}

// TestPIIRedactionHook_NoMatchProducesEmptyCountsButSummaryPresent
// asserts the no-match path: ctx still carries a Summary; Counts() is
// non-nil but empty.
func TestPIIRedactionHook_NoMatchProducesEmptyCountsButSummaryPresent(t *testing.T) {
	hook := freshHook("replace")
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{userMessage("Hello, world.")},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	s, ok := SummaryFromContext(ctx)
	if !ok {
		t.Fatal("Summary missing from ctx (test fixture stamped it)")
	}
	counts := s.Counts()
	if counts == nil {
		t.Error("Counts(): got nil, want non-nil empty map")
	}
	if len(counts) != 0 {
		t.Errorf("Counts: got %v, want empty map", counts)
	}
}

// TestPIIRedactionHook_Name asserts the filter-discovery name.
func TestPIIRedactionHook_Name(t *testing.T) {
	got := (&PIIRedactionHook{}).Name()
	if got != "PIIRedactionHook" {
		t.Errorf("Name: got %q, want %q", got, "PIIRedactionHook")
	}
}

// TestPIIRedactionHook_Describe_NoSecrets asserts Describe exposes
// safe-only fields. The HashKey (a secret) and the regex patterns must
// NOT appear in the config map.
func TestPIIRedactionHook_Describe_NoSecrets(t *testing.T) {
	hook := &PIIRedactionHook{
		Enabled:     true,
		Mode:        "hash",
		HashKey:     []byte("topsecret"),
		Recognizers: Recognizers,
	}
	kind, cfg := hook.Describe()
	if kind != "Pre,Post" {
		t.Errorf("kind: got %q, want %q", kind, "Pre,Post")
	}
	if cfg["enabled"] != true {
		t.Errorf("config[enabled]: got %v, want true", cfg["enabled"])
	}
	if cfg["mode"] != "hash" {
		t.Errorf("config[mode]: got %v, want hash", cfg["mode"])
	}
	entities, ok := cfg["entities"].([]string)
	if !ok {
		t.Fatalf("config[entities]: got %T, want []string", cfg["entities"])
	}
	// Active entity list must include the original six plus any
	// newly-registered recognizers; assert by name-set inclusion rather
	// than exact equality so adding a recognizer doesn't break this
	// invariant.
	wantSet := map[string]bool{
		"Email": true, "IPv4": true, "IPv6": true,
		"SSN": true, "CreditCard": true, "USPhone": true,
	}
	gotSet := make(map[string]bool, len(entities))
	for _, e := range entities {
		gotSet[e] = true
	}
	for name := range wantSet {
		if !gotSet[name] {
			t.Errorf("config[entities] missing required recognizer %q (got %v)", name, entities)
		}
	}
	// Forbid any key resembling a secret or revealing patterns.
	for k, v := range cfg {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "hash_key") || strings.Contains(lower, "hashkey") ||
			strings.Contains(lower, "encrypt_key") || strings.Contains(lower, "encryptkey") ||
			lower == "key" || strings.Contains(lower, "patterns") {
			t.Errorf("Describe config exposes suspicious key %q (T-8-LEAK)", k)
		}
		// Defensive: stringified value must not contain the secret.
		if str, ok := v.(string); ok && strings.Contains(str, "topsecret") {
			t.Errorf("Describe config value for key %q contains secret 'topsecret'", k)
		}
	}
}

// PII-ENCRYPT-04 — per-entity action resolution and encryptActive predicate.

func TestActionFor_FallbackToMode(t *testing.T) {
	h := &PIIRedactionHook{Mode: "mask"}
	if got := h.actionFor("Email"); got != "mask" {
		t.Errorf("actionFor with no override: got %q, want %q", got, "mask")
	}
}

func TestActionFor_OverrideWins(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "mask",
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	if got := h.actionFor("Email"); got != "encrypt" {
		t.Errorf("actionFor with override: got %q, want %q", got, "encrypt")
	}
	if got := h.actionFor("SSN"); got != "mask" {
		t.Errorf("actionFor unlisted entity: got %q, want fallback %q", got, "mask")
	}
}

func TestEncryptActive_GlobalMode(t *testing.T) {
	h := &PIIRedactionHook{Mode: "encrypt"}
	if !h.encryptActive() {
		t.Error("encryptActive: Mode=encrypt should report active")
	}
}

func TestEncryptActive_EntityOverride(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "replace",
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	if !h.encryptActive() {
		t.Error("encryptActive: any encrypt in EntityActions should report active")
	}
}

func TestEncryptActive_Inactive(t *testing.T) {
	h := &PIIRedactionHook{
		Mode:          "mask",
		EntityActions: map[string]string{"Email": "drop", "SSN": "hash"},
	}
	if h.encryptActive() {
		t.Error("encryptActive: no encrypt anywhere should report inactive")
	}
}

// PII-ENCRYPT-05 — Before flips req.Stream when encrypt is active.

func TestBefore_StreamDisabledWhenEncryptActive(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Enabled:     true,
		Mode:        "encrypt",
		EncryptKey:  k,
	}
	req := &canonical.ChatRequest{Stream: true}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if req.Stream {
		t.Error("Before: req.Stream should be false after Before when encrypt is active")
	}
}

func TestBefore_StreamUnchangedWhenEncryptInactive(t *testing.T) {
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Enabled:     true,
		Mode:        "replace",
	}
	req := &canonical.ChatRequest{Stream: true}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if !req.Stream {
		t.Error("Before: req.Stream should remain true when encrypt is NOT active")
	}
}

func TestBefore_PerEntityActionResolution(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{
		Recognizers:   Recognizers,
		Enabled:       true,
		Mode:          "mask",
		EncryptKey:    k,
		EntityActions: map[string]string{"Email": "encrypt"},
	}
	req := &canonical.ChatRequest{
		System: "contact corey@cmetech.io and 123-45-6789",
	}
	if _, err := h.Before(context.Background(), req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	// Email should be encrypted (token shape).
	if !strings.Contains(req.System, "[PII:Email:") {
		t.Errorf("Email should be encrypted: got System=%q", req.System)
	}
	// SSN should be masked (contains '*'), NOT encrypted.
	if strings.Contains(req.System, "[PII:SSN:") {
		t.Errorf("SSN should be masked, not encrypted: got System=%q", req.System)
	}
	if !strings.Contains(req.System, "*") {
		t.Errorf("SSN mask should contain '*': got System=%q", req.System)
	}
}

// PII-ENCRYPT-06 — After (PostHook) decrypt sweep.

func TestAfter_NoopWhenEncryptInactive(t *testing.T) {
	h := &PIIRedactionHook{Enabled: true, Mode: "replace"}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "hello [PII:Email:fakepayload]"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	// No-op: text is unchanged even though a token-shaped string is present.
	if resp.Message.Content[0].Text != "hello [PII:Email:fakepayload]" {
		t.Errorf("After should be no-op when encrypt inactive: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_RoundTripDecrypt(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	tok, err := EncryptValue(k, "Email", "corey@cmetech.io")
	if err != nil {
		t.Fatalf("EncryptValue: %v", err)
	}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Sure, I'll email " + tok + " for you."},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	want := "Sure, I'll email corey@cmetech.io for you."
	if resp.Message.Content[0].Text != want {
		t.Errorf("After decrypt: got %q, want %q", resp.Message.Content[0].Text, want)
	}
}

func TestAfter_MangledTokenLeftInPlace(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	// Hand-crafted shape-valid but cryptographically-garbage token.
	garbage := "[PII:Email:AAAAAAAAAAAAAAAA]"
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "see " + garbage + " for details"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if !strings.Contains(resp.Message.Content[0].Text, garbage) {
		t.Errorf("mangled token should be left verbatim: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_MultipleTokens(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	t1, err := EncryptValue(k, "Email", "alice@example.com")
	if err != nil {
		t.Fatalf("EncryptValue t1: %v", err)
	}
	t2, err := EncryptValue(k, "Email", "bob@example.com")
	if err != nil {
		t.Fatalf("EncryptValue t2: %v", err)
	}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "From " + t1 + " to " + t2},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	want := "From alice@example.com to bob@example.com"
	if resp.Message.Content[0].Text != want {
		t.Errorf("After multi-token: got %q, want %q", resp.Message.Content[0].Text, want)
	}
}

func TestAfter_SkipsNonTextParts(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindThinking, Text: "this should not be scanned [PII:Email:fake]"},
			},
		},
	}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	// Non-text parts are skipped — text field stays verbatim.
	if !strings.Contains(resp.Message.Content[0].Text, "[PII:Email:fake]") {
		t.Errorf("non-Text part should be skipped: got %q", resp.Message.Content[0].Text)
	}
}

func TestAfter_CompileTimePostHookSatisfied(t *testing.T) {
	// Compile-time guard: var _ engine.PostHook = (*PIIRedactionHook)(nil)
	// will fail to BUILD if the After signature drifts. This test is a
	// belt-and-suspenders marker that exercises the interface at runtime.
	var _ interface {
		After(context.Context, *canonical.ChatRequest, *canonical.ChatResponse) error
	} = (*PIIRedactionHook)(nil)
}

// PII-ENCRYPT-06b — defensive After tests + reason-category classification.

func TestAfter_NilResp(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, nil); err != nil {
		t.Errorf("After(nil resp): expected nil err, got %v", err)
	}
}

func TestAfter_EmptyContent(t *testing.T) {
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	h := &PIIRedactionHook{Enabled: true, Mode: "encrypt", EncryptKey: k}
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: nil}}
	if err := h.After(context.Background(), &canonical.ChatRequest{}, resp); err != nil {
		t.Errorf("After(empty Content): expected nil err, got %v", err)
	}
}

func TestClassifyDecryptErr_Categories(t *testing.T) {
	// Driven by DecryptToken's documented wrapping prefixes from
	// encrypt.go. If those prefixes change, classifyDecryptErr must
	// change in lock-step — this test pins the contract.
	k, err := DeriveKey("test")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	// bad_base64
	_, err = DecryptToken(k, "Email", "not-valid-base64!!!")
	if err == nil || classifyDecryptErr(err) != "bad_base64" {
		t.Errorf("bad_base64 path: err=%v reason=%q", err, classifyDecryptErr(err))
	}
	// gcm_open (16 base64url chars = 12 bytes = nonce-size, zero-length
	// ct, GCM Open errors with "message authentication failed").
	_, err = DecryptToken(k, "Email", "AAAAAAAAAAAAAAAA")
	if err == nil || classifyDecryptErr(err) != "gcm_open" {
		t.Errorf("gcm_open path: err=%v reason=%q", err, classifyDecryptErr(err))
	}
	// payload_too_short (8 base64url chars = 6 bytes, under 12-byte nonce).
	_, err = DecryptToken(k, "Email", "AAAAAAAA")
	if err == nil || classifyDecryptErr(err) != "payload_too_short" {
		t.Errorf("payload_too_short path: err=%v reason=%q", err, classifyDecryptErr(err))
	}
}

// PII-ENCRYPT-07 — Describe surfaces both Pre and Post kinds, plus
// decrypt_active and entity_actions for operator visibility on
// /health/hooks. EncryptKey is NEVER published (T-8-LEAK extension).

func TestDescribe_KindIncludesPost(t *testing.T) {
	h := &PIIRedactionHook{Recognizers: Recognizers, Mode: "encrypt"}
	kind, _ := h.Describe()
	if kind != "Pre,Post" {
		t.Errorf("Describe kind: got %q, want %q", kind, "Pre,Post")
	}
}

func TestDescribe_DecryptActiveFlag(t *testing.T) {
	tests := []struct {
		name        string
		hook        *PIIRedactionHook
		wantDecrypt bool
	}{
		{"mode=encrypt", &PIIRedactionHook{Recognizers: Recognizers, Mode: "encrypt"}, true},
		{"entity-override", &PIIRedactionHook{Recognizers: Recognizers, Mode: "replace", EntityActions: map[string]string{"Email": "encrypt"}}, true},
		{"no encrypt", &PIIRedactionHook{Recognizers: Recognizers, Mode: "mask"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := tc.hook.Describe()
			got, _ := cfg["decrypt_active"].(bool)
			if got != tc.wantDecrypt {
				t.Errorf("decrypt_active: got %v, want %v", got, tc.wantDecrypt)
			}
		})
	}
}

func TestDescribe_NeverPublishesEncryptKey(t *testing.T) {
	h := &PIIRedactionHook{
		Recognizers: Recognizers,
		Mode:        "encrypt",
		EncryptKey:  []byte("SECRET-SHOULD-NOT-LEAK"),
	}
	_, cfg := h.Describe()
	// Serialize to JSON (the on-wire shape used by /health/hooks) and
	// scan the bytes for the sentinel. This catches not only string-typed
	// fields but also []byte-typed fields, which json.Marshal encodes as
	// base64 — a future drift that put h.EncryptKey directly in the map
	// would leak it as base64 ciphertext, which the previous string-only
	// type assertion missed entirely.
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal cfg: %v", err)
	}
	// Plain text leak: direct string value somewhere in the map.
	if strings.Contains(string(b), "SECRET-SHOULD-NOT-LEAK") {
		t.Errorf("Describe leaked EncryptKey plaintext via JSON: %s", string(b))
	}
	// Base64 leak: []byte encoded by json.Marshal.
	encoded := base64.StdEncoding.EncodeToString([]byte("SECRET-SHOULD-NOT-LEAK"))
	if strings.Contains(string(b), encoded) {
		t.Errorf("Describe leaked EncryptKey base64 via JSON: %s", string(b))
	}
}

// redactText drives Before against text wrapped in a single-message
// ChatRequest and returns the mutated text. Companion to freshHook for
// e2e recognizer integration tests.
func redactText(t *testing.T, hook *PIIRedactionHook, text string) string {
	t.Helper()
	ctx, _ := withCtxSummary(t)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{userMessage(text)},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	return req.Messages[0].Content[0].Text
}

// TestIMEI_ContextAnchored_Integration: a 15-digit number with NO context
// keyword must be left alone by the redact pipeline. With "IMEI:" prefix
// it must be redacted.
func TestIMEI_ContextAnchored_Integration(t *testing.T) {
	hook := freshHook("replace")
	hook.EnabledEntities = []string{"IMEI"}

	bare := redactText(t, hook, "bare run 490154203237518 here")
	if !strings.Contains(bare, "490154203237518") {
		t.Errorf("expected bare 15-digit run NOT to be redacted without imei context; got %q", bare)
	}

	anchored := redactText(t, hook, "IMEI: 490154203237518")
	if strings.Contains(anchored, "490154203237518") {
		t.Errorf("expected redaction with 'IMEI:' prefix; got %q", anchored)
	}
	if !strings.Contains(anchored, "[IMEI") {
		t.Errorf("expected [IMEI token; got %q", anchored)
	}
}
