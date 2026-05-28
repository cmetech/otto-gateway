// Package plugin provides the day-one Pre/Post hooks for OTTO Gateway:
// RequestIDHook, AuthHook, LoggingHook, and PIIRedactionHook (subpackage
// pii — Phase 8 slice 4).
//
// Per Phase 8 D-01 (08-CONTEXT.md): there is NO Register(name, factory)
// registry. The chain is a hardcoded literal slice constructed in
// cmd/otto-gateway/main.go. This package's Chain type bundles
// []engine.PreHook + []engine.PostHook + Filter + Describe so the wiring
// (main.go) and introspection (/health/hooks for OBSV-04) paths share
// one type.
//
// References:
//   - 08-CONTEXT.md D-01 (hardcoded chain, no registry)
//   - 08-CONTEXT.md D-02 (ENABLED_HOOKS allowlist + typo-fail-fast)
//   - 08-RESEARCH.md Pattern 1 (Hardcoded Chain)
//   - 08-RESEARCH.md Open Question 1 recommendation (plugin imports
//     engine for the PreHook/PostHook interface types — implementing
//     them without depending on the package is impossible in Go, so
//     the dep is honest).
package plugin

import (
	"errors"
	"fmt"
	"reflect"

	"otto-gateway/internal/engine"
)

// Chain bundles the Pre and Post hook slices that the engine consumes
// via engine.Config{PreHooks, PostHooks}. The slice order IS the
// canonical execution order (D-02 SC5) — adding a hook is one line in
// main.go's literal; reordering is a deliberate edit.
//
// Chain itself is a value type with no internal state; copies are
// cheap and zero-Chain ({}) is a valid empty chain.
type Chain struct {
	Pre  []engine.PreHook
	Post []engine.PostHook
}

// HookDescription is the GET /health/hooks per-hook wire row (OBSV-04).
// JSON tags are the load-bearing wire contract — operators / dashboards
// depend on the snake_case-equivalent names.
//
// The Kind field is one of "Pre", "Post", or "Pre,Post" (a single hook
// implementing both interfaces appears once with the combined kind to
// keep the wire shape de-duplicated).
//
// Config exposes only fields each hook considers safe to publish
// (08-CONTEXT.md Claude's Discretion + 08-RESEARCH.md Pitfall 9 —
// describe whitelist, never secrets like tokens, regex patterns, or
// hash keys).
type HookDescription struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// Describer is the consumer-defined interface each hook implements to
// publish its non-secret config for /health/hooks. Hooks declare what
// THEY consider safe to publish — Chain.Describe does not inspect or
// override (08-CONTEXT.md "each hook's Describe declares what's safe").
//
// Hooks that do not implement Describer get a default-kind row
// inferred from which slice they appear in (Pre vs Post) and a nil
// Config.
type Describer interface {
	Describe() (kind string, config map[string]any)
}

// namer is the optional interface a hook can implement to declare its
// own name for chain.Filter and chain.Describe. Hooks that don't
// implement Name() fall back to a reflect-based type-name extraction
// (e.g., "*plugin.RequestIDHook" → "RequestIDHook").
//
// Preferring an explicit Name() over reflection makes the name part of
// the hook's API contract (caller-discoverable, test-overridable for
// fakes) instead of an accident of the type's identifier (which
// renaming would silently break).
type namer interface {
	Name() string
}

// Filter returns a Chain containing only the hooks whose name appears
// in allowlist. An empty allowlist returns the chain unchanged
// (default-permissive — D-02 / AUTH_TOKEN-parity semantics).
//
// A name in allowlist that does NOT match any hook in the chain is a
// boot error: this is the typo-fail-fast protection from D-02
// (ENABLED_HOOKS=PIIRedaction missing the "Hook" suffix would silently
// disable PII redaction, which the boot error refuses).
//
// Filter PRESERVES REGISTRATION ORDER (D-02 SC5): if the chain is
// [A, B, C] and allowlist is ["C", "A"], the result is [A, C] — not
// [C, A]. Operators cannot silently rewrite the documented hook
// sequence via env-var ordering.
//
// Hooks that fail name extraction (no Name() method, anonymous type)
// are filtered out when an allowlist is present — they can never be
// addressed by name.
func (c Chain) Filter(allowlist []string) (Chain, error) {
	if len(allowlist) == 0 {
		return c, nil
	}

	allow := make(map[string]struct{}, len(allowlist))
	matched := make(map[string]bool, len(allowlist))
	for _, n := range allowlist {
		allow[n] = struct{}{}
		matched[n] = false
	}

	filteredPre := make([]engine.PreHook, 0, len(c.Pre))
	for _, h := range c.Pre {
		name := hookName(h)
		if name == "" {
			continue
		}
		if _, ok := allow[name]; ok {
			filteredPre = append(filteredPre, h)
			matched[name] = true
		}
	}

	filteredPost := make([]engine.PostHook, 0, len(c.Post))
	for _, h := range c.Post {
		name := hookName(h)
		if name == "" {
			continue
		}
		if _, ok := allow[name]; ok {
			filteredPost = append(filteredPost, h)
			matched[name] = true
		}
	}

	// Typo-fail-fast: any allowlist name that didn't match a hook is a
	// boot error. Use errors.Join so multiple typos in one ENABLED_HOOKS
	// value surface together (better operator UX than fixing-one-then-
	// finding-the-next-on-restart).
	var unknown []error
	for _, name := range allowlist {
		if !matched[name] {
			unknown = append(unknown, fmt.Errorf("unknown hook in ENABLED_HOOKS: %q", name))
		}
	}
	if len(unknown) > 0 {
		return Chain{}, errors.Join(unknown...)
	}

	return Chain{Pre: filteredPre, Post: filteredPost}, nil
}

// Describe returns the JSON-tagged per-hook description rows for the
// /health/hooks introspection endpoint (OBSV-04). Both slices are
// returned non-nil-even-when-empty so JSON encoding produces `[]`
// rather than `null` (wire contract — dashboards expect arrays).
//
// For each hook:
//   - Name comes from hookName(h) (Name() interface or reflect fallback).
//   - Kind defaults to "Pre" for Pre-slice hooks and "Post" for Post-
//     slice hooks. If a hook implements Describer, the hook's reported
//     kind replaces the default (e.g., a Pre+Post hook can return
//     "Pre,Post" once even though it appears in both slices — the
//     decision to de-duplicate is left to the caller in Phase 8;
//     Wave 0 keeps both rows visible for diagnostic clarity).
//   - Enabled is true: hooks present in Chain after Filter are
//     by-definition allowed by ENABLED_HOOKS. The per-hook internal
//     enabled flag (e.g., PIIRedactionHook.Enabled) lives in Config,
//     so the wire stays honest about the two-knob model: ENABLED_HOOKS
//     controls presence; per-hook flags control work-doing.
//   - Config is whatever the hook reports via Describer (nil if the
//     hook doesn't implement Describer).
func (c Chain) Describe() (pre, post []HookDescription) {
	pre = make([]HookDescription, 0, len(c.Pre))
	for _, h := range c.Pre {
		pre = append(pre, describe(h, "Pre"))
	}
	post = make([]HookDescription, 0, len(c.Post))
	for _, h := range c.Post {
		post = append(post, describe(h, "Post"))
	}
	return pre, post
}

// describe builds one HookDescription row. The default kind is the
// caller's slice-implied kind; a Describer hook overrides it.
func describe(h any, defaultKind string) HookDescription {
	desc := HookDescription{
		Name:    hookName(h),
		Kind:    defaultKind,
		Enabled: true,
		Config:  nil,
	}
	if d, ok := h.(Describer); ok {
		if kind, cfg := d.Describe(); kind != "" {
			desc.Kind = kind
			desc.Config = cfg
		} else {
			desc.Config = cfg
		}
	}
	return desc
}

// hookName extracts the hook's declared name. Preference order:
//  1. Explicit Name() string method (preferred — caller-stable API).
//  2. Reflect-based type name: for a *plugin.RequestIDHook value,
//     returns "RequestIDHook".
//
// Returns "" when neither path produces a name (e.g., anonymous
// struct). Filter skips empty-name hooks; Describe still emits a row
// with Name == "".
func hookName(h any) string {
	if n, ok := h.(namer); ok {
		return n.Name()
	}
	t := reflect.TypeOf(h)
	if t == nil {
		return ""
	}
	// Pointer-to-struct is the dominant shape (hooks are usually
	// pointer receivers so methods can mutate Logger fields, etc.).
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
