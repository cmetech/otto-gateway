package privacy

import (
	"context"
	"encoding/base64"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"

	"otto-gateway/internal/canonical"
)

var outboundIPv4Pattern = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)

type outboundFixtureClassifier struct{}

func (outboundFixtureClassifier) Classify(_ string, value string) []Finding {
	var findings []Finding
	for _, match := range outboundIPv4Pattern.FindAllStringIndex(value, -1) {
		if _, err := netip.ParseAddr(value[match[0]:match[1]]); err == nil {
			findings = append(findings, Finding{
				Entity: "IPv4", Category: CategoryTechnical, Kind: MatchValidatedRegex,
				Start: match[0], End: match[1],
			})
		}
	}
	for _, target := range []string{"2001:db8::1234"} {
		if start := strings.Index(value, target); start >= 0 {
			findings = append(findings, Finding{
				Entity: "IPv6", Category: CategoryTechnical, Kind: MatchValidatedRegex,
				Start: start, End: start + len(target),
			})
		}
	}
	for _, target := range []string{"corey@example.com", "generated@example.com"} {
		for offset := 0; offset < len(value); {
			index := strings.Index(value[offset:], target)
			if index < 0 {
				break
			}
			start := offset + index
			findings = append(findings, Finding{
				Entity: "Email", Category: CategoryPersonal, Kind: MatchValidatedRegex,
				Start: start, End: start + len(target),
			})
			offset = start + len(target)
		}
	}
	return findings
}

func TestServiceStrict_OutboundResidualScansBeforeSelectiveRestoration(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{
		classifier: outboundFixtureClassifier{},
		piiMode:    ActionEncrypt,
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "outbound-residual-restore"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "10.20.30.40 corey@example.com"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	inputAlias, inputToken := strings.Fields(req.System)[0], strings.Fields(req.System)[1]
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText,
		Text: "answer " + inputAlias + " " + inputToken + " 10.20.30.41 generated@example.com",
	}}}}

	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	got := resp.Message.Content[0].Text
	for _, want := range []string{"10.20.30.40", "corey@example.com", "[EMAIL_1]"} {
		if !strings.Contains(got, want) {
			t.Errorf("restored outbound %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, inputAlias) || strings.Contains(got, inputToken) || strings.Contains(got, "10.20.30.41") {
		t.Fatalf("outbound restoration/protection incomplete: %q", got)
	}
	entries, err := service.store.Inspect("outbound-residual-restore")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, entry := range entries {
		if entry.Original == "10.20.30.41" {
			if entry.Provenance != ProvenanceGenerated || !strings.Contains(got, entry.Synthetic) {
				t.Fatalf("generated mapping=%+v, output=%q", entry, got)
			}
			return
		}
	}
	t.Fatal("generated technical mapping was not retained")
}

type outboundResidualPassClassifier struct {
	mu    sync.Mutex
	calls int
}

func (c *outboundResidualPassClassifier) Classify(_ string, value string) []Finding {
	if value == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return nil
	}
	return exactFinding(value, "missed-sensitive", "PERSON", CategoryPersonal, MatchNER)
}

func TestServiceStrict_OutboundResidualBlocksFreshFindingWithoutPublishingMutation(t *testing.T) {
	classifier := &outboundResidualPassClassifier{}
	service := newStrictTestService(t, strictTestConfig{classifier: classifier})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "outbound-residual-block"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	const original = "missed-sensitive"
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: original}}}}

	err := service.After(ctx, req, resp)
	assertPrivacyError(t, err, CodeOutputBlocked, "output")
	if got := resp.Message.Content[0].Text; got != original {
		t.Fatalf("failed response became visible as %q, want untouched %q", got, original)
	}
	if classifier.calls != 2 {
		t.Fatalf("outbound classifier calls=%d, want transform plus fresh residual", classifier.calls)
	}
	if got := service.store.Snapshot().RequestsInFlight; got != 0 {
		t.Fatalf("residual block retained %d leases", got)
	}
}

func TestServiceStrict_OutboundResidualRestoresAuthorizedBareInputToken(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{
		classifier:  outboundFixtureClassifier{},
		piiMode:     ActionEncrypt,
		recognizers: []string{"Email"},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "outbound-bare-token"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "corey@example.com"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	_, payload, ok := ParseEncryptedToken(req.System)
	if !ok {
		t.Fatalf("input token=%q", req.System)
	}
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: payload}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != "corey@example.com" {
		t.Fatalf("bare restoration=%q", got)
	}
}

func TestServiceStrict_OutboundResidualReservedInputOriginalUsesExactRestoredOccurrence(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{
		classifier:  outboundFixtureClassifier{},
		piiMode:     ActionEncrypt,
		recognizers: []string{"Email", "IPv4"},
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "reserved-input-original"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "198.18.1.10 corey@example.com"}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}
	inputAlias, inputToken := strings.Fields(req.System)[0], strings.Fields(req.System)[1]
	resp := &canonical.ChatResponse{Message: canonical.Message{ToolCalls: []canonical.ToolCall{{
		Arguments: map[string]any{
			"nested": []any{map[string]any{
				"answer": "prefix " + inputToken + " middle " + inputAlias + " suffix",
			}},
		},
	}}}}

	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After reserved input original: %v", err)
	}
	got := resp.Message.ToolCalls[0].Arguments["nested"].([]any)[0].(map[string]any)["answer"]
	const want = "prefix corey@example.com middle 198.18.1.10 suffix"
	if got != want {
		t.Fatalf("nested restored output=%q, want %q", got, want)
	}

	unbackedState := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "reserved-unbacked-control"})
	unbackedCtx := WithRequestState(context.Background(), unbackedState)
	unbackedReq := &canonical.ChatRequest{}
	if _, err := service.Before(unbackedCtx, unbackedReq); err != nil {
		t.Fatalf("unbacked Before: %v", err)
	}
	unbacked := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText, Text: "198.18.1.10",
	}}}}
	err := service.After(unbackedCtx, unbackedReq, unbacked)
	assertPrivacyError(t, err, CodeOutputBlocked, "output")
	if got := unbacked.Message.Content[0].Text; got != "198.18.1.10" {
		t.Fatalf("blocked unbacked response mutated to %q", got)
	}
}

func TestServiceStrict_ReceiptPassBlockAndInternalError(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		configure func(*Service)
		wantCode  string
		want      string
	}{
		{name: "pass", output: "ordinary", want: "pass"},
		{name: "block", output: "api_key=sk-abcdefghijklmnopqrstuvwxyz123456", wantCode: CodeOutputBlocked, want: "block"},
		{
			name:   "internal error",
			output: "10.20.30.41",
			configure: func(service *Service) {
				service.mapper = panickingTechnicalMapper{payload: "receipt-internal-detail"}
			},
			wantCode: CodeInternalError,
			want:     "error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{
				classifier: outboundFixtureClassifier{},
				secret:     NewSecretClassifier(),
			})
			if tc.configure != nil {
				tc.configure(service)
			}
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "receipt-" + strings.ReplaceAll(tc.name, " ", "-")})
			ctx := WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: tc.output}}}}
			err := service.After(ctx, req, resp)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("After: %v", err)
				}
			} else {
				assertPrivacyError(t, err, tc.wantCode, "output")
			}
			receipt, payload := decodeStateReceipt(t, state)
			if receipt.Version != 1 || receipt.Profile != ProfileStrict || receipt.Coverage != "full" || receipt.Result != tc.want {
				t.Fatalf("receipt=%+v", receipt)
			}
			if len(state.receiptValue()) > maxEncodedReceiptBytes {
				t.Fatalf("encoded receipt bytes=%d, want <=%d", len(state.receiptValue()), maxEncodedReceiptBytes)
			}
			for _, forbidden := range []string{
				"Email", "IPv4", "10.20.30.41", "198.18.", "sk-", "[PII:",
				"strict-test-alias-key", "receipt-internal-detail",
			} {
				if strings.Contains(payload, forbidden) {
					t.Fatalf("receipt leaked %q in %s", forbidden, payload)
				}
			}
		})
	}
}

func TestReceipt_NoValuesOrPerEntityDetails(t *testing.T) {
	receipt := Receipt{
		Version: 1, Profile: ProfileStrict, Scope: "safe-scope", Coverage: "full", Result: "pass",
		Transformed: 999999, Restored: 999999, Blocked: 999999,
	}
	encoded, err := encodeReceipt(receipt)
	if err != nil {
		t.Fatalf("encodeReceipt: %v", err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	payload := string(payloadBytes)
	if len(encoded) > maxEncodedReceiptBytes {
		t.Fatalf("encoded receipt bytes=%d", len(encoded))
	}
	for _, forbidden := range []string{"entity", "original", "synthetic", "pseudonym", "token", "key", "detail"} {
		if strings.Contains(strings.ToLower(payload), forbidden) {
			t.Fatalf("receipt contains forbidden field %q: %s", forbidden, payload)
		}
	}
}

func TestServiceStrict_OutboundProvenanceRestoresOnlyInputAndTransformsGenerated(t *testing.T) {
	service := newStrictTestService(t, strictTestConfig{
		classifier: outboundFixtureClassifier{},
		secret:     NewSecretClassifier(),
		piiMode:    ActionEncrypt,
	})
	state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "outbound-provenance"})
	ctx := WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{
		System: "input 10.20.30.40 corey@example.com",
		Messages: []canonical.Message{{Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{Input: map[string]any{
				"api_key": "sk-abcdefghijklmnopqrstuvwxyz123456",
			}},
		}}}},
	}
	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("Before: %v", err)
	}

	inputAlias := strings.Fields(req.System)[1]
	inputToken := strings.Fields(req.System)[2]
	inputSecretLabel := req.Messages[0].Content[0].ToolUse.Input["api_key"].(string)
	resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText,
		Text: strings.Join([]string{
			"ordinary", inputAlias, inputToken, inputSecretLabel,
			"10.20.30.41", "generated@example.com",
		}, " "),
	}}}}

	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	got := resp.Message.Content[0].Text
	for _, want := range []string{"ordinary", "10.20.30.40", "corey@example.com", inputSecretLabel, "[EMAIL_1]"} {
		if !strings.Contains(got, want) {
			t.Errorf("outbound text %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "10.20.30.41") || strings.Contains(got, "generated@example.com") {
		t.Fatalf("generated protected output was not transformed: %q", got)
	}
	if strings.Contains(got, inputAlias) || strings.Contains(got, inputToken) {
		t.Fatalf("input provenance aliases/tokens were not selectively restored: %q", got)
	}

	entries, err := service.store.Inspect("outbound-provenance")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var generatedTechnical MappingEntry
	for _, entry := range entries {
		if entry.Original == "10.20.30.41" {
			generatedTechnical = entry
		}
	}
	if generatedTechnical.Original == "" || generatedTechnical.Provenance != ProvenanceGenerated {
		t.Fatalf("generated technical mapping=%+v, want ProvenanceGenerated", generatedTechnical)
	}
	if !strings.Contains(got, generatedTechnical.Synthetic) {
		t.Fatalf("generated technical alias %q absent from %q", generatedTechnical.Synthetic, got)
	}
	if snapshot := service.store.Snapshot(); snapshot.RequestsInFlight != 0 {
		t.Fatalf("successful outbound retained %d leases", snapshot.RequestsInFlight)
	}
}

func TestServiceStrict_OutboundProvenanceBlocksSecretsUnknownAndMalformedAliases(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "generated secret", output: "api_key=sk-abcdefghijklmnopqrstuvwxyz123456"},
		{name: "unknown reserved IPv4", output: "198.19.200.10"},
		{name: "unknown reserved IPv6", output: "2001:db8::1234"},
		{name: "unknown privacy token", output: "[PII:Email:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA]"},
		{name: "unknown bare token", output: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{name: "malformed privacy token", output: "[PII:Email:not-valid?]"},
		{name: "malformed alias", output: "[IPv4:h-123456789]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := newStrictTestService(t, strictTestConfig{
				classifier: outboundFixtureClassifier{},
				secret:     NewSecretClassifier(),
			})
			state := NewRequestState(RequestMetadata{RequestedProfile: "strict", ScopeID: "outbound-block-" + strings.ReplaceAll(tc.name, " ", "-")})
			ctx := WithRequestState(context.Background(), state)
			req := &canonical.ChatRequest{}
			if _, err := service.Before(ctx, req); err != nil {
				t.Fatalf("Before: %v", err)
			}
			resp := &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: tc.output}}}}
			err := service.After(ctx, req, resp)
			assertPrivacyError(t, err, CodeOutputBlocked, "output")
			if got := resp.Message.Content[0].Text; got != tc.output {
				t.Fatalf("blocked response mutated from %q to %q", tc.output, got)
			}
			if snapshot := service.store.Snapshot(); snapshot.RequestsInFlight != 0 {
				t.Fatalf("blocked outbound retained %d leases", snapshot.RequestsInFlight)
			}
		})
	}
}
