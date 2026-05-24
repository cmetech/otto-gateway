// Package engine — Task 1 placeholder stub for buildBlocks. The real
// implementation lands in Task 2 (bracketed-section flattening + image
// emission). Keeping a minimal stub here lets engine.go reference
// buildBlocks at Task-1 build time without a forward-declaration cycle.
//
// THIS FILE IS REPLACED IN TASK 2 with the production implementation.
package engine

import (
	"loop24-gateway/internal/canonical"
)

// buildBlocks is replaced in Task 2 with the bracketed-section
// formatter and BlockKindImage emission.
func buildBlocks(req *canonical.ChatRequest) []canonical.Block {
	if req == nil {
		return nil
	}
	return []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: ""}},
	}
}
