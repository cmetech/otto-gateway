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
