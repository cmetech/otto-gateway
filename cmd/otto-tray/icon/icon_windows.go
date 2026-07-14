//go:build windows

// Package icon embeds the tray status icon. See icon_darwin.go for
// the design rationale around per-OS embeds.
//
// Windows: energye/systray.SetIcon writes the bytes to a temp file
// and calls LoadImageW(IMAGE_ICON, LR_LOADFROMFILE). LoadImageW
// rejects PNG payloads silently (LoadImage returns NULL, GetLastError
// is ERROR_SUCCESS — the source of the famous "unable to set icon:
// The operation completed successfully" log line). We ship an actual
// ICO so the Win32 loader accepts it. The template.ico asset is a
// 16x16 32-bit ARGB hollow-circle "O" placeholder; replace with a
// branded glyph when one ships.
package icon

import _ "embed"

// Template is the raw ICO bytes of the system-tray icon.
//
//go:embed template.ico
var Template []byte

// Running is the raw ICO bytes for the "gateway running" state icon.
//
//go:embed running.ico
var Running []byte

// Warning is the raw ICO bytes for the "gateway starting/degraded" state icon.
//
//go:embed warning.ico
var Warning []byte

// Error is the raw ICO bytes for the "gateway stopped/error" state icon.
//
//go:embed error.ico
var Error []byte

// Loop24 is the raw ICO bytes of the loop24 brand mark (colored, multi-size).
//
//go:embed loop24.ico
var Loop24 []byte
