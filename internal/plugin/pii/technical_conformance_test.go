package pii

import (
	"context"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/privacy"
)

func TestTechnicalConformance_StrictSIPInputIsDispatchableAndReversible(t *testing.T) {
	service := newTechnicalConformanceService(t)
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "strict",
		ScopeID:          "technical-conformance-sip",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "sip:alice@invalid"}

	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("strict SIP input was not dispatchable: %v", err)
	}
	if req.System == "sip:alice@invalid" || !strings.HasPrefix(req.System, "sip:u-") ||
		!strings.Contains(req.System, "@gw.invalid") {
		t.Fatalf("strict SIP input = %q, want reversible technical pseudonym", req.System)
	}
	assertTechnicalMapping(t, service, "technical-conformance-sip", "SIP_URI", "sip:alice@invalid", privacy.ProvenanceInput)

	resp := technicalEchoResponse(req.System)
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("strict SIP output: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != "sip:alice@invalid" {
		t.Fatalf("strict SIP restoration = %q, want exact caller input", got)
	}
}

func TestTechnicalConformance_ContextualIMEIWinsCreditCardOverlap(t *testing.T) {
	service := newTechnicalConformanceService(t)
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "strict",
		ScopeID:          "technical-conformance-imei",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{System: "IMEI: 490154203237518"}

	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("strict IMEI input was not dispatchable: %v", err)
	}
	entry := assertTechnicalMapping(
		t,
		service,
		"technical-conformance-imei",
		"IMEI",
		"490154203237518",
		privacy.ProvenanceInput,
	)
	if !strings.Contains(req.System, entry.Synthetic) || strings.Contains(req.System, "490154203237518") {
		t.Fatalf("strict IMEI input = %q, want IMEI technical pseudonym %q", req.System, entry.Synthetic)
	}

	resp := technicalEchoResponse(req.System)
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("strict IMEI output: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != "IMEI: 490154203237518" {
		t.Fatalf("strict IMEI restoration = %q, want exact caller input", got)
	}
}

func TestTechnicalConformance_SITEProvenanceControlsRestoration(t *testing.T) {
	t.Run("caller input restores exactly", func(t *testing.T) {
		service := newTechnicalConformanceService(t)
		state := privacy.NewRequestState(privacy.RequestMetadata{
			RequestedProfile: "strict",
			ScopeID:          "technical-conformance-site-input",
		})
		ctx := privacy.WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{System: "site-A12_NYC01"}

		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("strict SITE input was not dispatchable: %v", err)
		}
		entry := assertTechnicalMapping(
			t,
			service,
			"technical-conformance-site-input",
			"SITE",
			"site-A12_NYC01",
			privacy.ProvenanceInput,
		)
		if req.System != entry.Synthetic || !strings.HasPrefix(req.System, "SITE-SYN-") {
			t.Fatalf("strict SITE input = %q, want input-provenance alias %q", req.System, entry.Synthetic)
		}

		resp := technicalEchoResponse(req.System)
		if err := service.After(ctx, req, resp); err != nil {
			t.Fatalf("strict SITE output: %v", err)
		}
		if got := resp.Message.Content[0].Text; got != "site-A12_NYC01" {
			t.Fatalf("strict SITE restoration = %q, want exact caller input", got)
		}
	})

	t.Run("generated value stays pseudonymized", func(t *testing.T) {
		service := newTechnicalConformanceService(t)
		state := privacy.NewRequestState(privacy.RequestMetadata{
			RequestedProfile: "strict",
			ScopeID:          "technical-conformance-site-generated",
		})
		ctx := privacy.WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{}
		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("strict generated-SITE setup: %v", err)
		}

		resp := technicalEchoResponse("site-A12_NYC01")
		if err := service.After(ctx, req, resp); err != nil {
			t.Fatalf("strict generated SITE output: %v", err)
		}
		entry := assertTechnicalMapping(
			t,
			service,
			"technical-conformance-site-generated",
			"SITE",
			"site-A12_NYC01",
			privacy.ProvenanceGenerated,
		)
		if got := resp.Message.Content[0].Text; got != entry.Synthetic || got == "site-A12_NYC01" {
			t.Fatalf("generated SITE output = %q, want generated-provenance alias %q", got, entry.Synthetic)
		}
	})

	t.Run("unbacked synthetic alias blocks", func(t *testing.T) {
		service := newTechnicalConformanceService(t)
		state := privacy.NewRequestState(privacy.RequestMetadata{
			RequestedProfile: "strict",
			ScopeID:          "technical-conformance-site-unbacked",
		})
		ctx := privacy.WithRequestState(context.Background(), state)
		req := &canonical.ChatRequest{}
		if _, err := service.Before(ctx, req); err != nil {
			t.Fatalf("strict unbacked-SITE setup: %v", err)
		}

		err := service.After(ctx, req, technicalEchoResponse("SITE-SYN-AAAAAAAAAA"))
		_, code, ok := privacy.ErrorInfo(err)
		if !ok || code != privacy.CodeOutputBlocked {
			t.Fatalf("unbacked SITE alias error = %v (%q), want %s", err, code, privacy.CodeOutputBlocked)
		}
	})
}

func TestTechnicalConformance_ToolRecoveryReusesEncryptedPlaceholderAndRestoresOnce(t *testing.T) {
	const sensitiveMarker = "corey@example.com"
	restorations := 0
	service := newTechnicalConformanceServiceWithObservers(t, privacy.Observers{
		Restoration: func(_ privacy.Profile, entity, result string) {
			if entity == "Email" && result == "pass" {
				restorations++
			}
		},
	})
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "standard",
		ScopeID:          "technical-conformance-recovery",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Messages: []canonical.Message{{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "Send status to " + sensitiveMarker,
		}},
	}}}

	if _, err := service.Before(ctx, req); err != nil {
		t.Fatalf("standard encrypted input: %v", err)
	}
	firstAttemptPrompt := req.Messages[0].Content[0].Text
	if strings.Contains(firstAttemptPrompt, sensitiveMarker) || !strings.Contains(firstAttemptPrompt, "[PII:Email:") {
		t.Fatalf("first transformed prompt = %q, want encrypted email placeholder", firstAttemptPrompt)
	}
	// Recovery reuses the same transformed canonical request. Capturing it for
	// both ACP attempts must therefore preserve the exact randomized token;
	// calling Before a second time would generate a different ciphertext.
	secondAttemptEcho := req.Messages[0].Content[0].Text
	if secondAttemptEcho != firstAttemptPrompt {
		t.Fatalf("recovery placeholder changed: first=%q second=%q", firstAttemptPrompt, secondAttemptEcho)
	}

	resp := &canonical.ChatResponse{Message: canonical.Message{ToolCalls: []canonical.ToolCall{{
		ID:        "recovery-call",
		Name:      "send_status",
		Arguments: map[string]any{"email": strings.TrimPrefix(secondAttemptEcho, "Send status to ")},
	}}}}
	if err := service.After(ctx, req, resp); err != nil {
		t.Fatalf("restore corrected response: %v", err)
	}
	if got := resp.Message.ToolCalls[0].Arguments["email"]; got != sensitiveMarker {
		t.Fatalf("corrected response email = %q, want %q", got, sensitiveMarker)
	}
	if restorations != 1 {
		t.Fatalf("successful Email restorations = %d, want 1", restorations)
	}
}

func newTechnicalConformanceService(t *testing.T) *privacy.Service {
	return newTechnicalConformanceServiceWithObservers(t, privacy.Observers{})
}

func newTechnicalConformanceServiceWithObservers(t *testing.T, observers privacy.Observers) *privacy.Service {
	t.Helper()
	service, err := privacy.NewService(privacy.Config{
		DefaultProfile:     privacy.ProfileStandard,
		RequestProfiles:    []privacy.Profile{privacy.ProfileStandard, privacy.ProfileStrict},
		AliasKey:           []byte("technical-conformance-alias-key"),
		SecretAction:       privacy.ActionReplace,
		TechnicalAction:    privacy.ActionPseudonymize,
		ScopeTTL:           time.Hour,
		MaxScopes:          16,
		MaxEntriesPerScope: 32,
		MaxTotalEntries:    128,
		PIIEnabled:         true,
		PIIMode:            privacy.ActionEncrypt,
		PIIEncryptKey:      []byte("0123456789abcdef0123456789abcdef"),
		Recognizers:        SourceAuditNames(),
		Classifier:         NewPIIClassifier(Recognizers, nil, false),
		SecretClassifier:   privacy.NewSecretClassifier(),
		Observers:          observers,
	})
	if err != nil {
		t.Fatalf("privacy.NewService: %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func assertTechnicalMapping(
	t *testing.T,
	service *privacy.Service,
	scope, entity, original string,
	provenance privacy.Provenance,
) privacy.MappingEntry {
	t.Helper()
	entries, err := service.TriageCapability().InspectScope(scope)
	if err != nil {
		t.Fatalf("InspectScope(%q): %v", scope, err)
	}
	for _, entry := range entries {
		if entry.Entity == entity && entry.Original == original {
			if entry.Provenance != provenance {
				t.Fatalf("%s mapping provenance = %q, want %q", entity, entry.Provenance, provenance)
			}
			return entry
		}
	}
	t.Fatalf("%s mapping for %q missing from %+v", entity, original, entries)
	return privacy.MappingEntry{}
}

func technicalEchoResponse(value string) *canonical.ChatResponse {
	return &canonical.ChatResponse{Message: canonical.Message{Content: []canonical.ContentPart{{
		Kind: canonical.ContentKindText,
		Text: value,
	}}}}
}
