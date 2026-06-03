// Package jsonformat implements jsonformat.SteeringHook (operator-facing
// runtime name "JSONFormatSteeringHook"), a canonical-layer
// PreHook that appends a hard-steering system-prompt block whenever
// canonical.ChatRequest.Format is non-nil. This reproduces the Node Ollama
// shim's GEN_RULES injection so LangFlow flows that set format:"json" or
// format:<JSON schema> observe equivalent behavior against otto-gateway as
// they do against the Node shim.
//
// The hook is surface-agnostic: it fires on req.Format != nil regardless of
// which surface (Ollama, OpenAI, Anthropic) populated that field. Today only
// the Ollama adapter populates Format (Phase 08.2); the day other adapters
// wire up their respective response_format fields the steering applies for
// free — no hook change required.
//
// Phase 08.2 D-03, D-04, D-05, D-06, D-07.
package jsonformat

import (
	"context"
	"encoding/json"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// genRulesBlock is the verbatim GEN_RULES text from the Node shim (D-03).
// Copied byte-for-byte including the em-dash so LangFlow flows observe
// identical client-visible behavior after the otto-gateway cutover.
//
// The raw-string literal preserves the two-line structure and the em-dash
// without any escape gymnastics.
const genRulesBlock = `Generate the COMPLETE result for EVERY item requested in this single response — do not summarize, truncate, abbreviate, or omit items.
Do NOT add any prose, preamble, commentary, or follow-up questions, and do NOT offer to export, save, or write to a file. Output the data directly.`

// SteeringHook appends the GEN_RULES steering block (and an
// optional JSON-schema description) to req.System whenever req.Format is
// non-nil. It is a Pre-only hook — it mutates the request and returns
// (nil, nil) to continue the chain.
//
// Safe for concurrent Before calls: no mutable state beyond Enabled, which
// is set once at construction and never written after that.
type SteeringHook struct {
	Enabled bool
}

// New constructs a SteeringHook. enabled mirrors the
// JSON_FORMAT_STEERING_ENABLED env knob (default true per D-06).
func New(enabled bool) *SteeringHook {
	return &SteeringHook{Enabled: enabled}
}

// Name reports the filter-discovery name for chain.Filter (Pattern A —
// explicit Name() over reflect for stable API). Matches the hook-chain
// introspection key operators see via GET /health/hooks.
func (h *SteeringHook) Name() string { return "JSONFormatSteeringHook" }

// Describe publishes the hook's safe-to-publish config for /health/hooks
// (OBSV-04). Kind is "Pre" — this hook is Pre-only.
func (h *SteeringHook) Describe() (kind string, config map[string]any) {
	return "Pre", map[string]any{
		"enabled":    h.Enabled,
		"default_on": true,
	}
}

// Before is the PreHook entry point.
//
// Algorithm:
//  1. If !h.Enabled: return (nil, nil) — total pass-through.
//  2. If req.Format == nil: return (nil, nil) — no steering needed.
//  3. Build steering text: start with genRulesBlock.
//     If req.Format.Type == "json_schema" AND req.Format.Schema != nil:
//     marshal Schema to compact JSON and append the schema description line.
//  4. Append steering text to req.System with "\n\n" separator when System
//     is non-empty, or assign directly when System is empty.
//  5. Return (nil, nil).
//
// The hook does NOT short-circuit. It mutates req.System in-place and
// forwards.
func (h *SteeringHook) Before(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if !h.Enabled {
		return nil, nil
	}
	if req == nil || req.Format == nil {
		return nil, nil
	}

	steeringText := genRulesBlock

	// D-04: when the format carries a JSON schema, append a compact
	// description line so the model sees the expected output shape.
	if req.Format.Type == "json_schema" && req.Format.Schema != nil {
		schemaJSON, err := json.Marshal(req.Format.Schema)
		if err == nil {
			steeringText += "\nThe output must match this JSON schema: " + string(schemaJSON)
		}
		// On marshal error (shouldn't happen for map[string]any from
		// json.Unmarshal) skip the schema line and continue with genRulesBlock.
	}

	if req.System == "" {
		req.System = steeringText
	} else {
		req.System = req.System + "\n\n" + steeringText
	}

	return nil, nil
}

// Compile-time PreHook interface satisfaction. If a future engine signature
// change drifts the PreHook contract, this line fails to build at the
// hook's source — surfaces the regression at the right blame target.
var _ engine.PreHook = (*SteeringHook)(nil)
