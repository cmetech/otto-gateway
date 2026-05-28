// Phase 8 Plan 08-04 Task 1 — Wave 0 scaffold for pii.WalkStrings.
//
// These tests exercise the string-LEAVES-only walker contract:
//   - Never-panics property under testing/quick MaxCount=1000 with random
//     nested map[string]any / []any shapes (T-8-WALK-PANIC mitigation).
//   - Idempotent: WalkStrings(WalkStrings(x)) deep-equal WalkStrings(x)
//     when the transform is itself idempotent.
//   - Map KEYS preserved verbatim (D-03 + RESEARCH Pitfall 2); non-string
//     leaves (numbers, bools, nil) pass through unchanged.
//   - Depth bounded at 64 (RESEARCH Example 4 line 941) — deeper trees
//     pass through unchanged at the bound.
//   - Map-key invariance property (testing/quick): a key under transform
//     remains the key, never gets transformed into the value-side string
//     space.
//
// All 5 tests must fail with `undefined: WalkStrings` before Task 2
// implements walk.go.
package pii

import (
	"reflect"
	"testing"
	"testing/quick"
)

// TestWalkStrings_NeverPanics is the T-8-WALK-PANIC property: random
// nested shapes (strings, ints, bools, nils, maps, slices) must never
// crash the walker. Mirrors internal/engine/pickcwd_test.go:208's
// testing/quick pattern.
func TestWalkStrings_NeverPanics(t *testing.T) {
	property := func(seed int64, depth uint8, leafCount uint8) bool {
		v := randomShape(seed, int(depth%6), int(leafCount%8))
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("WalkStrings panicked on shape %+v: %v", v, r)
			}
		}()
		_ = WalkStrings(v, func(s string) string { return s + "_walked" })
		return true
	}
	cfg := &quick.Config{MaxCount: 1000}
	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("WalkStrings never-panics property failed: %v", err)
	}
}

// TestWalkStrings_Idempotent asserts that applying the same idempotent
// transform twice gives the same result. The redact closure here returns
// the literal "<E>" for any input so a second pass is a no-op.
func TestWalkStrings_Idempotent(t *testing.T) {
	input := map[string]any{
		"a": "user@x.com",
		"b": []any{"corey@cmetech.io", float64(1)},
	}
	redact := func(string) string { return "<E>" }
	once := WalkStrings(input, redact)
	twice := WalkStrings(once, redact)
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("WalkStrings not idempotent:\nonce=%+v\ntwice=%+v", once, twice)
	}
}

// TestWalkStrings_KeysAndNonStringLeavesPreserved asserts the D-03 / Pitfall 2
// invariant: map keys are NEVER transformed (they are protocol field
// names), and non-string leaves (float64, bool, nil) are bit-identical
// input vs output. Only the string value at "email_address" is
// transformed.
func TestWalkStrings_KeysAndNonStringLeavesPreserved(t *testing.T) {
	input := map[string]any{
		"email_address": "leak@x.com",
		"count":         float64(42),
		"ok":            true,
		"empty":         nil,
	}
	out := WalkStrings(input, func(s string) string { return "<REDACTED>" })
	got, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("WalkStrings returned non-map for map input: %T", out)
	}
	// Key still present.
	if _, present := got["email_address"]; !present {
		t.Errorf("map key 'email_address' was transformed or dropped; got keys %v", keysOf(got))
	}
	// String value transformed.
	if got["email_address"] != "<REDACTED>" {
		t.Errorf("email_address value: got %v, want <REDACTED>", got["email_address"])
	}
	// Non-string leaves untouched.
	if got["count"] != float64(42) {
		t.Errorf("count: got %v, want 42 (float64)", got["count"])
	}
	if got["ok"] != true {
		t.Errorf("ok: got %v, want true", got["ok"])
	}
	if got["empty"] != nil {
		t.Errorf("empty: got %v, want nil", got["empty"])
	}
}

// TestWalkStrings_DepthBounded constructs a 70-level deep nested map and
// asserts the walker returns without panic AND the deepest string leaf
// is NOT transformed (depth > 64 returns input unchanged per RESEARCH
// Example 4 line 944-946).
func TestWalkStrings_DepthBounded(t *testing.T) {
	const deep = 70
	// Build the innermost leaf, then wrap.
	var v any = "deep-leaf@x.com"
	for i := 0; i < deep; i++ {
		v = map[string]any{"k": v}
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WalkStrings panicked on 70-deep tree: %v", r)
		}
	}()
	out := WalkStrings(v, func(string) string { return "<REPLACED>" })
	// Navigate down to the leaf and assert it is unchanged (since depth
	// exceeded the bound, the subtree was returned verbatim somewhere
	// along the way).
	cur := out
	for i := 0; i < deep; i++ {
		m, ok := cur.(map[string]any)
		if !ok {
			// Reached a non-map before exhausting depth — that's OK; the
			// bound short-circuited.
			break
		}
		cur = m["k"]
	}
	// At least one level beyond the bound retained its original string.
	if cur == "<REPLACED>" {
		t.Errorf("depth bound failed: leaf at depth %d was transformed; expected pass-through", deep)
	}
}

// TestWalkStrings_MapKeyInvariance_Property is a testing/quick property:
// for any random string key s, walking map[string]any{s: "leak@x.com"}
// with a redacting transform must NOT change the key. MaxCount=500.
func TestWalkStrings_MapKeyInvariance_Property(t *testing.T) {
	property := func(s string) bool {
		input := map[string]any{s: "leak@x.com"}
		out := WalkStrings(input, func(string) string { return "X" })
		m, ok := out.(map[string]any)
		if !ok {
			return false
		}
		_, present := m[s]
		return present && len(m) == 1
	}
	cfg := &quick.Config{MaxCount: 500}
	if err := quick.Check(property, cfg); err != nil {
		t.Errorf("map-key-invariance property failed: %v", err)
	}
}

// randomShape builds a small random nested structure of strings, ints,
// bools, nils, maps, and slices for the never-panics property test.
// Deterministic given (seed, depth, breadth).
func randomShape(seed int64, depth, breadth int) any {
	if depth <= 0 || breadth <= 0 {
		switch seed % 4 {
		case 0:
			return "leaf"
		case 1:
			return float64(seed)
		case 2:
			return seed%2 == 0
		default:
			return nil
		}
	}
	if seed%2 == 0 {
		m := make(map[string]any, breadth)
		for i := 0; i < breadth; i++ {
			k := stringOf(i, seed)
			m[k] = randomShape(seed*31+int64(i), depth-1, breadth-1)
		}
		return m
	}
	sl := make([]any, 0, breadth)
	for i := 0; i < breadth; i++ {
		sl = append(sl, randomShape(seed*17+int64(i), depth-1, breadth-1))
	}
	return sl
}

// stringOf turns (i, seed) into a deterministic short string. Used as
// random map keys in randomShape.
func stringOf(i int, seed int64) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 0, 4)
	x := uint64(i) ^ uint64(seed)
	for j := 0; j < 4; j++ {
		out = append(out, alphabet[x%26])
		x /= 26
	}
	return string(out)
}

// keysOf returns the keys of m, for error messages.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
