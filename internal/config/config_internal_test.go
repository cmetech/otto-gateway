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
