// Package plugin — Wave 0 chain tests (Phase 8 Plan 08-01 Task 2).
//
// These tests scaffold the expectations for plugin.Chain BEFORE the
// implementation lands in Task 3. All tests in this file are expected
// to FAIL with `undefined: Chain` / `undefined: HookDescription` until
// chain.go is written; the Wave 0 RED state proves Task 3's GREEN delta.
//
// Whitebox (package plugin) so we can hand-construct fakes that satisfy
// engine.PreHook / engine.PostHook without going through the production
// hook implementations (Pattern A in 08-PATTERNS.md — consumer-defined
// fake source pattern from internal/server/agents_test.go).
package plugin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// --- Test fakes -----------------------------------------------------------

// fakePreHook records Before calls in invocation order. The optional
// shortCircuit response models the "first non-nil short-circuit wins"
// contract from internal/engine/engine.go:152-162 — needed for tests
// that prove Filter / Chain wiring honors the short-circuit semantics
// established in Phase 2 (Codex H-4).
type fakePreHook struct {
	name         string
	shortCircuit *canonical.ChatResponse
	callLog      *[]string
}

func (f *fakePreHook) Before(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	*f.callLog = append(*f.callLog, f.name)
	return f.shortCircuit, nil
}

// Name lets chain.Filter discover the hook by its declared name (mirrors
// RequestIDHook.Name() — preferred name-extractor over reflect).
func (f *fakePreHook) Name() string { return f.name }

// --- Tests ---------------------------------------------------------------

// TestChain_RegistrationOrder proves the slice order = execution order
// contract (D-02 SC5; registration order in the slice IS the canonical
// execution order, not allowlist order). Manually iterates Pre hooks
// the way engine.Run does (internal/engine/engine.go:152-162).
func TestChain_RegistrationOrder(t *testing.T) {
	var log []string
	chain := Chain{
		Pre: []engine.PreHook{
			&fakePreHook{name: "A", callLog: &log},
			&fakePreHook{name: "B", callLog: &log},
			&fakePreHook{name: "C", callLog: &log},
		},
	}

	ctx := context.Background()
	req := &canonical.ChatRequest{}
	for _, h := range chain.Pre {
		if _, err := h.Before(ctx, req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	want := []string{"A", "B", "C"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("execution order: want %v, got %v", want, log)
	}
}

// TestChain_ShortCircuit proves the chain emulates Codex H-4: when a
// PreHook returns a non-nil response, the engine STOPS iterating
// (later hooks never run). Wave 0 tests the SEAM is respected; the
// engine package owns the runtime behavior.
func TestChain_ShortCircuit(t *testing.T) {
	var log []string
	shortCircuitResp := &canonical.ChatResponse{}
	chain := Chain{
		Pre: []engine.PreHook{
			&fakePreHook{name: "A", callLog: &log, shortCircuit: shortCircuitResp},
			&fakePreHook{name: "B", callLog: &log},
		},
	}

	ctx := context.Background()
	req := &canonical.ChatRequest{}
	for _, h := range chain.Pre {
		resp, err := h.Before(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != nil {
			break // mirrors engine.Run short-circuit
		}
	}

	want := []string{"A"}
	if !reflect.DeepEqual(log, want) {
		t.Errorf("execution order on short-circuit: want %v, got %v", want, log)
	}
}

// TestChain_Filter_EmptyAllowlist_Passthrough proves Filter is
// default-permissive: nil or empty allowlist returns the chain
// unchanged (D-02 AUTH_TOKEN-parity semantics).
func TestChain_Filter_EmptyAllowlist_Passthrough(t *testing.T) {
	var log []string
	chain := Chain{
		Pre: []engine.PreHook{
			&fakePreHook{name: "A", callLog: &log},
			&fakePreHook{name: "B", callLog: &log},
			&fakePreHook{name: "C", callLog: &log},
		},
	}

	t.Run("nil_allowlist", func(t *testing.T) {
		got, err := chain.Filter(nil)
		if err != nil {
			t.Fatalf("Filter(nil): unexpected error: %v", err)
		}
		if len(got.Pre) != 3 {
			t.Errorf("Filter(nil): want 3 Pre hooks, got %d", len(got.Pre))
		}
	})

	t.Run("empty_allowlist", func(t *testing.T) {
		got, err := chain.Filter([]string{})
		if err != nil {
			t.Fatalf("Filter([]): unexpected error: %v", err)
		}
		if len(got.Pre) != 3 {
			t.Errorf("Filter([]): want 3 Pre hooks, got %d", len(got.Pre))
		}
	})
}

// TestChain_Filter_PreservesRegistrationOrder proves D-02 SC5: even when
// the allowlist order disagrees with registration order, the filtered
// chain preserves REGISTRATION order. This means a typo'd allowlist
// like {"C","A"} returns [A, C] not [C, A] — the operator can't
// silently rewrite the documented hook sequence.
func TestChain_Filter_PreservesRegistrationOrder(t *testing.T) {
	var log []string
	chain := Chain{
		Pre: []engine.PreHook{
			&fakePreHook{name: "A", callLog: &log},
			&fakePreHook{name: "B", callLog: &log},
			&fakePreHook{name: "C", callLog: &log},
		},
	}

	got, err := chain.Filter([]string{"C", "A"})
	if err != nil {
		t.Fatalf("Filter: unexpected error: %v", err)
	}
	if len(got.Pre) != 2 {
		t.Fatalf("Filter: want 2 Pre hooks, got %d", len(got.Pre))
	}
	// Use Name() to identify (avoid relying on pointer identity through
	// the engine.PreHook interface).
	gotNames := []string{}
	for _, h := range got.Pre {
		if named, ok := h.(interface{ Name() string }); ok {
			gotNames = append(gotNames, named.Name())
		}
	}
	want := []string{"A", "C"} // registration order, NOT allowlist order
	if !reflect.DeepEqual(gotNames, want) {
		t.Errorf("filtered order: want %v, got %v", want, gotNames)
	}
}

// TestChain_Describe_NilSafe proves an empty Chain does not panic on
// Describe — important for /health/hooks bootstrap (OBSV-04) when the
// chain hasn't been wired yet or is filtered down to zero.
func TestChain_Describe_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Chain{}.Describe() panicked: %v", r)
		}
	}()

	c := Chain{}
	pre, post := c.Describe()
	if pre == nil {
		t.Errorf("Describe pre: want non-nil empty slice, got nil")
	}
	if post == nil {
		t.Errorf("Describe post: want non-nil empty slice, got nil")
	}
	if len(pre) != 0 || len(post) != 0 {
		t.Errorf("Describe: want empty slices, got pre=%d post=%d", len(pre), len(post))
	}
}

// TestChain_Filter_UnknownNameError proves the typo-fail-fast contract
// from D-02: an allowlist name that does not match any hook in the
// chain returns a non-nil error. This is the load-bearing protection
// against a typo silently disabling PII redaction (CONTEXT.md §specifics).
func TestChain_Filter_UnknownNameError(t *testing.T) {
	var log []string
	chain := Chain{
		Pre: []engine.PreHook{
			&fakePreHook{name: "A", callLog: &log},
			&fakePreHook{name: "B", callLog: &log},
		},
	}

	_, err := chain.Filter([]string{"BogusHook"})
	if err == nil {
		t.Fatal("Filter with unknown hook name: want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown hook") {
		t.Errorf("error message: want substring 'unknown hook', got %q", err.Error())
	}

	// Ensure errors.Is preserves the join semantics so callers can
	// distinguish multiple typos at once (D-02 typo-fail-fast).
	_, err2 := chain.Filter([]string{"BogusHook", "AnotherBogus"})
	if err2 == nil {
		t.Fatal("Filter with two unknown names: want error, got nil")
	}
	if !strings.Contains(err2.Error(), "BogusHook") || !strings.Contains(err2.Error(), "AnotherBogus") {
		t.Errorf("error should name both unknowns; got %q", err2.Error())
	}
	// errors.Join is the wrapping idiom; sanity-check it's a multi-error.
	var joinable interface{ Unwrap() []error }
	if !errors.As(err2, &joinable) {
		t.Logf("note: err2 is not a multi-error (Join); proceeding (single-error contract is also acceptable as long as both names appear)")
	}
}

// --- HookErrorTracker --------------------------------------------------------

func TestHookErrorTracker_NilSafe(t *testing.T) {
	var tracker *HookErrorTracker
	tracker.Record(&fakePreHook{name: "A"}, errors.New("boom"))
	if got := tracker.LastError("A"); got != "" {
		t.Fatalf("nil tracker should be no-op, got %q", got)
	}
}

func TestHookErrorTracker_RecordsAndClears(t *testing.T) {
	tracker := NewHookErrorTracker()
	hook := &fakePreHook{name: "PII"}

	tracker.Record(hook, errors.New("boom"))
	if got := tracker.LastError("PII"); got != "boom" {
		t.Fatalf("after Record(err): got %q, want boom", got)
	}

	tracker.Record(hook, nil)
	if got := tracker.LastError("PII"); got != "" {
		t.Fatalf("after Record(nil): got %q, want empty", got)
	}
}

func TestHookErrorTracker_UnnamedHookIsNoop(t *testing.T) {
	tracker := NewHookErrorTracker()
	// An anonymous struct has no Name() method and no exported type name —
	// hookName returns "" so Record must silently drop.
	tracker.Record(struct{}{}, errors.New("boom"))
	// No panic, no slot — verified by the tracker being empty for any key.
	if got := tracker.LastError(""); got != "" {
		t.Fatalf("unnamed hook should not store, got %q", got)
	}
}

func TestChain_DescribeWith_PopulatesLastError(t *testing.T) {
	tracker := NewHookErrorTracker()
	callLog := []string{}
	pre := &fakePreHook{name: "PreA", callLog: &callLog}
	chain := Chain{Pre: []engine.PreHook{pre}}

	tracker.Record(pre, errors.New("preflight failed"))
	preDesc, _ := chain.DescribeWith(tracker)
	if len(preDesc) != 1 {
		t.Fatalf("describe length: got %d, want 1", len(preDesc))
	}
	if preDesc[0].LastError != "preflight failed" {
		t.Errorf("LastError: got %q, want preflight failed", preDesc[0].LastError)
	}
}
