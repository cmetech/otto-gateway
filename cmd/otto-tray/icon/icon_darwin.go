//go:build darwin

// Package icon embeds the tray status icon. Per-OS embed: darwin uses
// the PNG (Cocoa's NSStatusItem accepts PNG bytes directly and the
// template flag from systray.SetTemplateIcon adapts it to dark/light
// menu bars). Windows uses an ICO — see icon_windows.go for why
// PNG-on-Windows fails with "unable to set icon: The operation
// completed successfully" (LoadImageW with LR_LOADFROMFILE rejects
// PNG bytes, leaving GetLastError == ERROR_SUCCESS == 0).
package icon

import _ "embed"

// Template is the raw PNG bytes of the menu-bar template icon.
//
//go:embed template.png
var Template []byte

// Running is the raw PNG bytes for the "gateway running" state icon.
//
//go:embed template_running.png
var Running []byte

// Warning is the raw PNG bytes for the "gateway starting/degraded" state icon.
//
//go:embed template_warning.png
var Warning []byte

// Error is the raw PNG bytes for the "gateway stopped/error" state icon.
//
//go:embed template_error.png
var Error []byte

// Loop24 is the raw PNG bytes of the loop24 brand mark (colored, non-template).
// Used via SetIcon (not SetTemplateIcon) so the blue mark shows on the menu bar.
//
//go:embed loop24.png
var Loop24 []byte
