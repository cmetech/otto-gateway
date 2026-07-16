// Package engine — CoerceToolCall (D-01) + pickBestTool (D-10) +
// stripFences (D-10) — the load-bearing LangChain-compat behavior
// preserved from the Node reference (`docs/reference/acp_server_node_reference.md`
// §"Load-bearing weirdness: `coerceToolCall`" lines 166-195).
//
// Why this exists: LangChain agents emit tool invocations as plain JSON
// in the assistant's text content (e.g. `{"location":"Boston"}`) rather
// than as a native tool_calls payload, because not every backend
// surfaces a structured tool-call channel. `coerceToolCall` rescues
// those LangChain agents by parsing the assistant text, scoring each
// declared tool against the parsed JSON's keys, and synthesizing a
// real tool_call entry that the client SDK will surface as a tool
// invocation. Without this, LangFlow chains that emit `{"city":"London"}`
// as plain text silently lose their tool calls.
//
// Per-surface contract (Phase 6 D-03/D-05/D-07 — see also collect.go's
// commentary on Message.ToolCalls population):
//   - Ollama and OpenAI surfaces invoke CoerceToolCall immediately
//     after engine.Collect, between the canonical aggregation and the
//     per-surface render. Returns bool so adapters can debug-log a
//     `coerce=true` tag (Node's access log does this).
//   - Anthropic surface does NOT invoke CoerceToolCall. Its native
//     `tool_use` block path is wire-fluent and Anthropic-native clients
//     (`@anthropic-ai/sdk` / loop24-client) do not emit JSON-as-text
//     the way LangChain does on the Ollama path. Running coerce on
//     Anthropic would silently rewrite assistant text into `tool_use`
//     blocks, surprising the client. Anthropic populates
//     Message.ToolCalls via its adapter-local Collect (06-04 Option A1)
//     from kiro-native ChunkKindToolCall chunks.
//
// Interaction with engine.Collect (updated Defect 1a, 2026-07-16): kiro-native
// ChunkKindToolCall chunks are now surfaced structurally onto
// Message.ToolCalls by engine.Collect (no longer as `[tool: <name>]`
// narration text). The idempotency guard in Step 1 (len(ToolCalls) > 0 →
// return false) means CoerceToolCall no-ops on a kiro-native turn, so the
// native call is never double-counted. As a defensive belt-and-braces, the
// algorithm also naturally leaves any stray `[tool: …]`-shaped text alone —
// it is neither `{`- nor fence-prefixed, so Steps 3/4 fail and Step 5
// returns false. That defensive property is locked by
// `TestCoerceToolCall_AlgorithmCases/kiro_native_narration_text_no_coerce`
// in coerce_test.go.
package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"otto-gateway/internal/canonical"
)

// CoerceToolCall (D-01 + D-09 — the locked 9-step bare-{args} algorithm,
// with a Track 3b wrapper-tier fast path that runs first):
//
//  1. Defensive nil guards + skip:
//     - req == nil OR resp == nil → return false.
//     - len(req.Tools) == 0 → return false.
//     - len(resp.Message.ToolCalls) > 0 → return false (D-02 idempotency:
//     a non-empty ToolCalls short-circuits a re-invocation no-op).
//  2. Extract assistant text: locate the single ContentKindText part in
//     resp.Message.Content. If absent or empty (after TrimSpace), return
//     false. The text-part index is remembered so later steps can clear it.
//     2a. Track 3b wrapper tier (runs BEFORE the bare-{args} path below):
//     try ExtractToolCallWrappers(rawText, req.Tools) — the explicit
//     {"tool_call":{"name","arguments"}} wrapper is unambiguous, so it
//     takes precedence over the key-overlap heuristic. If it returns one
//     or more calls, clear Content[textIdx].Text, set
//     resp.Message.ToolCalls to the extracted calls, and return true
//     immediately — steps 3-9 below do NOT run. If it returns zero
//     calls, fall through to Step 3.
//  3. Try json.Unmarshal on the raw text.
//  4. If fail, run stripFences to strip ```json or bare ``` fences (the
//     fence MUST wrap the ENTIRE text — Pitfall 3 "entire text"
//     requirement). If stripFences returns (text, false) (no fence),
//     return false. If stripFences succeeds, retry json.Unmarshal on
//     the stripped text.
//  5. If still fail after fence-strip → return false. Text preserved
//     verbatim — never mutate response on parse failure.
//  6. pickBestTool: for each ToolSpec in req.Tools, count how many keys
//     in parsed (when parsed is map[string]any) appear as top-level keys
//     of spec.Parameters["properties"]. Pick the highest scorer; ties
//     broken by first-declared (D-10 — deterministic; first slice
//     index wins).
//  7. If the top score is zero → return false. Text preserved. (Node
//     behavior — better than firing the wrong tool.)
//  8. Replace Content[textIdx].Text with "" and append a synthetic
//     ToolCall{ID: "call_<unix-nano>", Name: bestSpec.Name,
//     Arguments: parsed-as-map} to resp.Message.ToolCalls.
//  9. Return true.
//
// Returns true iff resp was rewritten (Step 9 reached); false otherwise.
// No error return — parse failures are non-fatal per Step 5.
//
// CRITICAL — pointer semantics: this function mutates resp in place.
// Callers MUST use the same pointer after the call. Pre-copying
// (`respCopy := *resp`) before invocation would discard the mutation
// (Pitfall 6).
func CoerceToolCall(req *canonical.ChatRequest, resp *canonical.ChatResponse) bool {
	// Step 1: defensive guards + idempotency short-circuit.
	if req == nil || resp == nil {
		return false
	}
	if len(req.Tools) == 0 {
		return false
	}
	if len(resp.Message.ToolCalls) > 0 {
		return false
	}

	// Step 2: locate the single assistant-text content part. Phase 6's
	// engine.Collect always produces at most one ContentKindText part at
	// index 0, but defensively walk the slice to find the FIRST text
	// part regardless of position.
	textIdx := -1
	for i, part := range resp.Message.Content {
		if part.Kind == canonical.ContentKindText {
			textIdx = i
			break
		}
	}
	if textIdx < 0 {
		return false
	}
	rawText := resp.Message.Content[textIdx].Text
	if strings.TrimSpace(rawText) == "" {
		return false
	}

	// Track 3b: try the explicit {"tool_call":…} wrapper first (unambiguous).
	// On miss, fall through to the bare-{args} key-overlap heuristic below.
	if wrappers := ExtractToolCallWrappers(rawText, req.Tools); len(wrappers) > 0 {
		resp.Message.Content[textIdx].Text = ""
		resp.Message.ToolCalls = wrappers
		return true
	}

	// Step 3: try raw json.Unmarshal.
	var parsed any
	if err := json.Unmarshal([]byte(rawText), &parsed); err != nil {
		// Step 4: retry after fence-strip. If no fence is present
		// (HasPrefix/HasSuffix chains both fail), bail per Step 5.
		stripped, ok := StripFences(rawText)
		if !ok {
			return false
		}
		if err := json.Unmarshal([]byte(stripped), &parsed); err != nil {
			// Step 5: still fail → text preserved verbatim.
			return false
		}
	}

	// D-10: only object parses are candidates. Arrays, scalars, nulls
	// have no overlap with a tool's properties (which is a top-level
	// keys map).
	parsedMap, isMap := parsed.(map[string]any)
	if !isMap {
		return false
	}

	// Step 6: pick the best-scoring tool. Tie-break is first-declared
	// (D-10) — pickBestTool iterates req.Tools by slice index, NOT map
	// iteration, so ordering is deterministic.
	bestSpec, bestScore := pickBestTool(parsedMap, req.Tools)

	// Step 7: zero score → no-coerce (Node parity — better than firing
	// the wrong tool).
	if bestScore == 0 || bestSpec == nil {
		return false
	}

	// Step 8: rewrite response. The synthesized ID uses `call_<unix-nano>`
	// per D-11 (mirrors OpenAI's `call_` convention and the Phase 2
	// `chatcmpl-<unix-nano>` pattern). Opaque to clients; no cryptographic
	// claim — see T-06-03 in 06-01-PLAN.md threat register.
	resp.Message.Content[textIdx].Text = ""
	resp.Message.ToolCalls = append(resp.Message.ToolCalls, canonical.ToolCall{
		ID:        fmt.Sprintf("call_%d", time.Now().UnixNano()),
		Name:      bestSpec.Name,
		Arguments: parsedMap,
	})

	// Step 9: rewritten.
	return true
}

// pickBestTool scores each spec by the count of keys in parsed that
// appear as top-level keys of spec.Parameters["properties"]. Returns
// (best, score). On all-zero scores returns (nil, 0). Tie-break is
// first-declared (D-10) — iteration is by slice index, so the first
// spec with the highest score wins.
//
// Locked rules (D-10):
//   - Iterate tools in declaration order (slice index, not map iteration).
//   - For each spec: extract spec.Parameters["properties"] as
//     map[string]any. If absent / not a map / empty, skip this spec
//     (zero score; can't compare against an unknown shape).
//   - Score = count of keys in parsed that appear as top-level keys of
//     properties. TOP-LEVEL ONLY — no recursion (D-10).
//   - On strict `>` update bestIdx/bestScore. On equality (`==`), DO
//     NOT update — first-declared wins.
func pickBestTool(parsed map[string]any, tools []canonical.ToolSpec) (*canonical.ToolSpec, int) {
	bestIdx := -1
	bestScore := 0
	for i := range tools {
		spec := &tools[i]
		props := extractProperties(spec)
		if len(props) == 0 {
			continue
		}
		score := 0
		for key := range parsed {
			if _, ok := props[key]; ok {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil, 0
	}
	return &tools[bestIdx], bestScore
}

// extractProperties pulls spec.Parameters["properties"] as map[string]any,
// or returns nil if absent / wrong shape. Defensive against arbitrary
// caller-supplied Parameters maps.
func extractProperties(spec *canonical.ToolSpec) map[string]any {
	if spec == nil || spec.Parameters == nil {
		return nil
	}
	raw, ok := spec.Parameters["properties"]
	if !ok {
		return nil
	}
	props, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return props
}

// StripFences removes a single ```json ... ``` (or bare ``` ... ```) wrap
// when it brackets the ENTIRE trimmed input. Returns (stripped, true) on
// strip, (input, false) otherwise. Conservative: inline fences inside
// prose are preserved. Shared with the Ollama JSON-format render path
// (Phase 08.2) — do not loosen the whole-body match without auditing
// both callers.
//
// Locked rules (D-10):
//   - After strings.TrimSpace, check HasPrefix("```json\n") + HasSuffix("\n```")
//     first; then HasPrefix("```\n") + HasSuffix("\n```").
//   - Both prefix AND suffix must match — the fence MUST wrap the
//     ENTIRE text (Pitfall 3 "entire text" requirement — no inline
//     fenced JSON inside prose).
//   - Tolerate CRLF by normalizing "\r\n" → "\n" BEFORE the
//     prefix/suffix check.
//   - NO regex (Pitfall 3 catastrophic-backtracking safety + Don't-
//     Hand-Roll guidance). Two HasPrefix/HasSuffix chains with no
//     nested quantifiers — linear time guaranteed.
//
// Returns (text-trimmed, false) when no fence is detected so the
// caller can short-circuit; the trimmed text is informational only on
// a `false` return (caller should NOT retry parse with it).
func StripFences(text string) (string, bool) {
	t := strings.ReplaceAll(text, "\r\n", "\n")
	t = strings.TrimSpace(t)
	const suffix = "\n```"
	if strings.HasPrefix(t, "```json\n") && strings.HasSuffix(t, suffix) {
		inner := t[len("```json\n") : len(t)-len(suffix)]
		return inner, true
	}
	if strings.HasPrefix(t, "```\n") && strings.HasSuffix(t, suffix) {
		inner := t[len("```\n") : len(t)-len(suffix)]
		return inner, true
	}
	return t, false
}

// ExtractToolCallWrappers finds every explicit {"tool_call":{"name","arguments"}}
// object in text and returns canonical.ToolCalls, with invented-name remap
// against tools. Unambiguous (wrapper shape only) — safe on every surface incl.
// Anthropic. Returns nil when none found. Ports acp-server-ollama.js:1139-1200.
func ExtractToolCallWrappers(text string, tools []canonical.ToolSpec) []canonical.ToolCall {
	// seq is a per-invocation counter appended to each minted ID so that
	// two (or more) wrappers extracted within the SAME call get distinct
	// IDs even when time.Now().UnixNano() returns the same value for both
	// (a coarse clock can do this when multiple calls are extracted back
	// to back). Incremented once per successfully pushed wrapper.
	seq := 0

	// toolDeclared checks if any tool in tools has the given name.
	toolDeclared := func(name string, tools []canonical.ToolSpec) bool {
		for _, t := range tools {
			if t.Name == name {
				return true
			}
		}
		return false
	}

	// pushWrapper tries to extract and validate a {"tool_call":{...}} wrapper
	// from a map. If successful, remaps invented names via pickBestTool and
	// returns (canonical.ToolCall, true). On any failure, returns (_, false).
	pushWrapper := func(m map[string]any) (canonical.ToolCall, bool) {
		// Extract the "tool_call" key as a map.
		tc, ok := m["tool_call"].(map[string]any)
		if !ok {
			return canonical.ToolCall{}, false
		}

		// Extract the name field (required, non-empty).
		name, _ := tc["name"].(string)
		if name == "" {
			return canonical.ToolCall{}, false
		}

		// Extract arguments, handling three cases:
		// 1. arguments is a map[string]any → use directly.
		// 2. arguments is a string → json.Unmarshal it.
		// 3. arguments is missing/nil → use empty map.
		var args map[string]any
		switch a := tc["arguments"].(type) {
		case map[string]any:
			args = a
		case string:
			if a != "" {
				if err := json.Unmarshal([]byte(a), &args); err != nil {
					args = map[string]any{}
				}
			} else {
				args = map[string]any{}
			}
		default:
			args = map[string]any{}
		}
		if args == nil {
			args = map[string]any{}
		}

		// Invented-name remap: if the name is not in tools, try pickBestTool.
		if !toolDeclared(name, tools) {
			best, score := pickBestTool(args, tools)
			if best == nil || score == 0 {
				return canonical.ToolCall{}, false
			}
			name = best.Name
		}

		// Create and return the ToolCall. The ID incorporates seq (this
		// invocation's push index) so that multiple wrappers extracted in
		// one ExtractToolCallWrappers call never collide on ID even if
		// UnixNano() ticks are identical.
		id := fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), seq)
		seq++
		return canonical.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: args,
		}, true
	}

	// Strategy 1: try whole-content parse (raw or after fence-strip).
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		// Retry after fence-strip.
		if stripped, ok := StripFences(text); ok {
			_ = json.Unmarshal([]byte(stripped), &v)
		}
	}

	var candidates []map[string]any

	// If v is a single map, collect it.
	if m, ok := v.(map[string]any); ok {
		candidates = append(candidates, m)
	}
	// If v is an array, collect each element that is a map.
	if arr, ok := v.([]any); ok {
		for _, elem := range arr {
			if m, ok := elem.(map[string]any); ok {
				candidates = append(candidates, m)
			}
		}
	}

	// If Strategy 1 found candidates, try pushWrapper on them.
	if len(candidates) > 0 {
		var out []canonical.ToolCall
		for _, m := range candidates {
			if tc, ok := pushWrapper(m); ok {
				out = append(out, tc)
			}
		}
		if len(out) > 0 {
			return out
		}
	}

	// Strategy 2: use extractToolCallObjects if Strategy 1 yielded zero.
	var out []canonical.ToolCall
	for _, m := range extractToolCallObjects(text) {
		if tc, ok := pushWrapper(m); ok {
			out = append(out, tc)
		}
	}
	if len(out) > 0 {
		return out
	}

	return nil
}

// repairControlChars escapes raw control characters inside JSON string
// values. Iterates over bytes (not runes) to handle multi-byte UTF-8
// verbatim. When inside a quoted string (tracking inStr/esc), replaces
// raw \n → \\n, \r → \\r, \t → \\t. Outside strings, copies verbatim.
// This matches JS semantics for ASCII control chars in JSON repair.
func repairControlChars(s string) string {
	var out strings.Builder
	b := []byte(s)
	inStr := false
	esc := false

	for _, ch := range b {
		if inStr {
			if esc {
				out.WriteByte(ch)
				esc = false
			} else if ch == '\\' {
				out.WriteByte(ch)
				esc = true
			} else if ch == '"' {
				out.WriteByte(ch)
				inStr = false
			} else if ch == '\n' {
				out.WriteString("\\n")
			} else if ch == '\r' {
				out.WriteString("\\r")
			} else if ch == '\t' {
				out.WriteString("\\t")
			} else {
				out.WriteByte(ch)
			}
		} else {
			if ch == '"' {
				inStr = true
			}
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// Budgets for extractToolCallObjects. The single forward pass is already
// O(n); these are belt-and-suspenders caps that keep work bounded on
// pathological, untrusted, model-generated input (kiro's output is NOT
// capped by http.MaxBytesReader — that only bounds the request body).
const (
	// maxScanBytes bounds total scan work regardless of input length. A
	// legitimate kiro {"tool_call":…} wrapper is at most a few hundred
	// bytes; anything past 1 MiB is not a wrapper. We scan only the first
	// 1 MiB so the pass is O(min(len, 1 MiB)) — never O(n) on a 5 MB blob,
	// and never quadratic.
	maxScanBytes = 1 << 20 // 1 MiB
	// maxToolCallCandidates caps how many objects we extract, so an input
	// packed with thousands of tiny wrappers cannot blow up output size or
	// allocation count. 32 is far beyond any real multi-tool response.
	maxToolCallCandidates = 32
	// maxNestDepth bounds the open-object stack. Real wrappers nest only a
	// handful of levels; a runaway stream of '{' is refused past this so
	// the stack (and work) stay bounded.
	maxNestDepth = 64
	// maxRepairSlice caps the size of a truncation-repaired slice we build
	// and parse, so the repair path can never allocate/parse a multi-MB
	// blob even if a huge object was left unclosed at EOF.
	maxRepairSlice = 64 << 10 // 64 KiB
)

// extractToolCallObjects locates {"tool_call":…} wrapper objects embedded in
// prose and returns them as parsed maps. It is the Strategy 2 fallback of
// ExtractToolCallWrappers (whole-content/array parsing is Strategy 1).
//
// Algorithm — a SINGLE forward pass, O(min(len(text), maxScanBytes)):
//   - Maintain a stack of open-object start indices (parallel `starts` /
//     `marked` slices) with correct string/escape state, so braces and
//     quotes inside string VALUES never change depth.
//   - On '{' (outside a string) push the current index; on '}' pop — the
//     popped [start..here] is a complete object.
//   - When the literal "tool_call" appears as a KEY (a string whose content
//     is exactly tool_call, immediately followed by ':') of the currently
//     open object, mark that enclosing object's stack frame. This binds the
//     key to its TRUE enclosing object — fixing the nested-sibling defect
//     where a prior closed sibling ({"x":1}) misled a backward LastIndex.
//   - When a MARKED object closes, slice text[start:end+1], parse (raw →
//     then repairControlChars fallback), and append if it is a JSON object.
//     For a wrapper nested one level deep, "tool_call" is a direct key of the
//     inner object, so we extract the minimal enclosing object (JS-reference
//     intent) — not the outer wrapper.
//   - Truncation repair: if EOF leaves a marked object unclosed with
//     1 <= depth <= 8 and not inside a string, close it with '}'×depth
//     (trimmed to the last meaningful byte) and retry parse.
//
// Returns parsed objects (map[string]any) or nil if none found.
func extractToolCallObjects(text string) []map[string]any {
	var out []map[string]any

	const toolCallKey = "tool_call" // key NAME (quotes handled by scan state)

	// tryParse attempts to unmarshal s into map[string]any.
	tryParse := func(s string) (map[string]any, bool) {
		var m map[string]any
		err := json.Unmarshal([]byte(s), &m)
		return m, err == nil
	}

	scanLen := len(text)
	if scanLen > maxScanBytes {
		scanLen = maxScanBytes // input-size budget (defect 1)
	}
	// Copy only the scanned prefix — never the whole (possibly multi-MB)
	// input — so the byte copy is bounded by the budget too, not just the
	// scan loop. All indexing below stays within [0, scanLen).
	b := []byte(text[:scanLen])

	// Parallel stack: starts[i] is the byte index of an open object's '{';
	// marked[i] is true once "tool_call" is seen as a direct key of it.
	var starts []int
	var marked []bool

	inStr := false
	esc := false
	strStart := -1       // index just past the opening quote of current string
	lastMeaningful := -1 // last structurally meaningful byte (for repair)

	for j := 0; j < scanLen; j++ {
		ch := b[j]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch ch {
			case '\\':
				esc = true
			case '"':
				inStr = false
				lastMeaningful = j
				// "tool_call" is a KEY iff its content matches exactly and the
				// next non-space byte is ':'. Only mark when inside an object.
				if len(marked) > 0 &&
					bytesEqualString(b[strStart:j], toolCallKey) &&
					nextNonSpaceIsColon(b, j+1, scanLen) {
					marked[len(marked)-1] = true
				}
			}
			continue
		}

		switch ch {
		case '"':
			inStr = true
			strStart = j + 1
		case '{':
			if len(starts) >= maxNestDepth {
				// Nesting budget exceeded — not a real wrapper. Stop scanning;
				// truncation repair below is refused (depth > 8) so we return
				// whatever complete objects we already collected.
				goto done
			}
			starts = append(starts, j)
			marked = append(marked, false)
		case '}':
			if n := len(starts); n > 0 {
				start := starts[n-1]
				isMarked := marked[n-1]
				starts = starts[:n-1]
				marked = marked[:n-1]
				lastMeaningful = j
				if isMarked {
					slice := text[start : j+1]
					if parsed, ok := tryParse(slice); ok {
						out = append(out, parsed)
					} else if parsed, ok := tryParse(repairControlChars(slice)); ok {
						out = append(out, parsed)
					}
					if len(out) >= maxToolCallCandidates {
						return out // candidate budget reached
					}
				}
			}
		case ']':
			lastMeaningful = j
		default:
			if isWordOrDot(ch) {
				lastMeaningful = j
			}
		}
	}

	// Truncation repair (defect-1-safe): close the innermost still-open
	// object that contains a "tool_call" key, if its unclosed depth is
	// 1..8, we are not mid-string, and the repaired slice stays within the
	// per-candidate size cap.
	if !inStr && len(starts) > 0 {
		for i := len(marked) - 1; i >= 0; i-- {
			if !marked[i] {
				continue
			}
			depth := len(starts) - i
			if depth >= 1 && depth <= 8 && lastMeaningful >= starts[i] {
				end := lastMeaningful + 1
				if end-starts[i] <= maxRepairSlice {
					repaired := text[starts[i]:end] + strings.Repeat("}", depth)
					if parsed, ok := tryParse(repaired); ok {
						out = append(out, parsed)
					} else if parsed, ok := tryParse(repairControlChars(repaired)); ok {
						out = append(out, parsed)
					}
				}
			}
			break // only the innermost marked object is repaired
		}
	}

done:
	if len(out) == 0 {
		return nil
	}
	return out
}

// bytesEqualString reports whether b equals s byte-for-byte, without
// allocating (used in the hot scan loop to detect the "tool_call" key).
func bytesEqualString(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

// nextNonSpaceIsColon reports whether the first non-JSON-whitespace byte in
// b[from:limit] is ':'. Used to distinguish a "tool_call" KEY from a string
// VALUE that merely happens to equal tool_call.
func nextNonSpaceIsColon(b []byte, from, limit int) bool {
	for k := from; k < limit; k++ {
		switch b[k] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// isWordOrDot reports whether ch is a word character (a-z, A-Z, 0-9, _)
// or a dot (.). Used in balanced-brace scanning to track lastMeaningful.
func isWordOrDot(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_' ||
		ch == '.'
}
