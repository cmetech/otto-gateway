//go:build darwin || windows

package main

import "testing"

func TestValidateDisplayName(t *testing.T) {
	ok := []string{"OTTO", "LOOP24", "My App-1", "a.b_c"}
	bad := []string{"", "bad;name", "a\"b", "x/y", "a`b", strings2(65)}
	for _, s := range ok {
		if !validateDisplayName(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range bad {
		if validateDisplayName(s) {
			t.Errorf("expected %q invalid", s)
		}
	}
}

func strings2(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestIdentityFromDisplayName(t *testing.T) {
	id := identityFromDisplayName("OTTO")
	if id.WinExeName != "OTTO.exe" || id.MacAppName != "OTTO.app" ||
		id.MacProcMatch != "OTTO.app/Contents/MacOS/OTTO" {
		t.Fatalf("bad derivation: %+v", id)
	}
}

func TestRefineBrandIdentity_DefaultsWhenMissingOrBad(t *testing.T) {
	base := defaultBrandIdentity()
	// missing file → defaults
	got := refineBrandIdentity(base, "/nope/brand.json", func(string) ([]byte, error) { return nil, errMissing })
	if got.DisplayName != "OTTO" {
		t.Fatalf("missing file should keep defaults, got %q", got.DisplayName)
	}
	// malformed json → defaults
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) { return []byte("{"), nil })
	if got.DisplayName != "OTTO" {
		t.Fatalf("malformed json should keep defaults, got %q", got.DisplayName)
	}
	// invalid displayName → defaults (no injection)
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) { return []byte(`{"displayName":"a;b"}`), nil })
	if got.DisplayName != "OTTO" {
		t.Fatalf("invalid name should keep defaults, got %q", got.DisplayName)
	}
	// valid override
	got = refineBrandIdentity(base, "x", func(string) ([]byte, error) {
		return []byte(`{"displayName":"LOOP24","releasesRepo":"cmetech/loop24"}`), nil
	})
	if got.DisplayName != "LOOP24" || got.WinExeName != "LOOP24.exe" || got.InstallRepo != "cmetech/loop24" {
		t.Fatalf("valid override failed: %+v", got)
	}
}

var errMissing = &fsErr{}

type fsErr struct{}

func (*fsErr) Error() string { return "missing" }
