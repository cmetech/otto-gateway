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
	if id.WinExeName != "OTTO.exe" || id.MacAppName != "OTTO.app" || id.InstallRepo != "cmetech/otto" {
		t.Fatalf("bad derivation: %+v", id)
	}
}

func TestParseBrandDescriptor(t *testing.T) {
	valid := []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto","releasesRepo":"cmetech/loop24"}`)
	doc, ok := parseBrandDescriptor(valid)
	if !ok || doc.Slug != "loop24" || doc.DisplayName != "LOOP24" || doc.HomeDir != ".loop24" {
		t.Fatalf("valid descriptor = (%+v, %v)", doc, ok)
	}
	invalid := [][]byte{
		[]byte(`{"schemaVersion":2,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"../loop24","displayName":"LOOP24","homeDir":".../loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"bad;name","homeDir":".loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".otto","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"other"}`),
	}
	for _, raw := range invalid {
		if _, ok := parseBrandDescriptor(raw); ok {
			t.Errorf("accepted invalid descriptor %s", raw)
		}
	}
}
