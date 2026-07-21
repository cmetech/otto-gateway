// internal/plugin/compress/ctx_test.go
package compress

import (
	"context"
	"testing"
)

func TestHeaderDirective_RoundTrip(t *testing.T) {
	ctx := context.Background()

	if _, ok := HeaderDirectiveFromContext(ctx); ok {
		t.Fatal("unstamped ctx: ok = true, want false")
	}

	on, ok := HeaderDirectiveFromContext(WithHeaderDirective(ctx, true))
	if !ok || !on {
		t.Errorf("stamped true: got (%v, %v), want (true, true)", on, ok)
	}

	on, ok = HeaderDirectiveFromContext(WithHeaderDirective(ctx, false))
	if !ok || on {
		t.Errorf("stamped false: got (%v, %v), want (false, true)", on, ok)
	}
}

func TestParseHeaderValue(t *testing.T) {
	cases := []struct {
		in     string
		wantOn bool
		wantOK bool
	}{
		{"1", true, true},
		{"true", true, true},
		{"on", true, true},
		{"TRUE", true, true},
		{" 1 ", true, true}, // whitespace trimmed
		{"0", false, true},
		{"false", false, true},
		{"off", false, true},
		{" 0 ", false, true},
		// Invalid values are IGNORED (ok=false → fall through to
		// suffix/env), never treated as enable — "00", "no", garbage must
		// not switch destructive compression on.
		{"00", false, false},
		{"no", false, false},
		{"yes", false, false},
		{"garbage", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		on, ok := ParseHeaderValue(c.in)
		if on != c.wantOn || ok != c.wantOK {
			t.Errorf("ParseHeaderValue(%q) = (%v, %v), want (%v, %v)", c.in, on, ok, c.wantOn, c.wantOK)
		}
	}
}
