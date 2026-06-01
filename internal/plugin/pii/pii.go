// Phase 8 PLUG-06 — PIIRedactionHook (Pre). Walks canonical.ChatRequest
// content per D-03 (ContentParts[].Text + ContentParts[].ToolUse.Input +
// ContentParts[].ToolResult.Content + ChatRequest.System per RESEARCH
// OQ-5 disposition). Recognizes via the six-entry Recognizers registry
// from recognizers.go. Applies modes.ApplyMode per match. Counter scope:
// per-canonical.ChatRequest (resets each Before call per RESEARCH
// Pitfall 4 + CONTEXT.md Claude's Discretion). Populates the D-04
// summary seam (slice 3's pii.WithSummary / SummaryFromContext) so
// LoggingHook can emit redacted={Email:2, SSN:1}.
//
// Pre-only; no Post behavior. In-place mutation on req (Codex H-5
// discipline mirrored — req is a pointer; ContentParts slice elements
// mutated in place via index assignment).
//
// Summary seam contract (LOCKED per 08-03-SUMMARY "Next Phase
// Readiness" + this slice's orchestrator instruction):
//
//	PIIRedactionHook does NOT call pii.WithSummary itself. The
//	production-path adapter middleware (slice 5 Task 4b) stamps
//	ctx = pii.WithSummary(ctx, pii.NewSummary()) BEFORE engine entry,
//	so PIIRedactionHook and LoggingHook share the SAME *Summary
//	pointer via ctx. This hook reads via SummaryFromContext and
//	calls Summary.Add(entity) per match on the EXISTING pointer.
//
//	Defensive fallback for unit tests / disabled-PII paths: if
//	SummaryFromContext returns (nil, false), the hook constructs a
//	local *Summary and continues populating it (so internal counter
//	bookkeeping stays correct and the hook still mutates req
//	consistently). The local Summary is NOT stamped back onto ctx
//	via WithSummary because Before's ctx return is not propagated by
//	engine.Run to subsequent hooks (08-RESEARCH OQ-1) — the slice-5
//	middleware stamp is the only correct propagation seam.
//
// Counter-suffix scope (T-8-PII-COUNTER mitigation): a fresh counter
// map (per-canonical-value, scoped to this single Before call) is
// constructed each invocation and discarded on return. The same value
// in the same request shares a counter slot (preserving intra-prompt
// referential identity per RESEARCH Pitfall 4: 'corey@x.com' appearing
// twice → both render as '[EMAIL_1]'); a different value in the same
// request gets the next number; a new request resets the counter.
//
// T-8-PII discipline (in-place, no copies): req is mutated via index
// assignment; no field of req is copied to a long-lived structure.
// gosec G204 doesn't apply (no subprocess spawn); the discipline here is
// "never store raw req in a package-level value or accidentally close
// over it in a goroutine that outlives Before".

package pii

import (
	"context"
	"log/slog"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// PIIRedactionHook is the canonical-layer PII redaction Pre hook.
//
//	Recognizers      — the active recognizer registry (slice 5's main.go
//	                   passes pii.Recognizers; tests may inject subsets).
//	Enabled          — the work-doing toggle (controlled by
//	                   PII_REDACTION_ENABLED env; D-02 two-knob model).
//	Mode             — one of "replace" / "mask" / "hash" / "drop" /
//	                   "encrypt" (slice 5 validates).
//	HashKey          — the PII_HASH_KEY bytes for hash mode (slice 5
//	                   validates non-empty when Mode=="hash").
//	EnabledEntities  — optional allowlist filter; empty means all
//	                   Recognizers are active. Order in this slice does
//	                   NOT change recognizer iteration order; it is a
//	                   set semantically.
//	EncryptKey       — the 32-byte AES-256-GCM key for encrypt mode
//	                   (Task 3). Boot validation guarantees non-nil when
//	                   encrypt is active; nil otherwise.
//	EntityActions    — optional per-entity action override map (Task 4).
//	                   Empty = global Mode applies to all recognizers
//	                   (today's behavior).
type PIIRedactionHook struct {
	Recognizers []Recognizer
	Enabled     bool
	Mode        string
	HashKey     []byte
	// EncryptKey is the 32-byte AES-256-GCM key for the "encrypt" action
	// (Mode=="encrypt" or EntityActions[X]=="encrypt"). Nil when encrypt is
	// not active. Boot validation guarantees non-nil when encrypt IS active.
	EncryptKey []byte
	// EntityActions overrides the global Mode per recognizer Name.
	// e.g., {"Email":"encrypt","SSN":"mask"} → Email matches use encrypt,
	// SSN matches use mask, all other entities fall back to Mode.
	// Empty map reproduces today's behavior exactly (Mode applies to all).
	EntityActions   map[string]string
	EnabledEntities []string
	// Logger is the slog target for observability DEBUG lines (e.g.,
	// pii.redact.done). nil-falls-back to slog.Default() at first use.
	// Wired by main.go for the production-path adapter; tests may leave
	// nil — defensive fallback keeps the hook side-effect-free.
	Logger *slog.Logger
}

// Name reports the filter-discovery name for chain.Filter (08-PATTERNS
// Pattern A — explicit Name() over reflect for stable API).
func (h *PIIRedactionHook) Name() string { return "PIIRedactionHook" }

// Describe publishes the hook's safe-to-publish config for /health/hooks
// (OBSV-04). Kind is "Pre" — this hook is Pre-only by design (slice 5
// places it FOURTH-from-last in Pre order: RequestID → Auth → PII →
// Logging per D-04).
//
// Config exposes only:
//
//	enabled  bool      — operator can confirm work-doing state without
//	                     reading log records.
//	mode     string    — one of the four documented modes.
//	entities []string  — the recognizer Names actually active after
//	                     EnabledEntities filtering (or all Recognizers
//	                     when no filter applied).
//
// HashKey is NEVER published (T-8-LEAK). Regex patterns are NEVER
// published (T-8-PII secondary — patterns themselves are not secret
// but publishing them encourages clients to optimize around bypasses).
func (h *PIIRedactionHook) Describe() (kind string, config map[string]any) {
	entities := h.activeEntityNames()
	return "Pre", map[string]any{
		"enabled":  h.Enabled,
		"mode":     h.Mode,
		"entities": entities,
	}
}

// activeEntityNames returns the names of recognizers that would actually
// fire on Before, in registration order. Used by Describe AND by the
// inner Before loop to skip recognizers an operator excluded via
// EnabledEntities. Empty EnabledEntities means "all recognizers active".
func (h *PIIRedactionHook) activeEntityNames() []string {
	if len(h.EnabledEntities) == 0 {
		out := make([]string, 0, len(h.Recognizers))
		for _, r := range h.Recognizers {
			out = append(out, r.Name)
		}
		return out
	}
	allow := make(map[string]struct{}, len(h.EnabledEntities))
	for _, e := range h.EnabledEntities {
		allow[e] = struct{}{}
	}
	out := make([]string, 0, len(h.EnabledEntities))
	for _, r := range h.Recognizers {
		if _, ok := allow[r.Name]; ok {
			out = append(out, r.Name)
		}
	}
	return out
}

// activeRecognizers returns the Recognizer entries actually used during
// Before, applying the EnabledEntities filter while preserving the
// registration order from Recognizers.
func (h *PIIRedactionHook) activeRecognizers() []Recognizer {
	if len(h.EnabledEntities) == 0 {
		return h.Recognizers
	}
	allow := make(map[string]struct{}, len(h.EnabledEntities))
	for _, e := range h.EnabledEntities {
		allow[e] = struct{}{}
	}
	out := make([]Recognizer, 0, len(h.EnabledEntities))
	for _, r := range h.Recognizers {
		if _, ok := allow[r.Name]; ok {
			out = append(out, r)
		}
	}
	return out
}

// actionFor returns the action this hook should apply to a given
// entity. EntityActions[entity] wins when set; otherwise h.Mode.
func (h *PIIRedactionHook) actionFor(entity string) string {
	if a, ok := h.EntityActions[entity]; ok {
		return a
	}
	return h.Mode
}

// encryptActive reports whether any active entity is configured for
// encrypt mode. Used by Before's stream-disable side effect and by
// After's no-op fast path. Cheap O(len(EntityActions)).
func (h *PIIRedactionHook) encryptActive() bool {
	if h.Mode == "encrypt" {
		return true
	}
	for _, a := range h.EntityActions {
		if a == "encrypt" {
			return true
		}
	}
	return false
}

// logger returns h.Logger if set, otherwise slog.Default(). Per-call
// fallback (never cached) so changes to slog.Default at boot are seen.
// Mirrors LoggingHook.logger() (internal/plugin/logging.go) — the canonical
// nil-default pattern across canonical-layer hooks.
func (h *PIIRedactionHook) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// Before is the PreHook entry. Algorithm:
//
//  1. If !Enabled: return (nil, nil) — total no-op. Counter never
//     increments, Summary never populated. Matches D-02's two-knob model
//     (ENABLED_HOOKS controls chain membership; per-hook Enabled flag
//     controls work-doing).
//  2. Read summary, ok := pii.SummaryFromContext(ctx). If !ok, construct
//     a local *Summary so internal bookkeeping stays consistent (see
//     package docstring for why we don't WithSummary here).
//  3. Build per-canonical-value counter map (T-8-PII-COUNTER mitigation:
//     fresh per Before call; the same canonical value appearing twice
//     in one request gets the same suffix — referential identity).
//  4. Walk + mutate req IN PLACE per D-03:
//     - ChatRequest.System (RESEARCH OQ-5 disposition)
//     - For each Message: each ContentPart by Kind:
//     - ContentKindText: redact ContentParts[j].Text
//     - ContentKindToolUse: WalkStrings on ToolUse.Input map, write
//     back via type-assertion guard
//     - ContentKindToolResult: redact ToolResult.Content (string)
//  5. Return (nil, nil). Never short-circuits, never errors in v1.
func (h *PIIRedactionHook) Before(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if !h.Enabled || req == nil {
		return nil, nil
	}

	summary, _ := SummaryFromContext(ctx)
	if summary == nil {
		// Defensive fallback for paths that didn't stamp ctx (unit
		// tests should stamp via withCtxSummary helper; production
		// stamps via slice-5 middleware). Local Summary keeps internal
		// counter consistent even if no downstream reader exists.
		summary = NewSummary()
	}

	recs := h.activeRecognizers()
	// counters: per-canonical-value occurrence number. Keyed by
	// "<entity>|<canonical-value>" so the SAME value in a request reuses
	// the same suffix (referential identity per RESEARCH Pitfall 4),
	// while a different value in the same request gets the next slot
	// per-entity.
	counters := make(map[string]int)
	// nextN: per-entity next-issued number. Increments only when a
	// previously-unseen canonical value is encountered.
	nextN := make(map[string]int)

	redact := func(s string) string {
		out := s
		for _, r := range recs {
			out = r.Pattern.ReplaceAllStringFunc(out, func(match string) string {
				if r.Validate != nil && !r.Validate(match) {
					return match
				}
				// Canonicalize for counter-key dedup so 'Corey@CMETECH.io'
				// and 'corey@cmetech.io' share a counter slot (matches the
				// hash-mode canonical form for consistency across modes).
				key := r.Name + "|" + canonicalForm(match)
				n, seen := counters[key]
				if !seen {
					nextN[r.Name]++
					n = nextN[r.Name]
					counters[key] = n
				}
				summary.Add(r.Name)
				return ApplyMode(h.Mode, r.Name, match, n, h.HashKey, h.EncryptKey)
			})
		}
		return out
	}

	// ChatRequest.System (operator-side PII per RESEARCH OQ-5).
	req.System = redact(req.System)

	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			cp := &req.Messages[i].Content[j]
			switch cp.Kind {
			case canonical.ContentKindText:
				cp.Text = redact(cp.Text)
			case canonical.ContentKindToolUse:
				if cp.ToolUse == nil || cp.ToolUse.Input == nil {
					continue
				}
				walked := WalkStrings(cp.ToolUse.Input, redact)
				if m, ok := walked.(map[string]any); ok {
					cp.ToolUse.Input = m
				}
			case canonical.ContentKindToolResult:
				if cp.ToolResult == nil {
					continue
				}
				cp.ToolResult.Content = redact(cp.ToolResult.Content)
			default:
				// ContentKindImage / ContentKindThinking — no string
				// LEAVES to walk in v1 (image is base64; thinking is
				// model-side, not user-input). Pass through unchanged.
			}
		}
	}

	// request_id intentionally omitted to avoid plugin→pii→plugin import cycle; correlate via timestamps + active_recognizers count.
	h.logger().Debug("pii.redact.done",
		"active_recognizers", len(recs),
		"mode", h.Mode,
	)

	return nil, nil
}

// Compile-time PreHook interface satisfaction. If a future engine
// signature change drifts the PreHook contract, this line fails to
// build at the hook's source — surfaces the regression at the right
// blame target instead of at the slice-5 wiring site.
var _ engine.PreHook = (*PIIRedactionHook)(nil)
