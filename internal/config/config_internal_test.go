package config

// Whitebox tests for unexported helpers in the config package.
// Mirrors the Plan 02 pattern (auth_internal_test.go) of splitting tests of
// unexported symbols into a separate *_internal_test.go file so the existing
// blackbox config_test.go (package config_test) stays blackbox.

import (
	"strings"
	"testing"
)

func TestParseCIDRs_BareIP(t *testing.T) {
	t.Parallel()

	got, err := parseCIDRs([]string{"192.168.1.1"})
	if err != nil {
		t.Fatalf("parseCIDRs returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseCIDRs: got %d prefixes, want 1", len(got))
	}
	if got[0].String() != "192.168.1.1/32" {
		t.Errorf("parseCIDRs bare-IP fallback: got %q, want %q", got[0].String(), "192.168.1.1/32")
	}
	if got[0].Bits() != 32 {
		t.Errorf("parseCIDRs bare-IP bits: got %d, want 32", got[0].Bits())
	}
}

func TestParseCIDRs_BareIPv6(t *testing.T) {
	t.Parallel()

	got, err := parseCIDRs([]string{"::1"})
	if err != nil {
		t.Fatalf("parseCIDRs returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("parseCIDRs: got %d prefixes, want 1", len(got))
	}
	if got[0].String() != "::1/128" {
		t.Errorf("parseCIDRs bare-IPv6 fallback: got %q, want %q", got[0].String(), "::1/128")
	}
}

func TestParseCIDRs_Mixed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entries []string
		want    []string
	}{
		{
			name:    "v4 CIDR + bare v4",
			entries: []string{"10.0.0.0/8", "192.168.1.1"},
			want:    []string{"10.0.0.0/8", "192.168.1.1/32"},
		},
		{
			name:    "v4 CIDR + v6 CIDR",
			entries: []string{"10.0.0.0/8", "fd00::/8"},
			want:    []string{"10.0.0.0/8", "fd00::/8"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseCIDRs(tc.entries)
			if err != nil {
				t.Fatalf("parseCIDRs returned unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseCIDRs: got %d prefixes, want %d", len(got), len(tc.want))
			}
			for i, p := range got {
				if p.String() != tc.want[i] {
					t.Errorf("parseCIDRs[%d]: got %q, want %q", i, p.String(), tc.want[i])
				}
			}
		})
	}
}

func TestParseCIDRs_Error(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entries []string
	}{
		{name: "nonsense", entries: []string{"nonsense"}},
		{name: "almost-CIDR", entries: []string{"10.0.0.0/abc"}},
		{name: "mixed-with-bad-entry", entries: []string{"10.0.0.0/8", "not-an-ip"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseCIDRs(tc.entries)
			if err == nil {
				t.Fatalf("parseCIDRs(%v) should return an error, got nil", tc.entries)
			}
		})
	}
}

func TestParseCIDRs_NilInput(t *testing.T) {
	t.Parallel()

	// Nil input → (nil, nil); preserves Node parity ("empty env = allow-all").
	got, err := parseCIDRs(nil)
	if err != nil {
		t.Fatalf("parseCIDRs(nil) returned unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("parseCIDRs(nil): got %v, want nil", got)
	}
}

func TestParseCIDRs_EmptyNonNilInput(t *testing.T) {
	t.Parallel()

	// Non-nil zero-length input → empty non-nil slice; documents the
	// contract distinction from nil input.
	got, err := parseCIDRs([]string{})
	if err != nil {
		t.Fatalf("parseCIDRs([]string{}) returned unexpected error: %v", err)
	}
	if got == nil {
		t.Errorf("parseCIDRs([]string{}): got nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("parseCIDRs([]string{}): got %d entries, want 0", len(got))
	}
}

func TestParseCIDRs_ErrorMessageMentionsEntry(t *testing.T) {
	t.Parallel()

	_, err := parseCIDRs([]string{"definitely-not-an-ip"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "definitely-not-an-ip") {
		t.Errorf("error message should mention offending entry, got: %v", err)
	}
}

// --- validateEnabledSurfaces (Phase 3.1 D-16) ---------------------------

func TestValidateEnabledSurfaces_OllamaOnly(t *testing.T) {
	t.Parallel()
	if err := validateEnabledSurfaces([]string{"ollama"}); err != nil {
		t.Errorf("ollama is in the allow-list, want nil error, got %v", err)
	}
}

func TestValidateEnabledSurfaces_AnthropicOnly(t *testing.T) {
	t.Parallel()
	if err := validateEnabledSurfaces([]string{"anthropic"}); err != nil {
		t.Errorf("anthropic is in the allow-list, want nil error, got %v", err)
	}
}

func TestValidateEnabledSurfaces_Both(t *testing.T) {
	t.Parallel()
	if err := validateEnabledSurfaces([]string{"ollama", "anthropic"}); err != nil {
		t.Errorf("ollama+anthropic both valid, want nil error, got %v", err)
	}
}

func TestValidateEnabledSurfaces_NilOrEmpty(t *testing.T) {
	t.Parallel()
	// Load() injects the default before calling validateEnabledSurfaces,
	// so an empty list never reaches us in production. The helper still
	// tolerates it (no-op) so tests that probe edge cases don't crash.
	if err := validateEnabledSurfaces(nil); err != nil {
		t.Errorf("nil list should be a no-op, got %v", err)
	}
	if err := validateEnabledSurfaces([]string{}); err != nil {
		t.Errorf("empty list should be a no-op, got %v", err)
	}
}

func TestValidateEnabledSurfaces_UnknownName_NamesTheOffender(t *testing.T) {
	t.Parallel()
	// D-16: error message must NAME the offending surface so the
	// operator can diagnose the typo without re-reading the env (RESEARCH.md
	// Pitfall 10).
	err := validateEnabledSurfaces([]string{"ollama", "olama"})
	if err == nil {
		t.Fatal("want error for typo 'olama', got nil")
	}
	if !strings.Contains(err.Error(), "olama") {
		t.Errorf("error must name the offending surface 'olama', got: %v", err)
	}
}

func TestValidateEnabledSurfaces_OpenAIStillForbidden(t *testing.T) {
	t.Parallel()
	// Phase 3 will widen the allow-list to include "openai". Pin the
	// Phase 3.1 contract: it must REJECT "openai" until then so a
	// premature OpenAI deployment fails fast rather than silently
	// disabling the surface.
	err := validateEnabledSurfaces([]string{"openai"})
	if err == nil {
		t.Fatal("Phase 3.1 must NOT allow 'openai' (Phase 3 widens the list); got nil error")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error must name the offending surface 'openai', got: %v", err)
	}
}
