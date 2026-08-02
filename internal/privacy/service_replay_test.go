package privacy

import (
	"context"
	"sync"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestServiceValidatedReplay_RequiresSuccessfulStrictInbound(t *testing.T) {
	t.Run("adapter-marked caller intent with canonical stream false", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "replay-adapter-mark"})
		ctx := WithRequestState(context.Background(), state)
		MarkStreamRequested(ctx, true)
		req := &canonical.ChatRequest{Stream: false}
		if ValidatedReplayRequired(ctx) {
			t.Fatal("transport intent authorized replay before privacy validation")
		}
		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("Before: %v", err)
		}
		if !ValidatedReplayRequired(ctx) {
			t.Fatal("successful strict inbound did not authorize replay")
		}
		if req.Stream {
			t.Fatal("strict canonical request retained stream=true")
		}
		if err := service.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil): %v", err)
		}
	})

	t.Run("ordinary canonical stream intent uses same outcome", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "replay-canonical-stream"})
		ctx := WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{Stream: true}
		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("Before: %v", err)
		}
		if !ValidatedReplayRequired(ctx) {
			t.Fatal("ordinary strict stream did not authorize validated replay")
		}
		if req.Stream {
			t.Fatal("strict canonical stream was not disabled")
		}
		if err := service.After(ctx, req, nil); err != nil {
			t.Fatalf("After(nil): %v", err)
		}
	})

	for _, mode := range []Action{ActionReplace, ActionEncrypt} {
		t.Run("standard "+string(mode)+" stays aggregated", func(t *testing.T) {
			var encryptKey []byte
			if mode == ActionEncrypt {
				encryptKey = mustEncryptionKey(t, "validated-replay-standard")
			}
			service := newStandardTestService(t, mode, encryptKey)
			state := NewRequestState(RequestMetadata{RequestedProfile: "standard"})
			ctx := WithRequestState(context.Background(), state)
			MarkStreamRequested(ctx, true)
			req := &canonical.ChatRequest{Stream: false}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			if ValidatedReplayRequired(ctx) {
				t.Fatal("standard profile authorized replay")
			}
			if err := service.After(ctx, req, &canonical.ChatResponse{}); err != nil {
				t.Fatalf("After: %v", err)
			}
			receipt, _ := decodeStateReceipt(t, state)
			if receipt.Profile != ProfileStandard || receipt.Coverage != "input" || receipt.Result != "pass" {
				t.Fatalf("standard aggregated receipt=%+v", receipt)
			}
		})
	}
}

func TestServiceValidatedReplay_DeniesMissingUnknownBlockedAndPanickingInbound(t *testing.T) {
	MarkStreamRequested(context.Background(), true)
	if ValidatedReplayRequired(context.Background()) {
		t.Fatal("missing request state authorized replay")
	}

	t.Run("unknown profile", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{})
		state := NewRequestState(RequestMetadata{RequestedProfile: "maximum"})
		ctx := WithRequestState(context.Background(), state)
		MarkStreamRequested(ctx, true)
		_, err := service.Before(ctx, &canonical.ChatRequest{})
		assertPrivacyError(t, err, CodeProfileUnavailable, "profile")
		if ValidatedReplayRequired(ctx) {
			t.Fatal("unknown profile authorized replay")
		}
	})

	t.Run("strict input block", func(t *testing.T) {
		classifier := &residualPassClassifier{
			first: func(string) []Finding { return nil },
			residual: func(value string) []Finding {
				return exactFinding(value, "blocked-input", "PERSON", CategoryPersonal, MatchNER)
			},
		}
		service := newStrictTestService(t, strictTestConfig{classifier: classifier})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "replay-input-block"})
		ctx := WithRequestState(context.Background(), state)
		MarkStreamRequested(ctx, true)
		_, err := service.Before(ctx, &canonical.ChatRequest{System: "blocked-input"})
		assertPrivacyError(t, err, CodeInputBlocked, "input")
		if ValidatedReplayRequired(ctx) {
			t.Fatal("blocked strict input authorized replay")
		}
	})

	t.Run("strict input panic", func(t *testing.T) {
		service := newStrictTestService(t, strictTestConfig{
			classifier: panickingPrivacyClassifier{payload: "replay-panic-detail"},
		})
		state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "replay-input-panic"})
		ctx := WithRequestState(context.Background(), state)
		MarkStreamRequested(ctx, true)
		_, err := service.Before(ctx, &canonical.ChatRequest{System: "panic"})
		assertPrivacyError(t, err, CodeInternalError, "input")
		if ValidatedReplayRequired(ctx) {
			t.Fatal("panicking strict input authorized replay")
		}
	})
}

func TestServiceValidatedReplay_StateAccessIsRaceSafe(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "replay-race-safe"})
	ctx := WithRequestState(context.Background(), state)
	var group sync.WaitGroup
	for range 100 {
		group.Add(2)
		go func() {
			defer group.Done()
			MarkStreamRequested(ctx, true)
		}()
		go func() {
			defer group.Done()
			_ = ValidatedReplayRequired(ctx)
		}()
	}
	group.Wait()
	if ValidatedReplayRequired(ctx) {
		t.Fatal("transport intent alone authorized replay")
	}
	req := &canonical.ChatRequest{}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if !ValidatedReplayRequired(ctx) {
		t.Fatal("successful strict validation did not publish replay outcome")
	}
	if err := service.After(ctx, req, nil); err != nil {
		t.Fatalf("After(nil): %v", err)
	}
}
