// Package engine — Phase 1.1 preflight gate (Codex H-1).
//
// This file compile-references the exact Phase 1.1 surface that Plan 04
// depends on. If the package fails to BUILD because any reference below is
// invalid (Phase 1.1 has not landed expected API), the executor must surface:
//
//	Phase 2 cannot proceed — Phase 1.1 has not landed expected API.
//	Run /gsd-execute-phase 1.1 first.
//
// Compile-time references via `var _ T = expr` cannot fail at runtime; the
// primary failure mode is a build error caught by `go build`.
package engine

import (
	"testing"

	"loop24-gateway/internal/acp"
	"loop24-gateway/internal/canonical"
)

// TestPreflight_Phase11Surface compile-asserts the four Phase 1.1 surface
// additions this plan depends on. If this file builds, all four exist.
//
//   - acp.FinalResult.StopReason (Plan 01.1-03 D-07)
//   - (*acp.Client).AvailableModels (Plan 01.1-02 D-12)
//   - (*acp.Client).PromptCapabilities (Plan 01.1-02 D-09)
//   - canonical.ResourceLinkBlock.Name (Plan 01.1-01 D-04)
func TestPreflight_Phase11Surface(t *testing.T) {
	t.Helper()

	// Explicit-type assertions: the compiler will reject the assignment
	// if the underlying type drifts (e.g., AvailableModels returns
	// something other than []canonical.ModelInfo). Functions below
	// accept the expected type so the constraint is enforced even
	// where lint-formatters would otherwise strip the var-declared
	// type to inferred.
	assertStopReason := func(_ canonical.StopReason) {}
	assertModelsFn := func(_ func() []canonical.ModelInfo) {}
	assertCapsFn := func(_ func() canonical.PromptCapabilities) {}
	assertString := func(_ string) {}

	// 01.1-03 D-07: acp.FinalResult carries canonical.StopReason.
	assertStopReason((acp.FinalResult{}).StopReason)

	// 01.1-02 D-12: *acp.Client exposes AvailableModels() []canonical.ModelInfo.
	assertModelsFn((*acp.Client)(nil).AvailableModels)

	// 01.1-02 D-09: *acp.Client exposes PromptCapabilities() canonical.PromptCapabilities.
	assertCapsFn((*acp.Client)(nil).PromptCapabilities)

	// 01.1-01 D-04: canonical.ResourceLinkBlock has Name field.
	assertString(canonical.ResourceLinkBlock{}.Name)

	t.Log("Phase 1.1 preflight passed — all four surface additions present")
}
