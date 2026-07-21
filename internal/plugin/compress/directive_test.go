// internal/plugin/compress/directive_test.go
package compress

import "testing"

func TestSplitCompressDirective(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cases := []struct {
		in       string
		wantBase string
		wantDir  *bool
	}{
		{"qwen-2.5+compress", "qwen-2.5", boolPtr(true)},
		{"qwen-2.5-compress", "qwen-2.5", boolPtr(false)},
		{"claude-sonnet-4-6+compress", "claude-sonnet-4-6", boolPtr(true)},
		{"qwen-2.5+COMPRESS", "qwen-2.5", boolPtr(true)}, // case-insensitive
		{"qwen-2.5", "qwen-2.5", nil},
		{"auto", "auto", nil},
		{"", "", nil},
		{"compress", "compress", nil},                   // no +/- separator → not a directive
		{"model+compression", "model+compression", nil}, // suffix must be exactly "compress"
		{"+compress", "+compress", nil},                 // empty base → literal model name, NOT a directive
		{"-compress", "-compress", nil},                 // empty base → literal model name, NOT a directive
		// KNOWN COLLISION (documented, Node-syntax parity): a real model
		// whose id happens to end in "-compress" is indistinguishable from
		// a disable directive and gets stripped. There is no escape syntax;
		// docs/operating.md carries the caveat.
		{"vendor/model-compress", "vendor/model", boolPtr(false)},
	}
	for _, c := range cases {
		base, dir := SplitCompressDirective(c.in)
		if base != c.wantBase {
			t.Errorf("SplitCompressDirective(%q) base = %q, want %q", c.in, base, c.wantBase)
		}
		switch {
		case dir == nil && c.wantDir != nil:
			t.Errorf("SplitCompressDirective(%q) dir = nil, want %v", c.in, *c.wantDir)
		case dir != nil && c.wantDir == nil:
			t.Errorf("SplitCompressDirective(%q) dir = %v, want nil", c.in, *dir)
		case dir != nil && c.wantDir != nil && *dir != *c.wantDir:
			t.Errorf("SplitCompressDirective(%q) dir = %v, want %v", c.in, *dir, *c.wantDir)
		}
	}
}
