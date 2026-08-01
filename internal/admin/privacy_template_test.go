package admin

import (
	"context"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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
	for _, forbidden := range []string{
		"<form", "<input", "<select", "<textarea", "<button", "<script",
		"data-theme-toggle", "document.documentelement.dataset", "localstorage",
		"data-privacy-mutate", "mapping", "method=\"post\"", "method=\"delete\"",
		"method=\"put\"", "method=\"patch\"", "triage-token-must-not-leak",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("read-only privacy page contains forbidden content %q", forbidden)
		}
	}
}

func TestPrivacyPage_ThemeControlsRemainOnOtherPages(t *testing.T) {
	for _, path := range []string{"/", "/about", "/docs"} {
		body := renderPrivacyTestPage(t, path)
		for _, want := range []string{"<button", "data-theme-toggle", "document.documentElement.dataset", "localStorage"} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s lost shared theme control %q", path, want)
			}
		}
	}
}

// TestPrivacyLightThemeTextContrast catches a light-theme token change that
// makes the small eyebrow or help-link text fall below WCAG 2.1 AA's 4.5:1
// threshold against the card surface.
func TestPrivacyLightThemeTextContrast(t *testing.T) {
	cssBytes, err := fs.ReadFile(assetsFS, "static/css/admin.css")
	if err != nil {
		t.Fatalf("read embedded admin CSS: %v", err)
	}
	lightBlock := regexp.MustCompile(`(?s)\[data-theme="light"\]\s*\{([^}]*)\}`).FindSubmatch(cssBytes)
	if len(lightBlock) != 2 {
		t.Fatal("light-theme token block missing")
	}
	tokens := string(lightBlock[1])
	background := cssHexToken(t, tokens, "--gw-card")
	for _, token := range []string{"--gw-privacy-eyebrow", "--gw-privacy-link"} {
		foreground := cssHexToken(t, tokens, token)
		if ratio := contrastRatio(foreground, background); ratio < 4.5 {
			t.Errorf("%s contrast = %.2f:1, want >= 4.5:1", token, ratio)
		}
	}
}

func cssHexToken(t *testing.T, block, token string) [3]float64 {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(token) + `\s*:\s*(#[0-9A-Fa-f]{6})`)
	match := re.FindStringSubmatch(block)
	if len(match) != 2 {
		t.Fatalf("light-theme token %s missing or not a stable hex color", token)
	}
	var rgb [3]float64
	for i := range rgb {
		value, err := strconv.ParseUint(match[1][1+i*2:3+i*2], 16, 8)
		if err != nil {
			t.Fatalf("parse %s: %v", token, err)
		}
		rgb[i] = float64(value) / 255
	}
	return rgb
}

func contrastRatio(a, b [3]float64) float64 {
	luminance := func(rgb [3]float64) float64 {
		linear := func(channel float64) float64 {
			if channel <= 0.04045 {
				return channel / 12.92
			}
			return math.Pow((channel+0.055)/1.055, 2.4)
		}
		return 0.2126*linear(rgb[0]) + 0.7152*linear(rgb[1]) + 0.0722*linear(rgb[2])
	}
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
