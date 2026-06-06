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
