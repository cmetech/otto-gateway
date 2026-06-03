// Phase 8 PLUG-06 — PIIRedactionHook (Pre + Post). Implements both
// engine.PreHook (Before) and engine.PostHook (After) for the encrypt
// round-trip. Walks canonical.ChatRequest content per D-03
// (ContentParts[].Text + ContentParts[].ToolUse.Input +
// ContentParts[].ToolResult.Content + ChatRequest.System per RESEARCH
// OQ-5 disposition). Recognizes via the six-entry Recognizers registry
// from recognizers.go. Applies modes.ApplyMode per match. Counter scope:
// per-canonical.ChatRequest (resets each Before call per RESEARCH
// Pitfall 4 + CONTEXT.md Claude's Discretion). Populates the D-04
// summary seam (slice 3's pii.WithSummary / SummaryFromContext) so
// LoggingHook can emit redacted={Email:2, SSN:1}.
//
// In-place mutation discipline (Codex H-5): Before mutates req in place
// (req is a pointer; ContentParts slice elements mutated via index
// assignment). After mutates resp.Message.Content in place via the same
// index-assignment discipline — no copies of resp are made, no fields
// are stored in long-lived package-level values.
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
	"regexp"
	"strings"

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
	// NER, when non-nil, augments regex recognizers with prose-based
	// PERSON/LOCATION detection. Constructed by main.go when
	// PII_NER_ENABLED=true. nil = NER disabled (no prose model load).
	// See ner.go (Task 9).
	NER *nerEngine
}

// Name reports the filter-discovery name for chain.Filter (08-PATTERNS
// Pattern A — explicit Name() over reflect for stable API).
func (h *PIIRedactionHook) Name() string { return "PIIRedactionHook" }

// Describe publishes the hook's safe-to-publish config for /health/hooks
// (OBSV-04). Kind is "Pre,Post" — this hook is dual-interface (matches
// the LoggingHook precedent). The encrypt round-trip puts the same
// instance in chain.Pre (encrypt) and chain.Post (decrypt).
//
// HashKey and EncryptKey are NEVER published (T-8-LEAK).
func (h *PIIRedactionHook) Describe() (kind string, config map[string]any) {
	entities := h.activeEntityNames()
	return "Pre,Post", map[string]any{
		"enabled":        h.Enabled,
		"mode":           h.Mode,
		"entities":       entities,
		"decrypt_active": h.encryptActive(),
		"entity_actions": h.EntityActions, // safe: action names only
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

// enabledEntitiesSet returns h.EnabledEntities as a set, or nil if the
// allowlist is empty (caller treats nil as "allow all"). Used by both
// the regex collector (indirectly via activeRecognizers) and by
// acceptNERSpans (Task 10).
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

// decryptTokenRe matches the encrypt-mode wire token shape
// "[PII:<entity>:<base64url-payload>]". Group 1 = entity, Group 2 =
// payload. base64url alphabet is [A-Za-z0-9_-], unpadded — see
// EncryptValue (encrypt.go). Pre-compiled at package init.
var decryptTokenRe = regexp.MustCompile(`\[PII:([A-Za-z]+):([A-Za-z0-9_-]+)\]`)

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

	// Encrypt round-trip needs the full response buffered before decrypt
	// can run, so we flip req.Stream off here. The Post hook (After) runs
	// in engine.Collect on the aggregated response — streaming branches
	// would bypass it because their bytes hit the wire before Collect
	// finishes. Spec §3.1.
	if h.encryptActive() && req.Stream {
		req.Stream = false
		h.logger().Info(
			"pii.encrypt.streaming_disabled",
			"reason", "decrypt requires aggregated response",
		)
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
			candidates := h.NER.Detect(s)
			nerSpans = h.acceptNERSpans(s, candidates, regexSpans, counters, nextN, summary)
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
	h.logger().Debug(
		"pii.redact.done",
		"active_recognizers", len(recs),
		"mode", h.Mode,
	)

	return nil, nil
}

// After is the PostHook entry for the encrypt round-trip. It scans
// every ContentKindText part in resp.Message.Content for
// "[PII:<entity>:<base64url>]" tokens and decrypts each via
// DecryptToken. Failures (mangled token shape, bad base64, GCM Open
// error from wrong key / AAD mismatch / corruption) leave the token
// verbatim and emit a WARN — the client sees a visible defect, not
// a silent lie.
//
// After Algorithm:
//
//  1. Cheap fast-path no-op: if !h.Enabled || resp == nil ||
//     !encryptActive(), return nil immediately. engine.Collect still
//     ranges PostHooks even when encryptActive is false; this hook just
//     exits early.
//  2. For each ContentKindText part, run decryptTokenRe.ReplaceAllStringFunc.
//     Per match: regex FindStringSubmatch extracts entity + payload;
//     DecryptToken decrypts; on success the match is replaced with
//     plaintext; on failure (bad shape, GCM error) the match is left
//     verbatim and a WARN is emitted via pii.decrypt.failed.
//  3. Non-Text content parts (image, thinking, tool_use, tool_result) are
//     skipped — encrypt round-trip is scoped to Text only in v1. The
//     tool_use/tool_result surfaces ARE encrypted on the Pre side (Before
//     walks them via WalkStrings) but the kiro-cli response surface is
//     text-only for the assistant turn, so decrypt only needs Text.
//  4. Failure WARN log shape: pii.decrypt.failed with entity + reason +
//     err. Reason categories: bad_token_shape (malformed submatch),
//     bad_base64 (DecryptToken base64 decode error), gcm_open (GCM
//     authentication failure — usually wrong key, AAD mismatch, or
//     tag corruption), payload_too_short (blob shorter than the GCM
//     nonce size), decrypt_other (any unclassified DecryptToken error).
//
// req is unused but required by the engine.PostHook interface; matches
// the LoggingHook precedent.
func (h *PIIRedactionHook) After(ctx context.Context, _ *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	if !h.Enabled || resp == nil || !h.encryptActive() {
		return nil
	}
	for i := range resp.Message.Content {
		cp := &resp.Message.Content[i]
		if cp.Kind != canonical.ContentKindText {
			continue
		}
		cp.Text = decryptTokenRe.ReplaceAllStringFunc(cp.Text, func(match string) string {
			sub := decryptTokenRe.FindStringSubmatch(match)
			if len(sub) != 3 {
				h.logger().Warn("pii.decrypt.failed", "reason", "bad_token_shape")
				return match
			}
			entity, payload := sub[1], sub[2]
			pt, err := DecryptToken(h.EncryptKey, entity, payload)
			if err != nil {
				h.logger().Warn("pii.decrypt.failed",
					"entity", entity,
					"reason", classifyDecryptErr(err),
					"err", err)
				return match
			}
			return pt
		})
	}
	return nil
}

// classifyDecryptErr maps a DecryptToken error into a stable reason
// category for slog filtering. Prefix-matches the error string against
// the wrapping prefixes emitted by DecryptToken (encrypt.go); falls
// back to "decrypt_other" for anything unrecognized. Stringly-typed
// rather than sentinel-typed to keep DecryptToken's API free of
// exported error variables.
func classifyDecryptErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "bad base64"):
		return "bad_base64"
	case strings.Contains(msg, "payload too short"):
		return "payload_too_short"
	case strings.Contains(msg, "gcm open"):
		return "gcm_open"
	default:
		return "decrypt_other"
	}
}

// Compile-time PreHook interface satisfaction. If a future engine
// signature change drifts the PreHook contract, this line fails to
// build at the hook's source — surfaces the regression at the right
// blame target instead of at the slice-5 wiring site.
var _ engine.PreHook = (*PIIRedactionHook)(nil)

// Compile-time PostHook interface satisfaction (mirrors the existing
// PreHook line above). Drift in engine.PostHook surfaces here at the
// right blame target.
var _ engine.PostHook = (*PIIRedactionHook)(nil)
