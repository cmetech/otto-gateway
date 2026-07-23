// internal/plugin/compress/hook.go

package compress

import (
	"context"
	"log/slog"
	"sync/atomic"

	"otto-gateway/internal/canonical"
)

// Hook is the CompressionHook PreHook. Chain position: after
// PIIRedactionHook (compress redacted text; never resurface raw PII into
// stubs or logs), before LoggingHook (log what is actually sent).
//
// Enabled is the process-wide DEFAULT, not a hard gate — a per-request
// X-Compression header or a +compress/-compress model suffix overrides it
// in either direction. ENABLED_HOOKS remains the hard kill switch.
//
// Safe for concurrent Before calls: config fields are set once at
// construction; runtime state is atomic counters only.
type Hook struct {
	Enabled       bool
	TriggerTokens int
	BudgetTokens  int
	ProtectTail   int
	ToolKeep      int
	Logger        *slog.Logger

	eligible        atomic.Int64
	runs            atomic.Int64
	savedTok        atomic.Int64
	panicRecoveries atomic.Int64

	// budgetUnmet counts runs that ended still over BudgetTokens — the
	// budget is best-effort (pinned/protected/tool-carrying messages are
	// never elided, and stage 4's zero-evidence stop can leave the
	// transcript over budget by design).
	budgetUnmet atomic.Int64
}

// Name reports the filter-discovery name for chain.Filter (Pattern A —
// explicit Name() over reflect for stable API).
func (h *Hook) Name() string { return "CompressionHook" }

// Describe publishes config + lifetime counters for /health/hooks
// (OBSV-04). Everything here is static config or an atomic counter —
// nothing sensitive (stage 4 is local; there is no endpoint to leak).
// Note "runs" and "budget_unmet" answer different questions: runs counts
// only net-shrinking compressions (compress's `saved > 0` gate below),
// while budget_unmet counts every enabled run that crossed TriggerTokens
// and ended over BudgetTokens regardless of whether anything shrank — so
// budget_unmet can legitimately exceed runs.
func (h *Hook) Describe() (string, map[string]any) {
	return "Pre", map[string]any{
		"enabled":          h.Enabled,
		"trigger_tokens":   h.TriggerTokens,
		"budget_tokens":    h.BudgetTokens,
		"protect_tail":     h.ProtectTail,
		"tool_keep":        h.ToolKeep,
		"eligible":         h.eligible.Load(),
		"runs":             h.runs.Load(),
		"tokens_saved_est": h.savedTok.Load(),
		"budget_unmet":     h.budgetUnmet.Load(),
		"panic_recoveries": h.panicRecoveries.Load(),
	}
}

// StatsSnapshot is an atomic snapshot of the hook's lifetime decisions and
// outcomes. Each field is loaded independently; callers use it for monotonic
// observability rather than transactional accounting.
type StatsSnapshot struct {
	Eligible        int64
	Runs            int64
	SavedTokens     int64
	BudgetUnmet     int64
	PanicRecoveries int64
}

// Stats returns the lifetime counters (Prometheus CounterFunc seam — see
// metrics.RegisterCompression).
func (h *Hook) Stats() StatsSnapshot {
	return StatsSnapshot{
		Eligible:        h.eligible.Load(),
		Runs:            h.runs.Load(),
		SavedTokens:     h.savedTok.Load(),
		BudgetUnmet:     h.budgetUnmet.Load(),
		PanicRecoveries: h.panicRecoveries.Load(),
	}
}

func (h *Hook) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// Before is the PreHook entry point.
//
// CONTRACT: always returns (nil, nil). engine.callPreHookSafe converts a
// hook panic into a request-ABORTING error, so Before installs its own
// recover — compression must never be able to break a request.
func (h *Hook) Before(ctx context.Context, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.panicRecoveries.Add(1)
			h.logger().ErrorContext(ctx, "compress.panic_recovered", "panic", r)
			resp, err = nil, nil
		}
	}()

	if req == nil || len(req.Messages) == 0 {
		return nil, nil
	}

	// Effective enablement: header > model-suffix directive > env default.
	on := h.Enabled
	if v, ok := req.Metadata[MetadataKey].(bool); ok { // nil-map read is safe
		on = v
	}
	if v, ok := HeaderDirectiveFromContext(ctx); ok {
		on = v
	}
	if !on {
		return nil, nil
	}

	h.compress(ctx, req)
	return nil, nil
}

// compress runs the 4-stage pipeline in place. The budget is re-checked
// BETWEEN stages: once the estimate is at or under BudgetTokens no
// further (lossier) stage runs — stage 1 alone reaching the budget must
// not be followed by truncation or collapse (review 2 MAJOR-1).
func (h *Hook) compress(ctx context.Context, req *canonical.ChatRequest) {
	msgs := req.Messages
	before := estMessagesTokens(msgs)
	if before < h.TriggerTokens {
		return // not worth the work
	}
	if before <= h.BudgetTokens {
		return // already within budget (possible when budget == trigger) —
		// never lossily mutate a transcript that already fits
	}
	h.eligible.Add(1)

	tailStart := len(msgs) - h.ProtectTail
	if tailStart < 0 {
		tailStart = 0
	}
	// Two pins, immutable across ALL stages (second-pass MAJOR-3 +
	// third-pass MAJOR-1):
	// lastIdx — the current inbound turn (on OpenAI/Ollama a follow-up
	// can END in a RoleTool result the model must consume); queryIdx —
	// the latest user-text question (stage 4's relevance query). With
	// PROTECT_TAIL=0 nothing else protects either one.
	lastIdx, queryIdx := findPinned(msgs)
	mutable := func(i int) bool {
		return i < tailStart && i != lastIdx && i != queryIdx && msgs[i].Role != canonical.RoleSystem
	}
	overBudget := func() bool { return estMessagesTokens(msgs) > h.BudgetTokens }

	// Stage 1: blank-line/trailing-space cleanup (low-loss normalization).
	for i := range msgs {
		if mutable(i) {
			normalizeMessageWhitespace(&msgs[i])
		}
	}
	// Stage 2: stale tool-result truncation.
	if overBudget() {
		for i := range msgs {
			if mutable(i) {
				truncateToolResults(&msgs[i], h.ToolKeep)
			}
		}
	}
	// Stage 3: exact-duplicate collapse.
	if overBudget() {
		collapseDuplicates(msgs, mutable)
	}
	// Stage 4: local BM25 relevance pruning — fully in-process (no
	// network, no external model), so it runs whenever still over
	// budget. Elides nothing when no candidate shares a token with the
	// question (zero-evidence stop) or when there is no user question.
	if overBudget() {
		pruneByRelevance(ctx, msgs, mutable, queryIdx, h.BudgetTokens)
	}

	after := estMessagesTokens(msgs)
	if after > h.BudgetTokens {
		// Best-effort budget: pinned/protected/tool-carrying messages are
		// never elided and zero-evidence stops pruning, so the budget can
		// legitimately go unmet.
		h.budgetUnmet.Add(1)
	}
	if saved := before - after; saved > 0 {
		h.runs.Add(1)
		h.savedTok.Add(int64(saved))
		h.logger().DebugContext(ctx, "compress.done",
			"before_est_tokens", before, "after_est_tokens", after, "saved_est_tokens", saved)
	}
}
