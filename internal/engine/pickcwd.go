// Package engine — Task 1 placeholder stub for pickCwd. The real
// implementation lands in Task 2 (priority chain per D-16). Keeping a
// minimal stub here lets engine.go reference pickCwd at Task-1 build
// time without a forward-declaration cycle.
//
// THIS FILE IS REPLACED IN TASK 2 with the production implementation.
package engine

import (
	"loop24-gateway/internal/canonical"
)

// pickCwd is replaced in Task 2 with the four-step priority chain.
func pickCwd(req *canonical.ChatRequest, defaultCwd string) string {
	if req != nil && req.WorkingDirOverride != "" {
		return req.WorkingDirOverride
	}
	return defaultCwd
}
