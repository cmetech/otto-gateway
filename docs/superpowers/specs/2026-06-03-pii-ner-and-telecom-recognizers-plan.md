# PII NER + Telecom Recognizers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add seven telecom-domain regex recognizers (SIP_URI, IMEI, IMSI, MSISDN, MAC_ADDRESS, COORDINATES, SITE) and a `jdkato/prose/v2` NER recognizer that emits PERSON and LOCATION entities to `internal/plugin/pii/`, integrated into the existing Pre/Post encrypt round-trip without breaking single-binary distribution.

**Architecture:** Position-based span collection refactor of `redact()` so we can (a) implement context-keyword anchoring for IMEI/IMSI/MSISDN/SITE within a fixed character window and (b) merge regex spans with NER spans under greedy non-overlap (regex wins ties). The `Recognizer` struct grows a `ContextKeywords []string` field (nil = no context required). A new `nerEngine` type wraps a lazily-initialized `*prose.Document` factory behind a `PII_NER_ENABLED` gate so the prose model is never loaded when NER is off. The existing `PIIRedactionHook`, encrypt mode, and chain wiring stay structurally unchanged — new entities slot into the same `ApplyMode` dispatch and the same `[PII:Entity:…]` token shape.

**Tech Stack:** Go 1.24, stdlib `regexp` + `unicode`, `github.com/jdkato/prose/v2` (pure Go, model bundled in the module — no CGo, no model download). Test harness: stdlib `testing` per existing `*_test.go` conventions.

---

## Reference Material

- Style reference: `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md` (this plan follows the same section structure inside tasks).
- Existing PII core:
  - `internal/plugin/pii/recognizers.go` — `Recognizer` struct + the six built-in regex recognizers (Email, IPv4, IPv6, SSN, CreditCard, USPhone).
  - `internal/plugin/pii/pii.go` — `PIIRedactionHook` with `Before` (Pre, redact) + `After` (Post, decrypt) and the inner `redact` closure (lines 271–295) that calls `ReplaceAllStringFunc` per recognizer.
  - `internal/plugin/pii/modes.go` — `ApplyMode(mode, entity, value, counter, hashKey, encryptKey)` with the five action arms.
  - `internal/plugin/pii/encrypt.go` — `DeriveKey`/`EncryptValue`/`DecryptToken` (AES-256-GCM with entity as AAD).
- Existing config: `internal/config/config.go`
  - `validatePIIEntities` (line 674) — hand-coded six-entity allowlist.
  - `parsePIIEntityActions` (line 703) — six-entity × five-action allowlist for `PII_ENTITY_ACTIONS`.
- Existing wiring: `cmd/otto-gateway/main.go:207-224` — constructs the single `PIIRedactionHook` instance registered in both `Pre` and `Post` chains.
- Operator docs: `docs/operating.md` (lines 101–103 CLI table, lines 219–224 env-var table, lines 266–278 boot errors, line 473+ encrypt-streaming note).

---

## File Structure

**Modified:**
- `internal/plugin/pii/recognizers.go` — extend `Recognizer` with `ContextKeywords []string`; add seven telecom recognizers; export `defaultContextWindow` constant.
- `internal/plugin/pii/pii.go` — refactor `redact` to position-based span collection with overlap arbitration; thread the `nerEngine` through; introduce `(*PIIRedactionHook).collectSpans`.
- `internal/plugin/pii/modes.go` — no signature change; only the table-driven tests grow.
- `internal/config/config.go` — expand `validatePIIEntities` and `parsePIIEntityActions` allowlists; add `PII_NER_ENABLED` field + parse.
- `cmd/otto-gateway/main.go` — wire `cfg.PIINEREnabled` into `piiHook.NER` (a new field) at construction.
- `docs/operating.md` — extend env-var table, CLI table, boot-error list, accuracy-ceiling section.
- `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md` — append §11 "Recognizer Expansion (2026-06-03)" with the new entity list and prose accuracy note.
- `README.md` — extend the PII recognizer enumeration if present (check at execution time).
- `go.mod` / `go.sum` — add `github.com/jdkato/prose/v2`.

**Created:**
- `internal/plugin/pii/ner.go` — `nerEngine` type, lazy `sync.Once` init, `Detect(text string) []span`.
- `internal/plugin/pii/ner_test.go` — PERSON/LOCATION coverage, off-by-default verification, encrypt round-trip on a NER-detected entity.
- `internal/plugin/pii/contextual.go` — `hasContextWithin(text string, matchStart, matchEnd int, keywords []string, window int) bool` helper.
- `internal/plugin/pii/contextual_test.go` — context-window helper coverage.
- `internal/plugin/pii/spans.go` — `span` type + `mergeSpansGreedy` helper.
- `internal/plugin/pii/spans_test.go` — overlap-arbitration coverage.

**Boundaries:**
- All new code lives under `internal/plugin/pii/`. No new public packages.
- Config remains the single source of truth for the entity-name + action-name allowlists (TRST-04: hand-coded in `internal/config/`, not imported from `internal/plugin/pii/`, matching the existing precedent on `validatePIIEntities`).
- NER is opt-in via `PII_NER_ENABLED=true`; the prose model is never loaded when off (no `sync.Once` fire, no allocation).

---

## Scope Check

This plan covers two related but separable subsystems sharing a `redact()` refactor: telecom regex recognizers (Part A) and prose NER integration (Part B). Both depend on the same span-collection refactor (Task 1), so splitting into two plans would force redundant refactor work. Bundled execution is correct here.

---

## Self-Test Commands

Run these at every test step:

```bash
# Single package, verbose
go test -v ./internal/plugin/pii/...

# Full build + lint + test
go build ./...
go vet ./...
go test ./...
```

Binary-size baseline (run before Task 9 and after Task 14):

```bash
go build -trimpath -ldflags="-s -w" -o /tmp/otto-gateway-size ./cmd/otto-gateway
ls -lh /tmp/otto-gateway-size
```

---

## Task 1: Extend `Recognizer` with `ContextKeywords` + position-based redact

**Files:**
- Modify: `internal/plugin/pii/recognizers.go` — struct field + constant.
- Create: `internal/plugin/pii/contextual.go`
- Create: `internal/plugin/pii/contextual_test.go`
- Create: `internal/plugin/pii/spans.go`
- Create: `internal/plugin/pii/spans_test.go`
- Modify: `internal/plugin/pii/pii.go` — refactor `redact`.
- Test: `internal/plugin/pii/pii_test.go` — confirm existing tests still pass.

### Why this task exists

The current `redact` closure in `pii.go:271-295` uses `regexp.ReplaceAllStringFunc` per recognizer, which (a) doesn't expose match position so context-window checks are impossible, and (b) iterates sequentially so each recognizer sees already-replaced text. To support both context-anchored recognizers (IMEI vs IMSI disambiguation) and NER overlap arbitration (regex spans must be visible to NER), we collect spans against the **original** input, merge them, then rewrite in a single reverse-order pass.

### Step 1.1: Write failing tests for `hasContextWithin`

- [ ] **Create `internal/plugin/pii/contextual_test.go`:**

```go
// Tests for hasContextWithin — verifies the ±N-char window keyword
// check that powers IMEI/IMSI/MSISDN/SITE context disambiguation.
package pii

import "testing"

func TestHasContextWithin_KeywordBefore(t *testing.T) {
	text := "IMEI: 490154203237518 captured at the gateway"
	start := len("IMEI: ")
	end := start + len("490154203237518")
	if !hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("expected to find 'imei' before the match within 50 chars")
	}
}

func TestHasContextWithin_KeywordAfter(t *testing.T) {
	text := "value 490154203237518 (imei) was observed"
	start := len("value ")
	end := start + len("490154203237518")
	if !hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("expected to find 'imei' after the match within 50 chars")
	}
}

func TestHasContextWithin_NoKeyword(t *testing.T) {
	text := "a bare 15-digit run 490154203237518 with no context"
	start := len("a bare 15-digit run ")
	end := start + len("490154203237518")
	if hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("did not expect any keyword to match in plain context")
	}
}

func TestHasContextWithin_CaseInsensitive(t *testing.T) {
	text := "IMSI: 310150123456789 — subscriber number"
	start := len("IMSI: ")
	end := start + len("310150123456789")
	if !hasContextWithin(text, start, end, []string{"imsi"}, 50) {
		t.Error("expected case-insensitive match of 'imsi' against 'IMSI:'")
	}
}

func TestHasContextWithin_OutsideWindow(t *testing.T) {
	prefix := "imei "
	pad := make([]byte, 200)
	for i := range pad {
		pad[i] = 'x'
	}
	text := prefix + string(pad) + " 490154203237518"
	start := len(prefix) + len(pad) + 1
	end := start + len("490154203237518")
	if hasContextWithin(text, start, end, []string{"imei"}, 50) {
		t.Error("keyword sits beyond the 50-char window; must NOT match")
	}
}

func TestHasContextWithin_EmptyKeywords(t *testing.T) {
	text := "anything here 12345"
	if !hasContextWithin(text, 0, 5, nil, 50) {
		t.Error("nil keywords list must short-circuit to true (no context required)")
	}
}
```

- [ ] **Step 1.2: Run tests; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run TestHasContextWithin`
Expected: `undefined: hasContextWithin`

- [ ] **Step 1.3: Implement `hasContextWithin`**

Create `internal/plugin/pii/contextual.go`:

```go
// Contextual keyword matching for context-anchored recognizers (IMEI,
// IMSI, MSISDN, SITE). Go regexp has no variable-width lookbehind, so
// context is checked programmatically: do any of the keywords appear,
// case-insensitively, within a ±window byte range surrounding a regex
// match? Window is bytes not runes for simplicity; all our context
// keywords are ASCII so this is equivalent in practice.
//
// nil/empty keywords short-circuits to true so the same code path
// handles both context-free and context-required recognizers.

package pii

import "strings"

// defaultContextWindow is the default ±byte radius around a regex match
// in which one of the recognizer's ContextKeywords must appear. 50 bytes
// ≈ 8–10 English words on either side, matching the conventional
// presidio "this is an IMEI: 49015…" anchoring style.
const defaultContextWindow = 50

// hasContextWithin reports whether any of keywords appears (case-
// insensitively) within window bytes before matchStart or after matchEnd
// in text. nil/empty keywords returns true.
func hasContextWithin(text string, matchStart, matchEnd int, keywords []string, window int) bool {
	if len(keywords) == 0 {
		return true
	}
	lo := matchStart - window
	if lo < 0 {
		lo = 0
	}
	hi := matchEnd + window
	if hi > len(text) {
		hi = len(text)
	}
	hay := strings.ToLower(text[lo:hi])
	for _, k := range keywords {
		if k == "" {
			continue
		}
		if strings.Contains(hay, strings.ToLower(k)) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 1.4: Run tests; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run TestHasContextWithin`
Expected: all six PASS.

- [ ] **Step 1.5: Commit**

```bash
git add internal/plugin/pii/contextual.go internal/plugin/pii/contextual_test.go
git commit -m "feat(pii): add hasContextWithin helper for ±N-char keyword anchoring"
```

### Step 1.6: Span type + greedy merge

- [ ] **Create `internal/plugin/pii/spans_test.go`:**

```go
// Tests for span overlap arbitration. Regex spans are collected first;
// NER spans are added second. mergeSpansGreedy preserves the existing
// (regex-first) entries and drops any later entry whose [start,end)
// range intersects an already-accepted entry. Ties at equal start are
// broken by preferring the longer existing span. Result is sorted by
// start ascending.

package pii

import (
	"reflect"
	"testing"
)

func TestMergeSpansGreedy_NoOverlap(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 5}},
		[]span{{Name: "PERSON", Start: 10, End: 15}},
	)
	want := []span{
		{Name: "Email", Start: 0, End: 5},
		{Name: "PERSON", Start: 10, End: 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestMergeSpansGreedy_LaterOverlapDropped(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 16}},
		[]span{{Name: "PERSON", Start: 5, End: 10}},
	)
	want := []span{
		{Name: "Email", Start: 0, End: 16},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("later overlapping span should be dropped; got %+v want %+v", got, want)
	}
}

func TestMergeSpansGreedy_SortedByStart(t *testing.T) {
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 20, End: 30}},
		[]span{{Name: "PERSON", Start: 0, End: 5}},
	)
	if got[0].Start != 0 || got[1].Start != 20 {
		t.Errorf("expected sorted-by-start ascending, got %+v", got)
	}
}

func TestMergeSpansGreedy_AbuttingNotOverlap(t *testing.T) {
	// [0,5) and [5,10) share no character; they must both survive.
	got := mergeSpansGreedy(
		[]span{{Name: "Email", Start: 0, End: 5}},
		[]span{{Name: "PERSON", Start: 5, End: 10}},
	)
	if len(got) != 2 {
		t.Errorf("abutting spans must both be kept; got %+v", got)
	}
}
```

- [ ] **Step 1.7: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run TestMergeSpansGreedy`
Expected: `undefined: span` / `undefined: mergeSpansGreedy`

- [ ] **Step 1.8: Implement spans.go**

Create `internal/plugin/pii/spans.go`:

```go
// span describes a single recognizer match against the ORIGINAL pre-
// redaction text. Start/End are byte offsets into that text (half-open
// [Start,End)). Name is the recognizer Name (e.g., "Email", "PERSON",
// "IMEI") used by ApplyMode to pick replacement shape.
//
// Spans are produced by two collectors:
//   - Regex collector: iterates Recognizers, runs Pattern.FindAllStringIndex,
//     applies Validate + hasContextWithin filters.
//   - NER collector (optional): runs the prose engine when h.NER != nil
//     and emits PERSON/LOCATION spans.
//
// mergeSpansGreedy arbitrates between the two: regex spans are passed
// in as `first` and always win; NER spans in `second` are dropped if
// they intersect any accepted span. This mirrors loop_24's
// _merge_results greedy non-overlap policy.

package pii

import "sort"

type span struct {
	Name  string
	Value string // verbatim slice from the original text — what ApplyMode receives
	Start int
	End   int
}

func (s span) overlaps(other span) bool {
	return s.Start < other.End && other.Start < s.End
}

// mergeSpansGreedy returns the union of first and second with second's
// entries dropped wherever they intersect an accepted entry. Result is
// sorted by Start ascending. Within first, callers must already have
// pre-arbitrated overlaps (regex recognizers are run in registration
// order; an earlier recognizer's span wins).
func mergeSpansGreedy(first, second []span) []span {
	accepted := make([]span, 0, len(first)+len(second))
	accepted = append(accepted, first...)

next:
	for _, cand := range second {
		for _, a := range accepted {
			if cand.overlaps(a) {
				continue next
			}
		}
		accepted = append(accepted, cand)
	}
	sort.SliceStable(accepted, func(i, j int) bool {
		return accepted[i].Start < accepted[j].Start
	})
	return accepted
}
```

- [ ] **Step 1.9: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run TestMergeSpansGreedy`
Expected: all four PASS.

- [ ] **Step 1.10: Commit**

```bash
git add internal/plugin/pii/spans.go internal/plugin/pii/spans_test.go
git commit -m "feat(pii): add span type + greedy overlap merge helper"
```

### Step 1.11: Extend `Recognizer` struct with `ContextKeywords`

- [ ] **Modify `internal/plugin/pii/recognizers.go` — extend the struct (no behavior change yet):**

Find the existing `Recognizer` type (lines 41–45) and add the field. The full block becomes:

```go
type Recognizer struct {
	Name     string
	Pattern  *regexp.Regexp
	Validate func(string) bool
	// ContextKeywords, when non-empty, gates a match: the redact pipeline
	// only accepts a regex hit if at least one keyword (case-insensitive)
	// appears within ±defaultContextWindow bytes of the match. Used to
	// disambiguate ambiguous patterns like IMEI vs IMSI (both 15-digit).
	// nil/empty = no context required (existing recognizers stay nil).
	ContextKeywords []string
}
```

- [ ] **Step 1.12: Build to confirm nothing else broke**

Run: `go build ./...`
Expected: success. Existing recognizers leave `ContextKeywords` zero-valued (nil), no behavior change.

- [ ] **Step 1.13: Refactor `redact` to position-based collection**

The existing inner `redact` closure in `internal/plugin/pii/pii.go` (lines 271–295) is sequential per-recognizer with `ReplaceAllStringFunc`. Replace it with a two-phase approach: (1) collect all accepted spans against the **original** string, (2) rebuild the string with replacements applied left-to-right.

Find the existing `redact := func(s string) string { ... }` block and replace its body. The new full closure:

```go
		// Per-recognizer span collection against the ORIGINAL string.
		// Phase 1: gather. Phase 2: rebuild. Sequence:
		//   1. For each active Recognizer, FindAllStringIndex on input.
		//   2. Apply Validate (if set) — drops false-positive shapes.
		//   3. Apply ContextKeywords window check — drops uncontextualized
		//      ambiguous matches (IMEI without "imei" nearby).
		//   4. Drop a candidate if it overlaps any already-accepted span
		//      (preserves "first recognizer wins" semantics).
		//   5. Accept candidate → record span + bump counter + Summary.Add.
		// NER spans (when enabled) are merged after regex via
		// mergeSpansGreedy so regex always wins overlap arbitration.
		//
		// Replacement happens in a single pass after collection so that
		// recognizers downstream of a match still see ORIGINAL bytes,
		// not the redacted token (fixes: IMEI substring shows up inside
		// a coordinate match, etc.).

		redact := func(s string) string {
			if s == "" {
				return s
			}
			regexSpans := h.collectRegexSpans(s, recs, counters, nextN, summary)
			var nerSpans []span
			if h.NER != nil {
				nerSpans = h.NER.Detect(s)
				// Filter NER spans against the enabled-entities + Summary
				// bookkeeping done by collectNERSpans.
				nerSpans = h.acceptNERSpans(s, nerSpans, regexSpans, counters, nextN, summary)
			}
			all := mergeSpansGreedy(regexSpans, nerSpans)
			if len(all) == 0 {
				return s
			}
			var b strings.Builder
			b.Grow(len(s))
			cursor := 0
			for _, sp := range all {
				if sp.Start < cursor {
					continue // defensive: should be impossible after merge
				}
				b.WriteString(s[cursor:sp.Start])
				key := sp.Name + "|" + canonicalForm(sp.Value)
				n := counters[key]
				b.WriteString(ApplyMode(h.actionFor(sp.Name), sp.Name, sp.Value, n, h.HashKey, h.EncryptKey))
				cursor = sp.End
			}
			b.WriteString(s[cursor:])
			return b.String()
		}
```

- [ ] **Step 1.14: Add `collectRegexSpans` method on `*PIIRedactionHook` in `pii.go`**

After the existing `actionFor` / `encryptActive` helpers (around line 195), add:

```go
// collectRegexSpans iterates recs over s and returns the accepted
// (Validate-pass, context-pass, non-overlapping) spans against the
// ORIGINAL string s. counters / nextN / summary are mutated as
// matches are accepted, preserving the per-canonical-value referential
// identity invariant from the prior implementation.
func (h *PIIRedactionHook) collectRegexSpans(
	s string,
	recs []Recognizer,
	counters map[string]int,
	nextN map[string]int,
	summary *Summary,
) []span {
	out := make([]span, 0, 4)
	for _, r := range recs {
		idxs := r.Pattern.FindAllStringIndex(s, -1)
		for _, idx := range idxs {
			start, end := idx[0], idx[1]
			match := s[start:end]
			if r.Validate != nil && !r.Validate(match) {
				continue
			}
			if len(r.ContextKeywords) > 0 &&
				!hasContextWithin(s, start, end, r.ContextKeywords, defaultContextWindow) {
				continue
			}
			cand := span{Name: r.Name, Value: match, Start: start, End: end}
			conflict := false
			for _, a := range out {
				if cand.overlaps(a) {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}
			key := r.Name + "|" + canonicalForm(match)
			if _, seen := counters[key]; !seen {
				nextN[r.Name]++
				counters[key] = nextN[r.Name]
			}
			summary.Add(r.Name)
			out = append(out, cand)
		}
	}
	return out
}
```

Add a stub for `acceptNERSpans` that returns the input unchanged (Task 10 wires the real body). This keeps Task 1 self-contained — the `h.NER != nil` branch is unreachable until Task 9 adds the `NER` field, but having the method exist now keeps the `redact` closure compilable today:

```go
// acceptNERSpans is the NER-side of the regex+NER merge pipeline. It
// applies the EnabledEntities filter to NER outputs and bumps the same
// counter/summary bookkeeping that collectRegexSpans does. Task 1 ships
// the stub; Task 10 fills in the body once NER is wired.
func (h *PIIRedactionHook) acceptNERSpans(
	s string,
	candidates []span,
	regexSpans []span,
	counters map[string]int,
	nextN map[string]int,
	summary *Summary,
) []span {
	// Task 10 will populate. For now (NER==nil precondition) callers
	// never reach this path.
	return candidates
}
```

- [ ] **Step 1.15: Add `NER` field placeholder to `PIIRedactionHook`**

In the struct literal (around line 84), append:

```go
	// NER, when non-nil, augments regex recognizers with prose-based
	// PERSON/LOCATION detection. Constructed by main.go when
	// PII_NER_ENABLED=true. nil = NER disabled (no prose model load).
	// See ner.go (Task 9).
	NER *nerEngine
```

This forward-references a type that Task 9 creates. To keep Task 1's commit buildable, also create a minimal placeholder in `internal/plugin/pii/ner.go`:

```go
// Placeholder for the NER engine wired in Task 9. Defined here so the
// PIIRedactionHook.NER field type exists before its real implementation
// lands. Task 9 replaces this file entirely.

package pii

type nerEngine struct{}

func (n *nerEngine) Detect(text string) []span { return nil }
```

- [ ] **Step 1.16: Run the full PII test suite**

Run: `go test ./internal/plugin/pii/...`
Expected: all existing tests still pass. (The refactor preserves the semantics of regex-only redaction; only the implementation strategy changed.)

If anything fails, the most likely culprits:
- The `[ENTITY_N]` counter suffix on a recognizer that matched twice should still produce `_1` and `_2` — verify counter bookkeeping is identical.
- `WalkStrings` callers in the `Before` body still pass the new `redact` closure (no signature change).

- [ ] **Step 1.17: Commit**

```bash
git add internal/plugin/pii/recognizers.go internal/plugin/pii/pii.go internal/plugin/pii/ner.go
git commit -m "refactor(pii): position-based span collection in redact()

Replaces sequential ReplaceAllStringFunc per recognizer with a two-phase
collect+rewrite that operates on the ORIGINAL string. Unlocks (a)
context-keyword anchoring for ambiguous patterns (IMEI vs IMSI) and (b)
NER overlap arbitration. Adds Recognizer.ContextKeywords, nerEngine
placeholder, and PIIRedactionHook.NER field. No behavior change for
existing tests."
```

---

## Task 2: SIP_URI recognizer (no context required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go` — add to allowlists.

### Step 2.1: Write failing test

- [ ] **Append to `internal/plugin/pii/recognizers_test.go`:**

```go
// SIP_URI — RFC 3261 SIP/SIPS URI shape (sip:user@host[:port]).
// Context-free: pattern is distinctive enough on its own.
func TestSIPURIRecognizer(t *testing.T) {
	r := findRecognizer(t, "SIP_URI")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"sip:alice@atlanta.example.com", true},
		{"sips:bob@biloxi.example.com:5061", true},
		{"contact me at sip:carol@chicago.example.com please", true},
		{"https://example.com/sip:notuser", false},
		{"sip:", false},
		{"plain email user@host.com", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("SIP_URI %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
}
```

- [ ] **Step 2.2: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run TestSIPURIRecognizer`
Expected: `recognizer "SIP_URI" not present`.

- [ ] **Step 2.3: Implement**

In `internal/plugin/pii/recognizers.go`, in the `var (...)` regex block (after `usPhoneRe`), add:

```go
	sipURIRe = regexp.MustCompile(`sips?:[a-zA-Z0-9_.+\-]+@[a-zA-Z0-9.\-]+(?::\d+)?`)
```

And append to the `Recognizers` slice (before the closing brace):

```go
	{Name: "SIP_URI", Pattern: sipURIRe, Validate: nil},
```

- [ ] **Step 2.4: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run TestSIPURIRecognizer`
Expected: all six PASS.

- [ ] **Step 2.5: Add SIP_URI to config allowlists**

In `internal/config/config.go`:

Find `validatePIIEntities` (around line 674). The `allowed` map currently has six entries. Add `"SIP_URI": {},` (placement: alphabetical or grouped — match existing style).

Find `parsePIIEntityActions` (around line 703). The `allowedEntities` map currently has six entries. Add `"SIP_URI": {},`.

Also update the two error-message strings that enumerate the allowed list (look for `(allowed: Email, IPv4, ...)` literals) — append `, SIP_URI`. Both `validatePIIEntities` and `parsePIIEntityActions` have one such message each.

- [ ] **Step 2.6: Build + run all tests**

Run: `go build ./... && go test ./...`
Expected: clean build, all tests pass.

- [ ] **Step 2.7: Commit**

```bash
git add internal/plugin/pii/recognizers.go internal/plugin/pii/recognizers_test.go internal/config/config.go
git commit -m "feat(pii): add SIP_URI recognizer (sip:/sips: URIs)"
```

---

## Task 3: IMEI recognizer (context-required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 3.1: Write failing test

- [ ] **Append to `internal/plugin/pii/recognizers_test.go`:**

```go
// IMEI — 15-digit subscriber-equipment identifier. Shares its raw
// regex shape with IMSI; context keywords disambiguate. Without an
// "imei" keyword nearby, the regex matches but the recognizer
// integration must reject it via ContextKeywords + hasContextWithin.
//
// This test exercises BOTH layers:
//   - r.Pattern matches a bare 15-digit run (shape-level).
//   - r.ContextKeywords is populated and contains "imei".
// The full context-anchored rejection is exercised end-to-end in
// pii_test.go via the redact pipeline.
func TestIMEIRecognizer(t *testing.T) {
	r := findRecognizer(t, "IMEI")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"490154203237518", true},
		{"49015420323751", false},  // 14 digits
		{"4901542032375180", false}, // 16 digits
		{"abc490154203237518xyz", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("IMEI shape %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
	// Context-keyword shape assertion.
	wantKw := []string{"imei", "international mobile equipment identity"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("IMEI ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
	for i, kw := range wantKw {
		if r.ContextKeywords[i] != kw {
			t.Errorf("IMEI ContextKeywords[%d]: got %q, want %q", i, r.ContextKeywords[i], kw)
		}
	}
}

// End-to-end: a 15-digit number with NO context keyword must be left
// alone by the redact pipeline. With "IMEI:" prefix it must be redacted.
func TestIMEI_ContextAnchored_Integration(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"IMEI"})
	// No context → unchanged.
	got := redactString(t, hook, "bare run 490154203237518 here")
	if !strings.Contains(got, "490154203237518") {
		t.Errorf("expected bare 15-digit run NOT to be redacted without imei context; got %q", got)
	}
	// With context → redacted.
	got = redactString(t, hook, "IMEI: 490154203237518")
	if strings.Contains(got, "490154203237518") {
		t.Errorf("expected redaction with 'IMEI:' prefix; got %q", got)
	}
}
```

This test introduces two helpers (`newTestHook`, `redactString`) — implement them in a new file `internal/plugin/pii/test_helpers_test.go` if they don't already exist by inspection. **Check first:** `grep -n "func newTestHook\|func redactString" internal/plugin/pii/*_test.go` — adopt the existing names/signatures if a near-equivalent exists; otherwise:

```go
// test_helpers_test.go
package pii

import (
	"context"
	"testing"

	"otto-gateway/internal/canonical"
)

func newTestHook(t *testing.T, mode string, entities []string) *PIIRedactionHook {
	t.Helper()
	return &PIIRedactionHook{
		Recognizers:     Recognizers,
		Enabled:         true,
		Mode:            mode,
		EnabledEntities: entities,
	}
}

func redactString(t *testing.T, h *PIIRedactionHook, text string) string {
	t.Helper()
	ctx := WithSummary(context.Background(), NewSummary())
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{{
			Role: "user",
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: text,
			}},
		}},
	}
	if _, err := h.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	return req.Messages[0].Content[0].Text
}
```

- [ ] **Step 3.2: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run "TestIMEIRecognizer|TestIMEI_ContextAnchored"`
Expected: failures (recognizer absent).

- [ ] **Step 3.3: Implement IMEI in `recognizers.go`**

In the `var (...)` regex block, add:

```go
	imeiRe = regexp.MustCompile(`\b\d{15}\b`)
```

Append to `Recognizers`:

```go
	{
		Name:            "IMEI",
		Pattern:         imeiRe,
		Validate:        nil,
		ContextKeywords: []string{"imei", "international mobile equipment identity"},
	},
```

- [ ] **Step 3.4: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run "TestIMEIRecognizer|TestIMEI_ContextAnchored"`
Expected: all PASS.

- [ ] **Step 3.5: Add IMEI to config allowlists**

Same shape as Task 2.5 — add `"IMEI": {},` to both `validatePIIEntities` and `parsePIIEntityActions` allowlists, and append `, IMEI` to both error-message enumeration strings.

- [ ] **Step 3.6: Build + full test**

Run: `go build ./... && go test ./...`

- [ ] **Step 3.7: Commit**

```bash
git add internal/plugin/pii/recognizers.go internal/plugin/pii/recognizers_test.go internal/plugin/pii/test_helpers_test.go internal/config/config.go
git commit -m "feat(pii): add IMEI recognizer with imei context anchor"
```

---

## Task 4: IMSI recognizer (context-required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 4.1: Write failing test (mirror IMEI; context word is "imsi")

- [ ] **Append to `recognizers_test.go`:**

```go
// IMSI — same 15-digit shape as IMEI, disambiguated by "imsi" /
// "international mobile subscriber identity" context keyword.
func TestIMSIRecognizer(t *testing.T) {
	r := findRecognizer(t, "IMSI")
	if !r.Pattern.MatchString("310150123456789") {
		t.Error("IMSI shape: 15-digit run must match")
	}
	wantKw := []string{"imsi", "international mobile subscriber identity"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("IMSI ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
	for i, kw := range wantKw {
		if r.ContextKeywords[i] != kw {
			t.Errorf("IMSI ContextKeywords[%d]: got %q, want %q", i, r.ContextKeywords[i], kw)
		}
	}
}

// Disambiguation: "IMSI: 15digits" must redact as IMSI (NOT IMEI), and
// "IMEI: 15digits" must redact as IMEI (NOT IMSI). Both recognizers
// share a regex shape; the keyword disambiguates which redaction
// label is applied.
func TestIMSIvsIMEI_Disambiguation(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"IMEI", "IMSI"})

	imsiTxt := redactString(t, hook, "IMSI: 310150123456789")
	if !strings.Contains(imsiTxt, "[IMSI]") {
		t.Errorf("expected [IMSI] token, got %q", imsiTxt)
	}
	if strings.Contains(imsiTxt, "[IMEI]") {
		t.Errorf("IMSI context must not trigger IMEI label, got %q", imsiTxt)
	}

	imeiTxt := redactString(t, hook, "IMEI: 490154203237518")
	if !strings.Contains(imeiTxt, "[IMEI]") {
		t.Errorf("expected [IMEI] token, got %q", imeiTxt)
	}
	if strings.Contains(imeiTxt, "[IMSI]") {
		t.Errorf("IMEI context must not trigger IMSI label, got %q", imeiTxt)
	}
}
```

- [ ] **Step 4.2: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run "TestIMSIRecognizer|TestIMSIvsIMEI"`
Expected: failures.

- [ ] **Step 4.3: Implement IMSI**

In `recognizers.go`, the IMEI/IMSI regex shape is identical so reuse the compiled pattern. Append to `Recognizers` AFTER the IMEI entry (registration order matters for the "first wins" rule — but with context anchoring, both will only fire when their keyword is present, so order doesn't change correctness; still, put IMSI after IMEI for stable ordering):

```go
	{
		Name:            "IMSI",
		Pattern:         imeiRe, // same 15-digit shape; disambiguated by context
		Validate:        nil,
		ContextKeywords: []string{"imsi", "international mobile subscriber identity"},
	},
```

**Important corner case:** when text contains BOTH keywords (rare, e.g. "IMSI and IMEI are both 15-digit identifiers like 310150123456789"), the first recognizer in registration order wins per the overlap-arbitration rule in `collectRegexSpans`. Document this in the recognizer registration comment.

- [ ] **Step 4.4: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run "TestIMSIRecognizer|TestIMSIvsIMEI"`
Expected: PASS.

- [ ] **Step 4.5: Config allowlists**

Add `"IMSI": {}` in both places + extend both error-message enumerations.

- [ ] **Step 4.6: Build + full test**

Run: `go build ./... && go test ./...`

- [ ] **Step 4.7: Commit**

```bash
git add internal/plugin/pii/recognizers.go internal/plugin/pii/recognizers_test.go internal/config/config.go
git commit -m "feat(pii): add IMSI recognizer with imsi context anchor"
```

---

## Task 5: MSISDN recognizer (context-required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 5.1: Write failing test

- [ ] **Append to `recognizers_test.go`:**

```go
// MSISDN — E.164 international phone number, context-anchored to
// "msisdn"/"subscriber number"/"calling number"/"called number". Naked
// E.164 numbers without context fall through (avoids competing with
// USPhone in informal contexts).
func TestMSISDNRecognizer(t *testing.T) {
	r := findRecognizer(t, "MSISDN")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"+14155552671", true},
		{"+442071838750", true},
		{"+1", false}, // too short
		{"+0123456789", false}, // leading 0 disallowed
		{"14155552671", false}, // missing '+'
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("MSISDN %q: regex matched=%v, want %v", c.in, got, c.wantMatched)
			}
		})
	}
	wantKw := []string{"msisdn", "subscriber number", "calling number", "called number"}
	if len(r.ContextKeywords) != len(wantKw) {
		t.Fatalf("MSISDN ContextKeywords len: got %d, want %d", len(r.ContextKeywords), len(wantKw))
	}
}

func TestMSISDN_ContextRequired_Integration(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"MSISDN"})
	plain := redactString(t, hook, "called +14155552671 just now")
	if !strings.Contains(plain, "+14155552671") {
		t.Errorf("plain E.164 without msisdn context must NOT be redacted; got %q", plain)
	}
	anchored := redactString(t, hook, "MSISDN: +14155552671")
	if strings.Contains(anchored, "+14155552671") {
		t.Errorf("anchored MSISDN must be redacted; got %q", anchored)
	}
}
```

- [ ] **Step 5.2: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run "TestMSISDN"`

- [ ] **Step 5.3: Implement**

In `recognizers.go` regex block:

```go
	msisdnRe = regexp.MustCompile(`\+[1-9]\d{7,14}`)
```

In `Recognizers`:

```go
	{
		Name:            "MSISDN",
		Pattern:         msisdnRe,
		Validate:        nil,
		ContextKeywords: []string{"msisdn", "subscriber number", "calling number", "called number"},
	},
```

- [ ] **Step 5.4: Run; confirm pass**

- [ ] **Step 5.5: Config allowlists** (add `"MSISDN"`, extend error messages)

- [ ] **Step 5.6: Build + full test**

- [ ] **Step 5.7: Commit**

```bash
git commit -m "feat(pii): add MSISDN recognizer with subscriber-number context anchor"
```

---

## Task 6: MAC_ADDRESS recognizer (no context required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 6.1: Write failing test

- [ ] **Append to `recognizers_test.go`:**

```go
// MAC_ADDRESS — six pairs of hex with either ':' or '-' separators.
func TestMACAddressRecognizer(t *testing.T) {
	r := findRecognizer(t, "MAC_ADDRESS")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"00:1B:44:11:3A:B7", true},
		{"00-1B-44-11-3A-B7", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"00:1B:44:11:3A", false}, // 5 pairs
		{"GG:1B:44:11:3A:B7", false}, // invalid hex
		{"00:1B:44:11:3A:B7:00", false}, // 7 pairs
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("MAC %q: matched=%v want=%v", c.in, got, c.wantMatched)
			}
		})
	}
}
```

- [ ] **Step 6.2: Run; confirm failure**

- [ ] **Step 6.3: Implement**

Regex block:

```go
	macAddrRe = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:\-]){5}[0-9A-Fa-f]{2}\b`)
```

Recognizers:

```go
	{Name: "MAC_ADDRESS", Pattern: macAddrRe, Validate: nil},
```

- [ ] **Step 6.4: Run; confirm pass**

- [ ] **Step 6.5: Config allowlists** (add `"MAC_ADDRESS"`)

- [ ] **Step 6.6: Build + full test**

- [ ] **Step 6.7: Commit**

```bash
git commit -m "feat(pii): add MAC_ADDRESS recognizer"
```

---

## Task 7: COORDINATES recognizer (no context required)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 7.1: Write failing test

- [ ] **Append to `recognizers_test.go`:**

```go
// COORDINATES — decimal-degrees lat/long with N/S and E/W suffixes.
func TestCoordinatesRecognizer(t *testing.T) {
	r := findRecognizer(t, "COORDINATES")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"37.7749 N, 122.4194 W", true},
		{"37.7749°N, 122.4194°W", true},
		{"37.7749 S 122.4194 E", true},
		{"37 N 122 W", false}, // no decimal portion
		{"37.7749, -122.4194", false}, // no hemisphere markers
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("COORDINATES %q: matched=%v want=%v", c.in, got, c.wantMatched)
			}
		})
	}
}
```

- [ ] **Step 7.2: Run; confirm failure**

- [ ] **Step 7.3: Implement**

```go
	coordinatesRe = regexp.MustCompile(`\b\d{1,3}\.\d+\s*°?\s*[NS][,\s]+\d{1,3}\.\d+\s*°?\s*[EW]\b`)
```

```go
	{Name: "COORDINATES", Pattern: coordinatesRe, Validate: nil},
```

- [ ] **Step 7.4: Run; confirm pass**

- [ ] **Step 7.5: Config allowlists** (add `"COORDINATES"`)

- [ ] **Step 7.6: Build + full test**

- [ ] **Step 7.7: Commit**

```bash
git commit -m "feat(pii): add COORDINATES recognizer"
```

---

## Task 8: SITE recognizer (context-required, two alternation arms)

**Files:**
- Modify: `internal/plugin/pii/recognizers.go`
- Modify: `internal/plugin/pii/recognizers_test.go`
- Modify: `internal/config/config.go`

### Step 8.1: Write failing test

- [ ] **Append to `recognizers_test.go`:**

```go
// SITE — telecom site / network-element identifiers. Two alternation
// arms covered by a single regex via |, both gated by context keywords
// ("site", "cell", "base station", etc.) so generic alphanumeric tags
// like "JOB-XYZ123" don't false-positive.
func TestSITERecognizer(t *testing.T) {
	r := findRecognizer(t, "SITE")
	cases := []struct {
		in          string
		wantMatched bool
	}{
		{"site-A12_NYC01", true},
		{"site A12 NYC01", true},
		{"ENB-12345", true},
		{"BTS_AB12", true},
		{"random-id-not-a-site", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, _ := regexAndValidate(r, c.in)
			if got != c.wantMatched {
				t.Errorf("SITE %q: matched=%v want=%v", c.in, got, c.wantMatched)
			}
		})
	}
	if len(r.ContextKeywords) == 0 {
		t.Fatal("SITE must have ContextKeywords")
	}
}

func TestSITE_ContextRequired_Integration(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"SITE"})
	plain := redactString(t, hook, "the system uses tag site-A12_NYC01 internally")
	// Context keyword "site" is part of the regex match itself ("site-A12...")
	// AND appears in the context window. So this case DOES get redacted —
	// that's the intended behavior. Use a case without any keyword for the
	// negative side.
	if !strings.Contains(plain, "[SITE]") {
		t.Errorf("expected redaction when 'site-A12...' itself contains 'site'; got %q", plain)
	}

	bare := redactString(t, hook, "ENB-12345 was provisioned")
	// ENB-12345 matches the second alternation arm. Context keywords
	// include "enb", so the regex match itself satisfies hasContextWithin.
	if !strings.Contains(bare, "[SITE]") {
		t.Errorf("expected redaction when match itself contains an ENB keyword; got %q", bare)
	}
}
```

- [ ] **Step 8.2: Run; confirm failure**

- [ ] **Step 8.3: Implement**

Regex block:

```go
	siteRe = regexp.MustCompile(
		`\bsite[-_\s]?[A-Z0-9]{1,2}[-_]?[A-Z0-9]{2,10}\b` +
			`|\b(?:ENB|BTS|NB|CELL|NODE|RAN|BSC|RNC|MSC|HLR|MME|SGW|PGW)[-_]?[A-Z0-9]{2,12}\b`)
```

Recognizers:

```go
	{
		Name:    "SITE",
		Pattern: siteRe,
		Validate: nil,
		ContextKeywords: []string{
			"site", "cell", "base station", "node", "tower",
			"location code", "enb", "bts", "ran", "network element", "ne id",
		},
	},
```

Note: because the regex itself contains the keyword strings (e.g. matches always start with "site" or "ENB"), the `hasContextWithin` check inside the redact pipeline will always succeed for an actual match. The keyword list still has value: it documents intent and provides a wider context match if the pattern is later loosened.

- [ ] **Step 8.4: Run; confirm pass**

- [ ] **Step 8.5: Config allowlists** (add `"SITE"`)

- [ ] **Step 8.6: Build + full test**

- [ ] **Step 8.7: Commit**

```bash
git commit -m "feat(pii): add SITE recognizer (telecom network elements)"
```

---

## Task 9: Add prose dependency + `nerEngine` lazy-init

**Files:**
- Modify: `go.mod` / `go.sum`
- Replace: `internal/plugin/pii/ner.go` (was a placeholder from Task 1.15)
- Create: `internal/plugin/pii/ner_test.go`

### Step 9.1: Measure baseline binary size

- [ ] **Build and record baseline:**

```bash
go build -trimpath -ldflags="-s -w" -o /tmp/otto-gateway-pre /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/cmd/otto-gateway
ls -lh /tmp/otto-gateway-pre
```

Record the pre-prose size in a scratchpad — you'll quote this in Task 14's report.

### Step 9.2: Add prose dependency

- [ ] **Add the module:**

```bash
go get github.com/jdkato/prose/v2@latest
go mod tidy
```

- [ ] **Step 9.3: Confirm install is clean and binary still builds:**

```bash
go build ./...
ls -lh /tmp/otto-gateway-pre  # unchanged — we haven't imported it yet
```

If `go mod tidy` introduces an indirect-dependency surprise, inspect `go.sum` diff and decide whether to pin a specific tag. Prose's last published tag (as of writing) is `v2.0.0`.

- [ ] **Step 9.4: Write the failing NER test**

Replace `internal/plugin/pii/ner_test.go` (Task 1 left only the placeholder file). Note: `nerEngine` returns spans against the source text; offsets must be precise for the redact rewriter to work.

```go
// PII-NER tests for the prose-backed PERSON/LOCATION recognizer.
// Verifies:
//   - Lazy init: NewNEREngine() does not load the model until first
//     Detect call (cannot directly test the underlying allocation,
//     but the sync.Once guard is exercised by repeated calls being
//     no-op-faster).
//   - Detect emits spans with byte-accurate Start/End offsets into
//     the original text.
//   - Detect labels common Western names as PERSON, common place
//     names as LOCATION.
//   - Empty input is a no-op (no panic, empty slice).

package pii

import (
	"strings"
	"testing"
)

func TestNEREngine_DisabledByDefault(t *testing.T) {
	// PIIRedactionHook.NER is nil by default; tests in other files
	// already cover the regex-only path with this invariant. This test
	// pins the documented default.
	h := &PIIRedactionHook{}
	if h.NER != nil {
		t.Error("PIIRedactionHook.NER must default to nil")
	}
}

func TestNEREngine_DetectPerson(t *testing.T) {
	eng := NewNEREngine()
	text := "Hello, my name is Alice Johnson and I live in Boston."
	spans := eng.Detect(text)

	if len(spans) == 0 {
		t.Fatal("expected at least one span; got 0")
	}

	foundPerson := false
	for _, s := range spans {
		if s.Name != "PERSON" {
			continue
		}
		got := text[s.Start:s.End]
		if !strings.Contains(got, "Alice") {
			continue
		}
		foundPerson = true
		// Sanity: the span text must equal what s.Value records.
		if s.Value != got {
			t.Errorf("span.Value=%q does not match text[Start:End]=%q", s.Value, got)
		}
	}
	if !foundPerson {
		t.Errorf("expected to find a PERSON span containing 'Alice' in %+v", spans)
	}
}

func TestNEREngine_DetectLocation(t *testing.T) {
	eng := NewNEREngine()
	text := "We deployed the gateway in Boston last week."
	spans := eng.Detect(text)

	foundLoc := false
	for _, s := range spans {
		if s.Name == "LOCATION" && strings.Contains(text[s.Start:s.End], "Boston") {
			foundLoc = true
			break
		}
	}
	if !foundLoc {
		t.Errorf("expected to find a LOCATION span 'Boston' in %+v", spans)
	}
}

func TestNEREngine_EmptyInput(t *testing.T) {
	eng := NewNEREngine()
	got := eng.Detect("")
	if len(got) != 0 {
		t.Errorf("Detect(\"\"): expected 0 spans, got %d", len(got))
	}
}

func TestNEREngine_RepeatableInit(t *testing.T) {
	eng := NewNEREngine()
	_ = eng.Detect("first call primes the lazy init")
	// Second call must not panic and must produce consistent results.
	a := eng.Detect("Bob lives in Seattle.")
	b := eng.Detect("Bob lives in Seattle.")
	if len(a) != len(b) {
		t.Errorf("repeated Detect calls yielded different counts: %d vs %d", len(a), len(b))
	}
}
```

- [ ] **Step 9.5: Run; confirm failure**

Run: `go test -v ./internal/plugin/pii/ -run TestNEREngine`
Expected: `undefined: NewNEREngine` (or the placeholder Detect returns nil for everything).

- [ ] **Step 9.6: Implement `ner.go`**

Replace `internal/plugin/pii/ner.go`:

```go
// PII-NER engine — wraps a jdkato/prose/v2 document factory under
// sync.Once so the prose tagger/tokenizer state is loaded exactly once
// per process. When PII_NER_ENABLED is false at boot, NewNEREngine is
// never called and no prose state is allocated.
//
// Why prose:
//   - Pure Go: no CGo, no shared libs, single static binary preserved.
//   - Bundled model: the averaged-perceptron NER weights ship inside
//     the Go module — no model download, no first-run bootstrap, no
//     network at install time. Curl|sh install stays one command.
//   - English-only: known limitation, documented in the design doc
//     accuracy-ceiling section.
//
// Accuracy ceiling (v1):
//   - Decent on common Western names and major place names.
//   - Weaker on Asian / multilingual names, unusual locations.
//   - Roughly: spaCy small ≤ prose < spaCy large < BERT.
//   - v2 will add an opt-in transformer-backed engine (first-run ONNX
//     model download); explicitly out of scope here.
//
// Byte-offset reconstruction:
//   prose returns Entity.Text but not byte offsets. We reconstruct
//   them by scanning the original text for each entity's text in
//   order, advancing a cursor so duplicates resolve to distinct
//   matches. This is robust enough for the round-trip property
//   (each detected name maps to ONE span; duplicates handled by
//   sequential scan). Pathological cases (overlapping substrings of
//   different entities, weird tokenizer normalizations) fall back to
//   skipping the entity — preferred over emitting a wrong span.

package pii

import (
	"strings"
	"sync"

	"github.com/jdkato/prose/v2"
)

// nerEngine wraps prose under a sync.Once. The Document is NOT cached
// across calls — prose Documents are constructed per-text — but the
// sync.Once gates whatever one-time global state prose may lazy-init.
type nerEngine struct {
	once sync.Once
}

// NewNEREngine constructs an engine. The prose tagger/tokenizer global
// state is NOT touched until the first Detect call.
func NewNEREngine() *nerEngine {
	return &nerEngine{}
}

// Detect returns PERSON and LOCATION spans in text, with byte offsets
// reconstructed by sequential scan. Returns nil for empty text.
func (n *nerEngine) Detect(text string) []span {
	if text == "" {
		return nil
	}
	// sync.Once gives us one-time warmup hook; prose itself has no
	// explicit init function, so the body is empty for now. Kept so
	// any future warmup (e.g., dummy doc parse to pre-populate caches)
	// has a clean home.
	n.once.Do(func() {})

	doc, err := prose.NewDocument(
		text,
		prose.WithExtraction(true), // enables NER
		prose.WithSegmentation(false),
		prose.WithTokenization(true),
	)
	if err != nil {
		return nil
	}

	entities := doc.Entities()
	if len(entities) == 0 {
		return nil
	}

	out := make([]span, 0, len(entities))
	cursor := 0
	for _, e := range entities {
		var name string
		switch e.Label {
		case "PERSON":
			name = "PERSON"
		case "GPE", "LOC", "LOCATION":
			name = "LOCATION"
		default:
			continue
		}
		// Find e.Text in text starting at cursor. If not found from
		// cursor, fall back to a full scan (some tokenizers may emit
		// entities out of order). If still not found, skip — better
		// to drop one entity than emit a wrong span.
		idx := strings.Index(text[cursor:], e.Text)
		if idx < 0 {
			idx = strings.Index(text, e.Text)
			if idx < 0 {
				continue
			}
			out = append(out, span{
				Name:  name,
				Value: e.Text,
				Start: idx,
				End:   idx + len(e.Text),
			})
			continue
		}
		start := cursor + idx
		out = append(out, span{
			Name:  name,
			Value: e.Text,
			Start: start,
			End:   start + len(e.Text),
		})
		cursor = start + len(e.Text)
	}
	return out
}
```

- [ ] **Step 9.7: Run NER tests**

Run: `go test -v ./internal/plugin/pii/ -run TestNEREngine`
Expected: PASS.

If prose's NER labels disagree with the test fixtures, adjust the test text to use canonical examples — `prose` ships well-known fixture coverage for "John Smith", "New York", etc. Verify against the prose README before tightening.

- [ ] **Step 9.8: Measure post-prose binary size**

```bash
go build -trimpath -ldflags="-s -w" -o /tmp/otto-gateway-post ./cmd/otto-gateway
ls -lh /tmp/otto-gateway-pre /tmp/otto-gateway-post
```

If the delta is > 30 MB, flag it (record in Task 14's report and leave a note in the design doc). The prose averaged-perceptron model is bundled in the module — expected delta ~15-25 MB.

- [ ] **Step 9.9: Commit**

```bash
git add go.mod go.sum internal/plugin/pii/ner.go internal/plugin/pii/ner_test.go
git commit -m "feat(pii): add jdkato/prose v2 NER engine (PERSON + LOCATION)

Pure-Go NER under sync.Once gated init. Default off; PIIRedactionHook.NER
must be set by main.go when PII_NER_ENABLED=true. Adds bundled-model
prose dep — see plan for binary-size delta. Accuracy ceiling documented
in encrypt-design doc §11."
```

---

## Task 10: Wire NER into `redact` + EnabledEntities filter

**Files:**
- Modify: `internal/plugin/pii/pii.go` — fill in `acceptNERSpans` body.
- Modify: `internal/plugin/pii/ner_test.go` — add integration coverage through `redact`.

### Step 10.1: Write failing integration test

- [ ] **Append to `ner_test.go`:**

```go
// Integration: when NER is wired into PIIRedactionHook AND PERSON is
// in EnabledEntities, a name in the user text is replaced.
func TestNER_Integration_PersonReplace(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"PERSON", "LOCATION", "Email"})
	hook.NER = NewNEREngine()
	got := redactString(t, hook, "Alice Johnson emailed me from Boston.")
	if strings.Contains(got, "Alice Johnson") {
		t.Errorf("expected PERSON redaction; got %q", got)
	}
	if strings.Contains(got, "Boston") {
		t.Errorf("expected LOCATION redaction; got %q", got)
	}
}

// Off-by-default: NER is nil on the hook → names pass through even
// when PERSON is in EnabledEntities (the allowlist is shape-only;
// behavior depends on NER being wired).
func TestNER_OffByDefault(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"PERSON", "Email"})
	// hook.NER intentionally nil
	got := redactString(t, hook, "Alice Johnson emailed me.")
	if !strings.Contains(got, "Alice Johnson") {
		t.Errorf("NER off → name must pass through; got %q", got)
	}
}

// Overlap arbitration: regex Email wins over NER PERSON at the same
// span. Prose can occasionally label "weird.username@example.com" as
// a PERSON if the local part looks name-ish; verify regex wins.
func TestNER_RegexWinsOverlap(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"Email", "PERSON"})
	hook.NER = NewNEREngine()
	got := redactString(t, hook, "contact alice.johnson@example.com please")
	if !strings.Contains(got, "[EMAIL") {
		t.Errorf("expected EMAIL redaction (regex must win); got %q", got)
	}
	if strings.Contains(got, "[PERSON") {
		t.Errorf("PERSON must not also fire on the same email span; got %q", got)
	}
}

// EnabledEntities allowlist must filter NER outputs even when NER is
// wired. PERSON enabled but LOCATION NOT → only person redacts.
func TestNER_EnabledEntitiesFilter(t *testing.T) {
	hook := newTestHook(t, "replace", []string{"PERSON"})
	hook.NER = NewNEREngine()
	got := redactString(t, hook, "Alice Johnson visited Boston.")
	if strings.Contains(got, "Alice Johnson") {
		t.Errorf("PERSON enabled → name must be redacted; got %q", got)
	}
	if !strings.Contains(got, "Boston") {
		t.Errorf("LOCATION NOT enabled → must pass through; got %q", got)
	}
}

// Encrypt round-trip on a NER-detected PERSON: Before emits
// [PII:PERSON:base64url]; After's DecryptToken restores the plaintext
// regardless of recognizer origin (entity name is AAD; PERSON is a
// valid string just like Email).
func TestNER_EncryptRoundTrip(t *testing.T) {
	key, err := DeriveKey("test-key-for-ner-roundtrip")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	hook := &PIIRedactionHook{
		Recognizers:     Recognizers,
		Enabled:         true,
		Mode:            "encrypt",
		EncryptKey:      key,
		EnabledEntities: []string{"PERSON"},
		NER:             NewNEREngine(),
	}
	encrypted := redactString(t, hook, "Alice Johnson said hello.")
	if !strings.Contains(encrypted, "[PII:PERSON:") {
		t.Fatalf("expected [PII:PERSON:...] token; got %q", encrypted)
	}
	// Build a response with the encrypted text and run After.
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: "assistant",
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: encrypted,
			}},
		},
	}
	if err := hook.After(context.Background(), nil, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if !strings.Contains(resp.Message.Content[0].Text, "Alice Johnson") {
		t.Errorf("decrypt round-trip failed; got %q", resp.Message.Content[0].Text)
	}
}
```

The encrypt round-trip test will need extra imports — extend the test file's imports to include `context` and `otto-gateway/internal/canonical`.

- [ ] **Step 10.2: Run; confirm failure (some tests now exercise unimplemented `acceptNERSpans`)**

Run: `go test -v ./internal/plugin/pii/ -run TestNER`
Expected: failures (acceptNERSpans currently returns input unchanged → NER spans not actually integrated into the rewrite).

- [ ] **Step 10.3: Fill in `acceptNERSpans`**

In `internal/plugin/pii/pii.go`, replace the stub body:

```go
func (h *PIIRedactionHook) acceptNERSpans(
	s string,
	candidates []span,
	regexSpans []span,
	counters map[string]int,
	nextN map[string]int,
	summary *Summary,
) []span {
	if len(candidates) == 0 {
		return nil
	}
	allowSet := h.enabledEntitiesSet()
	out := make([]span, 0, len(candidates))
	for _, cand := range candidates {
		// EnabledEntities filter (empty allowSet = allow all).
		if len(allowSet) > 0 {
			if _, ok := allowSet[cand.Name]; !ok {
				continue
			}
		}
		// Regex wins overlap arbitration (greedy).
		conflict := false
		for _, r := range regexSpans {
			if cand.overlaps(r) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		// Also avoid intra-NER overlap (prose can emit nested entities
		// in rare cases).
		for _, prev := range out {
			if cand.overlaps(prev) {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		key := cand.Name + "|" + canonicalForm(cand.Value)
		if _, seen := counters[key]; !seen {
			nextN[cand.Name]++
			counters[key] = nextN[cand.Name]
		}
		summary.Add(cand.Name)
		out = append(out, cand)
	}
	return out
}

// enabledEntitiesSet returns h.EnabledEntities as a set, or nil if
// the allowlist is empty (caller treats nil as "allow all").
func (h *PIIRedactionHook) enabledEntitiesSet() map[string]struct{} {
	if len(h.EnabledEntities) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(h.EnabledEntities))
	for _, e := range h.EnabledEntities {
		out[e] = struct{}{}
	}
	return out
}
```

- [ ] **Step 10.4: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/`
Expected: full PII package test suite green.

- [ ] **Step 10.5: Commit**

```bash
git add internal/plugin/pii/pii.go internal/plugin/pii/ner_test.go
git commit -m "feat(pii): wire NER into redact() with EnabledEntities filter"
```

---

## Task 11: Config + main.go wiring for `PII_NER_ENABLED` and new entity names

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go` — add boot-validation cases.
- Modify: `cmd/otto-gateway/main.go`

### Step 11.1: Write failing config tests

- [ ] **Append to `internal/config/config_test.go` (or the file holding `TestLoad_PII_*`):**

```go
// PII_NER_ENABLED: boolean parse + default false.
func TestLoad_PIINEREnabled_DefaultFalse(t *testing.T) {
	withEnv(t, map[string]string{})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIINEREnabled {
		t.Error("PIINEREnabled default must be false")
	}
}

func TestLoad_PIINEREnabled_True(t *testing.T) {
	withEnv(t, map[string]string{"PII_NER_ENABLED": "true"})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.PIINEREnabled {
		t.Error("PIINEREnabled=true env must set field to true")
	}
}

// PERSON/LOCATION + new telecom names accepted by allowlist.
func TestLoad_PIIEnabledEntities_NewNames(t *testing.T) {
	withEnv(t, map[string]string{
		"PII_REDACTION_ENABLED":  "true",
		"PII_ENABLED_ENTITIES":   "PERSON,LOCATION,IMEI,IMSI,MSISDN,SIP_URI,MAC_ADDRESS,COORDINATES,SITE",
	})
	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// PII_ENTITY_ACTIONS accepts new entities.
func TestLoad_PIIEntityActions_NewEntities(t *testing.T) {
	withEnv(t, map[string]string{
		"PII_REDACTION_ENABLED":  "true",
		"PII_ENTITY_ACTIONS":     "PERSON:mask,LOCATION:drop,IMEI:encrypt",
		"PII_ENCRYPT_KEY":        "k",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PIIEntityActions["PERSON"] != "mask" ||
		cfg.PIIEntityActions["LOCATION"] != "drop" ||
		cfg.PIIEntityActions["IMEI"] != "encrypt" {
		t.Errorf("PIIEntityActions parse mismatch: %+v", cfg.PIIEntityActions)
	}
}
```

The above assumes a `withEnv(t, map)` test helper exists (it's used by the existing `TestLoad_PII*` tests — check `config_test.go` for its actual name and adapt; if it's `t.Setenv` calls directly, switch the new tests to that form).

- [ ] **Step 11.2: Run; confirm failure**

Run: `go test -v ./internal/config/ -run TestLoad_PII`
Expected: failures (PIINEREnabled field absent; new entity names rejected by allowlist).

- [ ] **Step 11.3: Implement in `internal/config/config.go`**

(1) Add `PIINEREnabled bool` to the `Config` struct (group it near `PIIEntityActions`).

(2) Extend `validatePIIEntities` (line 674): add to the `allowed` map:

```go
"PERSON":       {},
"LOCATION":     {},
"SIP_URI":      {},  // already added in Task 2
"IMEI":         {},  // already added in Task 3
"IMSI":         {},  // already added in Task 4
"MSISDN":       {},  // already added in Task 5
"MAC_ADDRESS":  {},  // already added in Task 6
"COORDINATES": {},  // already added in Task 7
"SITE":         {},  // already added in Task 8
```

(Most are already added by Tasks 2-8; this step layers in PERSON/LOCATION and ensures the error-message string lists them all.) Update both `allowed`-error-string enumerations to include the full list.

(3) Same edits in `parsePIIEntityActions` (line 703).

(4) Parse `PII_NER_ENABLED` near the existing `piiEnabled` parse (around line 317):

```go
piiNEREnabled, err := getEnvBool("PII_NER_ENABLED", false)
if err != nil {
	errs = append(errs, err)
}
```

(5) Wire into the `Config{}` literal at the bottom of `Load` (around line 467):

```go
PIINEREnabled: piiNEREnabled,
```

- [ ] **Step 11.4: Run; confirm config tests pass**

Run: `go test -v ./internal/config/`

- [ ] **Step 11.5: Wire into `cmd/otto-gateway/main.go`**

Find the `piiHook := &pii.PIIRedactionHook{...}` block (line 207). After the existing fields and after the `EncryptKey` derive block (line 222), add:

```go
	if cfg.PIINEREnabled {
		piiHook.NER = pii.NewNEREngine()
	}
```

- [ ] **Step 11.6: Build + run all tests**

Run: `go build ./... && go test ./...`
Expected: clean.

- [ ] **Step 11.7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/otto-gateway/main.go
git commit -m "feat(config): add PII_NER_ENABLED + PERSON/LOCATION allowlist entries

Wires the prose NER engine via main.go when PII_NER_ENABLED=true.
EnabledEntities/EntityActions allowlists accept PERSON and LOCATION."
```

---

## Task 12: End-to-end encrypt round-trip via the engine chain

**Files:**
- Modify: `internal/plugin/pii/pii_test.go` — add a chain-level test if a similar one exists, else mirror `encrypt_test.go`'s round-trip style.

### Step 12.1: Locate the existing encrypt round-trip test

- [ ] **Identify the closest analog:**

Run: `grep -n "TestPII.*EncryptRoundTrip\|encrypt.*round.*trip" internal/plugin/pii/*_test.go`

The existing encrypt round-trip lives in `encrypt_test.go` (per Step 9.4's preview) and `pii_test.go`. Find the most chain-shaped one (uses `Before` + `After` against a `*PIIRedactionHook` directly with `Mode: "encrypt"`).

### Step 12.2: Add NER-driven encrypt round-trip

- [ ] **Append to `internal/plugin/pii/pii_test.go`:**

```go
// End-to-end NER + encrypt round-trip:
//   1. Before encrypts a PERSON span detected by prose.
//   2. The token survives a simulated upstream response (we just copy
//      the encrypted text into resp.Message.Content[0].Text).
//   3. After decrypts and the plaintext PERSON name is restored.
//
// Mirrors the existing Email encrypt round-trip but exercises the NER
// code path end-to-end. PII_NER_ENABLED is simulated by setting
// hook.NER directly (production wiring is exercised in main_test.go).
func TestPIIRedactionHook_NEREncryptRoundTrip(t *testing.T) {
	key, err := DeriveKey("e2e-ner-test-key")
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	hook := &PIIRedactionHook{
		Recognizers:     Recognizers,
		Enabled:         true,
		Mode:            "encrypt",
		EncryptKey:      key,
		EnabledEntities: []string{"PERSON", "LOCATION", "Email"},
		NER:             NewNEREngine(),
	}

	original := "Alice Johnson visited Boston last week, contact: alice@example.com"
	ctx := WithSummary(context.Background(), NewSummary())
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{{
			Role: "user",
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: original,
			}},
		}},
	}
	if _, err := hook.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	encrypted := req.Messages[0].Content[0].Text
	for _, plain := range []string{"Alice Johnson", "Boston", "alice@example.com"} {
		if strings.Contains(encrypted, plain) {
			t.Errorf("plaintext %q leaked through Before; got %q", plain, encrypted)
		}
	}
	// All three entities must appear in the encrypt-token form.
	for _, label := range []string{"[PII:PERSON:", "[PII:LOCATION:", "[PII:EMAIL:"} {
		if !strings.Contains(encrypted, label) {
			t.Errorf("expected token prefix %q in encrypted text; got %q", label, encrypted)
		}
	}

	// Simulate the upstream response: echo back the encrypted text.
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: "assistant",
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: encrypted,
			}},
		},
	}
	if err := hook.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	got := resp.Message.Content[0].Text
	for _, plain := range []string{"Alice Johnson", "Boston", "alice@example.com"} {
		if !strings.Contains(got, plain) {
			t.Errorf("decrypt did not restore plaintext %q; got %q", plain, got)
		}
	}
}
```

If `pii_test.go` already imports `context` and `canonical`, omit those import additions; otherwise add them.

- [ ] **Step 12.3: Run; confirm pass**

Run: `go test -v ./internal/plugin/pii/ -run TestPIIRedactionHook_NEREncryptRoundTrip`
Expected: PASS.

- [ ] **Step 12.4: Commit**

```bash
git add internal/plugin/pii/pii_test.go
git commit -m "test(pii): end-to-end NER+encrypt round-trip for PERSON/LOCATION/Email"
```

---

## Task 13: Documentation — design doc, operator docs, README

**Files:**
- Modify: `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md`
- Modify: `docs/operating.md`
- Modify: `README.md` (only if it already enumerates PII entities — check first)

### Step 13.1: Append §11 to the encrypt design doc

- [ ] **Append to `docs/superpowers/specs/2026-06-01-pii-encrypt-design.md`:**

```markdown
## 11. Recognizer Expansion (2026-06-03)

The encrypt round-trip described in §1–§10 is recognizer-agnostic: any
entity name parsed by `redact()` flows through `ApplyMode("encrypt", …)`
into a `[PII:Entity:base64url]` token and is decrypted by the `After`
sweep. This section records the recognizer set that ships in the
2026-06-03 expansion plan.

### 11.1 Telecom Regex Recognizers

Seven additional regex-only recognizers ported from the loop_24
Privacy Vault project:

| Entity | Pattern (shape) | Context anchor |
|---|---|---|
| `SIP_URI` | `sips?:user@host[:port]` | None (pattern is distinctive) |
| `IMEI` | 15-digit run | Required: `imei`, `international mobile equipment identity` |
| `IMSI` | 15-digit run (same shape as IMEI) | Required: `imsi`, `international mobile subscriber identity` |
| `MSISDN` | `+E.164` | Required: `msisdn`, `subscriber number`, `calling number`, `called number` |
| `MAC_ADDRESS` | Six hex pairs with `:` or `-` | None |
| `COORDINATES` | Decimal-degrees lat/long with N/S/E/W | None |
| `SITE` | `site-XX_YYY` or `ENB/BTS/…-XXXX` | Required: site/cell/base station/network element terms |

Context-anchored recognizers run against a ±50 byte window (`defaultContextWindow`)
around each regex match. The recognizer struct gains `ContextKeywords []string`;
nil means "no context required" (preserves existing six recognizers
unchanged).

**Why position-based redact refactor:** Go regex has no variable-width
lookbehind, so context anchoring cannot be expressed in the regex itself.
The `redact()` function in `pii.go` was refactored from sequential
`ReplaceAllStringFunc` calls to two-phase span collection + rewrite,
which also enables NER overlap arbitration (§11.3).

### 11.2 prose NER (PERSON + LOCATION)

`jdkato/prose/v2` is added as a pure-Go NER engine emitting `PERSON`
and `LOCATION` spans. Opt-in via `PII_NER_ENABLED=true`; default off
so the prose model is not allocated on installs that do not need it.

**Why prose (not spaCy / BERT / transformers):**
- Pure Go: no CGo. Single-static-binary distribution preserved.
- Bundled model: averaged-perceptron NER weights ship inside the Go
  module. No model download, no first-run bootstrap, no network at
  install time. `curl|sh` install remains one command.

**Accuracy ceiling (known v1 limitation):**
- English-only.
- Decent on common Western names and major place names.
- Weaker on Asian / multilingual names and unusual locations.
- Roughly: spaCy small ≤ prose < spaCy large < BERT.

A future v2 may add an opt-in transformer-backed engine (first-run ONNX
model download), which is explicitly out of scope here.

### 11.3 Recognizer + NER Merge

Regex spans are collected first against the original input; NER spans
are collected second and merged greedily: NER candidates that overlap
any accepted regex span are dropped. This mirrors loop_24's
`_merge_results` greedy non-overlap policy. Intra-NER overlaps are
also resolved by registration order.

### 11.4 Configuration Surface

New env vars:

```bash
# Default false. When true, main.go constructs a *nerEngine and
# attaches it to PIIRedactionHook.NER. When false, no prose state is
# allocated.
PII_NER_ENABLED=true
```

Extended:
- `PII_ENABLED_ENTITIES` now accepts: `Email`, `IPv4`, `IPv6`, `SSN`,
  `CreditCard`, `USPhone`, `SIP_URI`, `IMEI`, `IMSI`, `MSISDN`,
  `MAC_ADDRESS`, `COORDINATES`, `SITE`, `PERSON`, `LOCATION`.
- `PII_ENTITY_ACTIONS` accepts the same expanded entity set.

Backward compatibility is preserved: when `PII_NER_ENABLED` is unset
and `PII_ENABLED_ENTITIES` does not include any new name, behavior is
bit-identical to pre-expansion.
```

- [ ] **Step 13.2: Update `docs/operating.md`**

- Env-var table (around line 219) — add rows for `PII_NER_ENABLED` and update the entity enumeration in the `PII_ENABLED_ENTITIES` / `PII_ENTITY_ACTIONS` rows.
- CLI table (around line 101) — extend the entity enumeration in the `--entities` row.
- Boot-error list (around line 266) — note that unknown entities now have a longer allowlist.

Suggested text for the new env-var row:

```markdown
| `PII_NER_ENABLED` | `false` | When `true`, attaches a `jdkato/prose/v2` NER engine to the PII hook, emitting `PERSON` and `LOCATION` spans alongside the regex recognizers. Default `false` so the prose model is not allocated unless requested. Adds ~15-25 MB to the binary at build time. English-only; see encrypt-design doc §11.2 for accuracy ceiling. |
```

- [ ] **Step 13.3: README**

Run: `grep -n "Email\|IPv4\|SSN\|PII_" README.md | head -30`

If the README enumerates recognizer names, append the new ones in the same shape. If it links to `docs/operating.md` without enumeration, no change required.

- [ ] **Step 13.4: Run all tests once more for safety**

Run: `go build ./... && go test ./...`

- [ ] **Step 13.5: Commit**

```bash
git add docs/superpowers/specs/2026-06-01-pii-encrypt-design.md docs/operating.md README.md
git commit -m "docs(pii): document telecom recognizers + prose NER accuracy ceiling"
```

---

## Task 14: Binary size report + final verification

**Files:**
- None modified — verification only.

### Step 14.1: Re-measure final binary size

- [ ] **Build with the same flags as the baseline:**

```bash
go build -trimpath -ldflags="-s -w" -o /tmp/otto-gateway-final ./cmd/otto-gateway
ls -lh /tmp/otto-gateway-pre /tmp/otto-gateway-final
```

Record:
- Pre-prose size (from Task 9.1).
- Final size.
- Delta.
- Whether delta > 30 MB (the user's flag-threshold).

If > 30 MB, leave a note in the design doc §11.2.

### Step 14.2: Full test suite + race + vet

- [ ] **Run:**

```bash
go vet ./...
go test -race ./...
```

Expected: clean.

### Step 14.3: Summary commit (no code, only optional binary-size note in design doc if threshold crossed)

- [ ] **If you added a binary-size addendum:**

```bash
git add docs/superpowers/specs/2026-06-01-pii-encrypt-design.md
git commit -m "docs(pii): record final binary-size delta after prose addition"
```

If no doc change, no commit. Task 14 is the verify-and-stop gate.

---

## Test Plan Summary

| Layer | Coverage | File |
|---|---|---|
| Helper: `hasContextWithin` | window, case, before/after, empty list | `contextual_test.go` |
| Helper: `mergeSpansGreedy` | no overlap, later-overlap dropped, sort, abutting | `spans_test.go` |
| Recognizer shape: SIP_URI | positive + negative literals | `recognizers_test.go` |
| Recognizer shape: IMEI / IMSI / MSISDN | length, ContextKeywords presence | `recognizers_test.go` |
| Recognizer shape: MAC_ADDRESS / COORDINATES / SITE | positive + negative literals | `recognizers_test.go` |
| Context disambiguation: IMSI vs IMEI | label depends on keyword | `recognizers_test.go` |
| Context disambiguation: MSISDN with/without keyword | end-to-end through Before | `recognizers_test.go` |
| NER: lazy init + Detect | PERSON, LOCATION, empty, repeat | `ner_test.go` |
| NER: integration through redact() | enable + filter + overlap arbitration | `ner_test.go` |
| NER: encrypt round-trip | PERSON ⇄ base64url ⇄ PERSON | `pii_test.go` |
| Config: PII_NER_ENABLED parse | default false + true | `config_test.go` |
| Config: allowlists include new names | PERSON/LOCATION/telecom | `config_test.go` |
| Binary size | pre/post delta recorded | (manual, Task 14) |

## Configuration Additions Summary

| Var | Default | Purpose |
|---|---|---|
| `PII_NER_ENABLED` | `false` | Enable prose NER for PERSON/LOCATION |
| `PII_ENABLED_ENTITIES` | (extended set) | Accepts 15 names now (6 existing + 9 new) |
| `PII_ENTITY_ACTIONS` | (extended set) | Accepts 15 entity keys × 5 actions |

## Self-Review

**Spec coverage:** All explicitly-requested items mapped to tasks:
- Part A — seven telecom regexes with context behavior → Tasks 2–8 (one task per entity); Recognizer extension and context window helper → Task 1.
- Part B — prose dep, lazy init, NER engine, regex/NER merge with regex-winning overlap → Tasks 9 + 10; off-by-default gating → Task 11.
- Part C — unit tests per recognizer → embedded in Tasks 2–8; encrypt round-trip via NER → Tasks 10 + 12; design-doc update → Task 13.1; operator-doc update → Task 13.2; README check → Task 13.3; binary-size report → Tasks 9.1, 9.8, 14.1.
- "Don't do" list — confirmed no CGo, no Python, no ONNX, no model download, no transformer v2.

**Placeholder scan:** All code blocks contain complete Go source. No "TODO" / "implement later" / "TBD" markers. The `acceptNERSpans` stub in Task 1.14 is intentional and replaced in Task 10.3; the placeholder `ner.go` in Task 1.15 is intentional and replaced in Task 9.6. Both stubs are explicitly flagged in their respective steps.

**Type consistency:**
- `span` struct used identically in `spans.go` / `contextual.go` / `pii.go` / `ner.go` / tests.
- `Recognizer` field name `ContextKeywords` used identically throughout.
- `PIIRedactionHook.NER *nerEngine` used identically throughout.
- `NewNEREngine() *nerEngine` factory name used identically throughout.
- Method names `(*PIIRedactionHook).collectRegexSpans`, `acceptNERSpans`, `enabledEntitiesSet` consistent across Tasks 1 and 10.
