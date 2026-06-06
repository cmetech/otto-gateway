// Package icon embeds the tray status icon. The asset compiles on
// every platform (no build tag) so `go build ./...` from a linux
// CI host still succeeds; the platform-gated main package consumes
// it only when the tray binary is built.
package icon

import _ "embed"

// Template is the raw PNG bytes of the menu-bar / system-tray icon.
//
//go:embed template.png
var Template []byte
