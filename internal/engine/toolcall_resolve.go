// Package engine — native tool-call resolution + dedup (alias-primary design,
// 2026-07-16).
//
// Grounded in a real kiro-cli capture: when the caller offers tools, kiro
// still reaches for its OWN built-in shell tool and emits a native ACP
// tool_call whose `kind` is "execute" (internal toolName "shell") with args
// `{"command":"…"}`. The gateway denies kiro's local execution, but the
// native tool_call itself carries the command the user asked for. Rather than
// (a) surfacing "execute" — a name the host never offered and can't route —
// or (b) relying on kiro to reformat it as the host tool name (flaky; it
// sometimes gives up), we ALIAS the native name to the caller-offered tool
// (e.g. execute → run_shell) and surface it structurally. Names with no alias
// to an offered tool are dropped so the host never receives an unroutable call
// and the coerce/wrapper fallback path is not clobbered.
package engine

import (
	"encoding/json"
	"sort"
	"strings"

	"otto-gateway/internal/canonical"
)

// ResolveNativeToolName decides how a kiro-native tool call should be surfaced
// given the caller's offered tools and the configured aliases:
//
//   - No tools offered (len(tools)==0): surface as-is (informational — the
//     caller declared no tools, so there is no deny regime and nothing to
//     route to; the native name is all we have).
//   - Tools offered:
//   - name already matches an offered tool → surface under that name.
//   - aliases[name] matches an offered tool → surface under the aliased name.
//   - otherwise → drop (surface=false): a denied built-in the host can't
//     route. Dropping keeps the coerce/wrapper fallback unclobbered.
func ResolveNativeToolName(name string, tools []canonical.ToolSpec, aliases map[string]string) (resolved string, surface bool) {
	if len(tools) == 0 {
		return name, true
	}
	if toolOffered(name, tools) {
		return name, true
	}
	if alias, ok := aliases[name]; ok && toolOffered(alias, tools) {
		return alias, true
	}
	return "", false
}

// toolOffered reports whether a tool with the given name is in the offered set.
func toolOffered(name string, tools []canonical.ToolSpec) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// DedupToolCalls collapses the redundant native tool-call entries kiro emits
// within one turn into the minimal set a client should act on:
//
//  1. Merge by tool-call id: kiro emits a `tool_call_chunk` (id, name, no args)
//     followed by a `tool_call` (same id, name, rawInput args). The chunk lands
//     first with empty args; the full frame supplies them. Same id → one entry,
//     preferring the non-empty args and name.
//  2. Collapse exact duplicates: when a built-in is denied, kiro retries with a
//     FRESH id but an identical (name, args). Running the same command twice is
//     wrong, so identical (name, args) pairs collapse to the first occurrence.
//
// Entries with an empty id are never id-merged (each is distinct at step 1) but
// still participate in the step-2 exact-duplicate collapse. Order is preserved.
func DedupToolCalls(calls []canonical.ToolCall) []canonical.ToolCall {
	// Step 1 — merge by id.
	byID := make(map[string]int, len(calls))
	merged := make([]canonical.ToolCall, 0, len(calls))
	for _, c := range calls {
		if c.ID != "" {
			if idx, ok := byID[c.ID]; ok {
				if len(merged[idx].Arguments) == 0 && len(c.Arguments) > 0 {
					merged[idx].Arguments = c.Arguments
				}
				if merged[idx].Name == "" {
					merged[idx].Name = c.Name
				}
				continue
			}
			byID[c.ID] = len(merged)
		}
		merged = append(merged, c)
	}

	// Step 2 — collapse exact (name, args) duplicates.
	seen := make(map[string]struct{}, len(merged))
	out := make([]canonical.ToolCall, 0, len(merged))
	for _, c := range merged {
		key := c.Name + "\x00" + canonicalArgs(c.Arguments)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// canonicalArgs renders an args map to a stable string for equality checks.
// encoding/json sorts map keys, so two equal maps produce equal bytes; a nil
// map renders as "null". Marshal failure (a pathological non-JSON value) falls
// back to a sorted key list so distinct arg sets still compare distinct.
func canonicalArgs(args map[string]any) string {
	if len(args) == 0 {
		return "null"
	}
	if b, err := json.Marshal(args); err == nil {
		return string(b)
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
