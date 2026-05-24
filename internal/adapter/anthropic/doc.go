// Package anthropic implements the Anthropic-shape HTTP surface for the
// Loop24 gateway. It exposes a chi sub-router that mounts under the
// configured ANTHROPIC_PATH_PREFIX (default "/v1") and forwards
// canonical requests through the engine to the warm kiro-cli pool.
//
// Architecture (TRST-04 boundary — enforced by .go-arch-lint.yml):
//   - this package declares CONSUMER-defined interfaces (Engine,
//     RunHandle, Stream) locally. It MUST NOT import internal/engine —
//     the concrete *engine.Engine structurally satisfies the local
//     Engine interface and is wired in by cmd/loop24-gateway/main.go.
//   - this package imports loop24-gateway/internal/canonical only.
//
// Status: package skeleton only. Plans 03.1-02, 03.1-03, and 03.1-04
// populate adapter.go, handlers.go, wire.go, render.go, sse.go, and
// errors.go in that order. This doc.go file establishes the package
// directory so .go-arch-lint.yml's adapter_anthropic component
// declaration (set in Plan 03.1-01 Task 3) resolves cleanly before
// any adapter source code lands.
package anthropic
