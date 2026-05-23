// Package version exposes build-time version information embedded via -ldflags.
package version

import "runtime/debug"

// Version is set at build time via:
//
//	-ldflags="-X loop24-gateway/internal/version.Version=1.2.3"
//
// Falls back to "0.0.0-dev" for local builds without -ldflags.
var Version = "0.0.0-dev"

// Commit returns the first 7 characters of the VCS commit hash from build metadata.
// Returns "unknown" if build info is unavailable (e.g., `go run` without a module).
func Commit() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return s.Value[:7]
			}
		}
	}
	return "unknown"
}
