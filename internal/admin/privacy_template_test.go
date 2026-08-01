package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/testutil"
)

func completePrivacySnapshot() PrivacySnapshot {
	return PrivacySnapshot{
		DefaultProfile:        "strict",
		RequestProfiles:       []string{"standard", "strict"},
		StrictAvailable:       true,
		TriageEnabled:         false,
		AliasKeyPresent:       true,
		TriageTokenPresent:    true,
		PIIEnabled:            true,
		NEREnabled:            true,
		SecretAction:          "replace",
		TechnicalAction:       "pseudonymize",
		PIIMode:               "encrypt",
		Recognizers:           []string{"Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone", "SIP_URI", "IMEI", "IMSI", "MSISDN", "MAC_ADDRESS", "COORDINATES", "SITE", "USAddress", "USState", "USZIP", "PERSON", "LOCATION"},
		EntityActions:         map[string]string{"IPv4": "pseudonymize", "SSN": "drop"},
		StrictFullBuffering:   true,
		ReceiptVersion:        1,
		ScopesActive:          3,
		RequestsInFlight:      2,
		Entries:               41,
		MaxScopes:             128,
		MaxEntriesPerScope:    4096,
		MaxTotalEntries:       32768,
		ScopeTTLSeconds:       3600,
		OldestScopeAgeSeconds: 91,
		RequestsProtected:     144,
		RequestsBlocked:       7,
		LastErrorCode:         "privacy_output_blocked",
	}
}

func renderPrivacyTestPage(t *testing.T, path string) string {
	t.Helper()
	h := Handler(Deps{
		Logger:             testutil.Logger(t),
		Version:            "1.2.3",
		PrivacyStatus:      stubPrivacyStatus{snapshot: completePrivacySnapshot()},
		PrivacyTriageToken: "triage-token-MUST-NOT-LEAK",
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: got %d, want 200; body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestDashboardPrivacy_StatusHooks catches loss of any operational value the
// dashboard promises to hydrate from the ordinary snapshot.
func TestDashboardPrivacy_StatusHooks(t *testing.T) {
	body := renderPrivacyTestPage(t, "/")
	for _, want := range []string{
		"Privacy Boundary", "Default profile", "Strict requests", "Protected", "Blocked",
		"Active scopes", "In flight", "Entries", "Per-scope limit", "Scope TTL",
		"Oldest scope", "Triage", "Last error", "data-privacy-default-profile",
		"data-privacy-strict", "data-privacy-protected", "data-privacy-blocked",
		"data-privacy-scopes", "data-privacy-in-flight", "data-privacy-entries",
		"data-privacy-oldest", "data-privacy-triage", "data-privacy-last-error",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing privacy contract %q", want)
		}
	}
	if !strings.Contains(body, `href="/admin/privacy"`) {
		t.Error("dashboard privacy help link is not keyboard-reachable")
	}
}

// TestAboutPrivacy_CompleteConfigurationAndInventory catches the documented
// inventory drift that previously claimed 13 regex recognizers.
func TestAboutPrivacy_CompleteConfigurationAndInventory(t *testing.T) {
	body := renderPrivacyTestPage(t, "/about")
	for _, setting := range []string{
		"PRIVACY_DEFAULT_PROFILE", "PRIVACY_REQUEST_PROFILES", "PRIVACY_ALIAS_KEY",
		"PRIVACY_SECRET_ACTION", "PRIVACY_TECHNICAL_ACTION", "PRIVACY_SCOPE_TTL",
		"PRIVACY_MAX_SCOPES", "PRIVACY_MAX_ENTRIES_PER_SCOPE", "PRIVACY_MAX_TOTAL_ENTRIES",
		"PRIVACY_TRIAGE_ENABLED", "PRIVACY_TRIAGE_TOKEN", "PII_REDACTION_ENABLED",
		"PII_REDACTION_MODE", "PII_NER_ENABLED", "PII_ENABLED_ENTITIES",
		"PII_ENTITY_ACTIONS", "PII_HASH_KEY", "PII_ENCRYPT_KEY",
	} {
		if !strings.Contains(body, setting) {
			t.Errorf("About missing setting %q", setting)
		}
	}
	for _, recognizer := range completePrivacySnapshot().Recognizers {
		if !strings.Contains(body, recognizer) {
			t.Errorf("About missing recognizer %q", recognizer)
		}
	}
	for _, want := range []string{"16 regex recognizers", "PERSON", "LOCATION", "Restart required", "current / maximum", "strict requests buffer the complete response", "Receipt version 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("About missing privacy documentation %q", want)
		}
	}
	if strings.Contains(body, "triage-token-MUST-NOT-LEAK") {
		t.Fatal("About leaked triage token canary")
	}
}

// TestPrivacyPage_ReadOnlyAccessiblePosture catches mutation affordances,
// mapping disclosure, and loss of the semantic operator status structure.
func TestPrivacyPage_ReadOnlyAccessiblePosture(t *testing.T) {
	body := renderPrivacyTestPage(t, "/privacy")
	for _, want := range []string{
		`aria-current="page"`, "Privacy Boundary", "Runtime status", "Enforcement contract",
		"Configuration", "Recognizer inventory", "<dl", "<dt", "<dd", "strict",
		"144", "7", "3 / 128", "41 / 32768", "4096", "1h", "1m 31s",
		"privacy_output_blocked", "Alias key", "present", "Triage token", "Restart required",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page missing %q", want)
		}
	}
	lower := strings.ToLower(body)
	for _, forbidden := range []string{"<form", "<input", "<select", "<textarea", "data-privacy-mutate", "mapping", "method=\"post\"", "method=\"delete\"", "method=\"put\"", "method=\"patch\"", "triage-token-must-not-leak"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("read-only privacy page contains forbidden content %q", forbidden)
		}
	}
}
