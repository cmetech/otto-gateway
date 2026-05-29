// Quick 260529-ll2 — surface_test.go.
//
// Tests WithSurface / SurfaceFromContext round-trip and the absent-key
// fallback shape. Both tests are pure ctx-value mechanics — no engine,
// no http.Request, no goroutines — so they run trivially fast.

package plugin

import (
	"context"
	"testing"
)

// TestSurfaceContextRoundTrip asserts that WithSurface stamps a value
// recoverable by SurfaceFromContext with ok=true.
func TestSurfaceContextRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []string{"openai", "ollama", "anthropic"}
	for _, name := range cases {
		ctx := WithSurface(context.Background(), name)
		got, ok := SurfaceFromContext(ctx)
		if !ok {
			t.Errorf("SurfaceFromContext(WithSurface(_, %q)): ok=false, want true", name)
		}
		if got != name {
			t.Errorf("SurfaceFromContext(WithSurface(_, %q)): got %q, want %q", name, got, name)
		}
	}
}

// TestSurfaceFromContext_Absent asserts that a bare ctx returns
// ("", false) — callers can render the surface field unconditionally
// (empty string is fine downstream).
func TestSurfaceFromContext_Absent(t *testing.T) {
	t.Parallel()

	got, ok := SurfaceFromContext(context.Background())
	if ok {
		t.Errorf("SurfaceFromContext(empty ctx): ok=true, want false")
	}
	if got != "" {
		t.Errorf("SurfaceFromContext(empty ctx): got %q, want empty", got)
	}
}
