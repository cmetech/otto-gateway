package privacy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"otto-gateway/internal/canonical"
)

type standardEmailClassifier struct{}

func (standardEmailClassifier) Classify(_ string, value string) []Finding {
	const email = "corey@example.com"
	start := strings.Index(value, email)
	if start < 0 {
		return nil
	}
	return []Finding{{
		Entity:   "Email",
		Category: CategoryPersonal,
		Kind:     MatchValidatedRegex,
		Start:    start,
		End:      start + len(email),
	}}
}

func TestServiceStandardInboundReceiptIsInputCoverageAndValueFree(t *testing.T) {
	service := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "run-standard", Surface: "openai", Workload: "audit"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{
		Stream: true,
		Messages: []canonical.Message{{Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "contact corey@example.com",
		}}}},
	}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	receipt, payload := decodeStateReceipt(t, state)
	if receipt != (Receipt{
		Version: 1, Profile: ProfileStandard, Scope: "run-standard",
		Coverage: "input", Result: "pass", Transformed: 1,
	}) {
		t.Fatalf("receipt: got %#v", receipt)
	}
	for _, forbidden := range []string{"corey@example.com", "Email", "entity", "map"} {
		if strings.Contains(payload, forbidden) {
			t.Errorf("receipt contains forbidden %q: %s", forbidden, payload)
		}
	}
}

func TestServiceStandardAggregatedOutputUpgradesReceiptToFull(t *testing.T) {
	service := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "aggregate"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Stream: false}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := service.After(ctx, req, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("After: %v", err)
	}
	receipt, _ := decodeStateReceipt(t, state)
	if receipt.Coverage != "full" || receipt.Profile != ProfileStandard || receipt.Result != "pass" {
		t.Fatalf("receipt: got %#v", receipt)
	}
}

// This simulates engine collection after an earlier Pre hook short-circuits:
// the adapter stamped state, this Service's Before was skipped, and its After
// still receives the short-circuit response.
func TestServiceStandardEarlierPreHookShortCircuitDoesNotIssueReceipt(t *testing.T) {
	service := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "short-circuit"})
	ctx := WithRequestState(context.Background(), state)

	if err := service.After(ctx, &canonical.ChatRequest{Stream: false}, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("After: %v", err)
	}
	if got := state.receiptValue(); got != "" {
		t.Fatalf("After without this Service's Before issued a false receipt: %q", got)
	}
}

func TestServiceStandardInboundPassIsBoundToServiceInstance(t *testing.T) {
	first := newStandardTestService(t, ActionReplace, nil)
	second := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "service-bound"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Stream: false}

	if _, err := first.Before(ctx, req); err != nil {
		t.Fatalf("first Before: %v", err)
	}
	if err := second.After(ctx, req, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("second After: %v", err)
	}
	receipt, _ := decodeStateReceipt(t, state)
	if receipt.Coverage != "input" {
		t.Fatalf("different Service upgraded receipt: %#v", receipt)
	}

	if err := first.After(ctx, req, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("first After: %v", err)
	}
	receipt, _ = decodeStateReceipt(t, state)
	if receipt.Coverage != "full" || receipt.Result != "pass" {
		t.Fatalf("matching Service did not upgrade receipt: %#v", receipt)
	}
}

func TestServiceStandardInboundPassLifecycleIsRaceSafe(t *testing.T) {
	service := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "parallel-after"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Stream: false}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}

	const workers = 32
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			errs <- service.After(ctx, req, &canonical.ChatResponse{})
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("After: %v", err)
		}
	}
	receipt, _ := decodeStateReceipt(t, state)
	if receipt.Coverage != "full" || receipt.Result != "pass" {
		t.Fatalf("receipt: %#v", receipt)
	}
}

func TestServiceStandardTrueStreamingOutputKeepsInputCoverage(t *testing.T) {
	service := newStandardTestService(t, ActionReplace, nil)
	state := NewRequestState(RequestMetadata{ScopeID: "stream"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Stream: true}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if err := service.After(ctx, req, &canonical.ChatResponse{}); err != nil {
		t.Fatalf("After: %v", err)
	}
	receipt, _ := decodeStateReceipt(t, state)
	if receipt.Coverage != "input" {
		t.Fatalf("receipt: got %#v", receipt)
	}
}

func TestServiceStandardEncryptDowngradeRestoresAndReportsFull(t *testing.T) {
	key, err := DeriveEncryptionKey("standard-receipt-key")
	if err != nil {
		t.Fatalf("DeriveEncryptionKey: %v", err)
	}
	service := newStandardTestService(t, ActionEncrypt, key)
	state := NewRequestState(RequestMetadata{ScopeID: "encrypted"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{
		Stream: true,
		Messages: []canonical.Message{{Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "corey@example.com",
		}}}},
	}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if req.Stream {
		t.Fatal("encrypt request did not downgrade streaming")
	}
	token := req.Messages[0].Content[0].Text
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText,
		Text: token,
	}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != "corey@example.com" {
		t.Fatalf("restored: got %q", got)
	}
	receipt, payload := decodeStateReceipt(t, state)
	if receipt.Coverage != "full" || receipt.Transformed != 1 || receipt.Restored != 1 || receipt.Blocked != 0 {
		t.Fatalf("receipt: got %#v", receipt)
	}
	if strings.Contains(payload, "corey@example.com") || strings.Contains(payload, token) {
		t.Fatalf("receipt leaked value/token: %s", payload)
	}
}

func newStandardTestService(t *testing.T, mode Action, encryptKey []byte) *Service {
	t.Helper()
	service, err := NewService(Config{
		DefaultProfile:   ProfileStandard,
		RequestProfiles:  []Profile{ProfileStandard},
		PIIEnabled:       true,
		PIIMode:          mode,
		PIIEncryptKey:    encryptKey,
		Recognizers:      []string{"Email"},
		Classifier:       standardEmailClassifier{},
		PIIEntityActions: map[string]Action{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func decodeStateReceipt(t *testing.T, state *RequestState) (Receipt, string) {
	t.Helper()
	encoded := state.receiptValue()
	if encoded == "" {
		t.Fatal("missing encoded receipt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return receipt, string(payload)
}
