//go:build !darwin && !windows

// Package icon stub for non-tray platforms. The tray binary itself is
// build-tag-excluded on these platforms (see cmd/otto-tray/main_other.go),
// but the icon subpackage stays platform-neutral so `go build ./...`
// from a linux CI host succeeds without needing the embed assets.
package icon

// Template is empty on non-tray platforms — no asset is needed since
// the tray binary will never call into this package on linux/etc.
var Template []byte
