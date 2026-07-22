//go:build darwin || windows

package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDesktopAppCandidates(t *testing.T) {
	id := defaultBrandIdentity()
	env := func(k string) string {
		switch k {
		case "LOCALAPPDATA":
			return `C:\Users\me\AppData\Local`
		}
		return ""
	}
	win := desktopAppCandidates("windows", id, env, "")
	if len(win) == 0 || filepath.Base(win[0]) != "OTTO.exe" {
		t.Fatalf("windows candidates bad: %v", win)
	}
	mac := desktopAppCandidates("darwin", id, func(string) string { return "" }, "/Users/me")
	if len(mac) < 2 || mac[0] != "/Applications/OTTO.app" {
		t.Fatalf("darwin candidates bad: %v", mac)
	}
}

func TestInstalledAppPath(t *testing.T) {
	id := defaultBrandIdentity()
	present := "/Applications/OTTO.app"
	exists := func(p string) bool { return p == present }
	got := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", exists)
	if got != present {
		t.Fatalf("expected %q, got %q", present, got)
	}
	none := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", func(string) bool { return false })
	if none != "" {
		t.Fatalf("expected empty, got %q", none)
	}
}

func TestResolveDesktopIdentity(t *testing.T) {
	const present = "/Applications/OTTO.app"
	exists := func(p string) bool { return p == present }
	// The tray resolves the desktop app from fixed OTTO defaults and never
	// reads brand.json (quick task 260721-an5).
	id, appPath := resolveDesktopIdentity("darwin", func(string) string { return "" }, "/Users/me", exists)
	if id.DisplayName != "OTTO" {
		t.Fatalf("expected DisplayName OTTO, got %q", id.DisplayName)
	}
	if appPath != present {
		t.Fatalf("expected appPath %q, got %q", present, appPath)
	}

	notFound := func(string) bool { return false }
	_, appPath = resolveDesktopIdentity("darwin", func(string) string { return "" }, "/Users/me", notFound)
	if appPath != "" {
		t.Fatalf("expected empty appPath when not installed, got %q", appPath)
	}
}

func TestDiscoverDesktopCandidatesWindowsLoop24(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	appDir := filepath.Join(root, "Programs", "LOOP24")
	desc := filepath.Join(appDir, "resources", "brand.json")
	exe := filepath.Join(appDir, "LOOP24.exe")
	deps := desktopDiscoveryDeps{
		glob: func(string) ([]string, error) { return []string{desc, desc}, nil },
		readFile: func(string) ([]byte, error) {
			return []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`), nil
		},
		exists: func(path string) bool { return path == exe },
	}
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return root
		}
		return ""
	}
	got, err := discoverDesktopCandidates("windows", env, "", deps)
	if err != nil || len(got) != 1 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
	if got[0].Slug != "loop24" || got[0].Identity.WinExeName != "LOOP24.exe" || got[0].AppPath != exe {
		t.Fatalf("candidate = %+v", got[0])
	}
}

func TestDiscoverDesktopCandidatesMacLoop24(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "me")
	app := filepath.Join(home, "Applications", "LOOP24.app")
	desc := filepath.Join(app, "Contents", "Resources", "brand.json")
	exe := filepath.Join(app, "Contents", "MacOS", "LOOP24")
	deps := desktopDiscoveryDeps{
		glob: func(string) ([]string, error) { return []string{desc}, nil },
		readFile: func(string) ([]byte, error) {
			return []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"OTTO"}`), nil
		},
		exists: func(path string) bool { return path == exe },
	}

	got, err := discoverDesktopCandidates("darwin", func(string) string { return "" }, home, deps)
	if err != nil || len(got) != 1 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
	if got[0].Identity.MacProcMatch != "LOOP24.app/Contents/MacOS/LOOP24" ||
		got[0].AppPath != app || got[0].DescriptorPath != desc {
		t.Fatalf("candidate = %+v", got[0])
	}
}

func TestDiscoverDesktopCandidatesIgnoresRejectedDescriptors(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	tests := []struct {
		name   string
		raw    string
		exists bool
	}{
		{
			name:   "missing executable",
			raw:    `{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`,
			exists: false,
		},
		{
			name:   "invalid descriptor",
			raw:    `{"schemaVersion":2,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`,
			exists: true,
		},
		{
			name:   "foreign gateway",
			raw:    `{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"other"}`,
			exists: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appDir := filepath.Join(root, "Programs", "LOOP24")
			desc := filepath.Join(appDir, "resources", "brand.json")
			exe := filepath.Join(appDir, "LOOP24.exe")
			got, err := discoverDesktopCandidates("windows", func(k string) string {
				if k == "LOCALAPPDATA" {
					return root
				}
				return ""
			}, "", desktopDiscoveryDeps{
				glob:     func(string) ([]string, error) { return []string{desc}, nil },
				readFile: func(string) ([]byte, error) { return []byte(tt.raw), nil },
				exists:   func(path string) bool { return tt.exists && path == exe },
			})
			if err != nil || len(got) != 0 {
				t.Fatalf("candidates = %+v, err=%v", got, err)
			}
		})
	}
}

func TestDiscoverDesktopCandidatesReturnsReadFailureWithoutPartialResult(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	loopDir := filepath.Join(root, "Programs", "LOOP24")
	badDir := filepath.Join(root, "Programs", "BROKEN")
	loopDesc := filepath.Join(loopDir, "resources", "brand.json")
	badDesc := filepath.Join(badDir, "resources", "brand.json")
	wantErr := errors.New("read failed")
	calls := 0
	got, err := discoverDesktopCandidates("windows", func(k string) string {
		if k == "LOCALAPPDATA" {
			return root
		}
		return ""
	}, "", desktopDiscoveryDeps{
		glob: func(string) ([]string, error) {
			calls++
			if calls == 1 {
				return []string{loopDesc, badDesc}, nil
			}
			return nil, nil
		},
		readFile: func(path string) ([]byte, error) {
			if path == badDesc {
				return nil, wantErr
			}
			return []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`), nil
		},
		exists: func(path string) bool { return path == filepath.Join(loopDir, "LOOP24.exe") },
	})
	if !errors.Is(err, wantErr) || len(got) != 0 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
}

func TestDiscoverDesktopCandidatesReturnsGlobFailureWithoutPartialResult(t *testing.T) {
	wantErr := errors.New("glob failed")
	calls := 0
	got, err := discoverDesktopCandidates("windows", func(k string) string {
		if k == "LOCALAPPDATA" {
			return filepath.Join("C:", "Users", "me", "AppData", "Local")
		}
		return ""
	}, "", desktopDiscoveryDeps{
		glob: func(string) ([]string, error) {
			calls++
			if calls == 2 {
				return nil, wantErr
			}
			return nil, nil
		},
		readFile: func(string) ([]byte, error) { return nil, nil },
		exists:   func(string) bool { return false },
	})
	if !errors.Is(err, wantErr) || len(got) != 0 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
}

func TestDiscoverDesktopCandidatesDescriptorOTTOReplacesLegacyFallback(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	appDir := filepath.Join(root, "Programs", "OTTO")
	desc := filepath.Join(appDir, "resources", "brand.json")
	exe := filepath.Join(appDir, "OTTO.exe")
	got, err := discoverDesktopCandidates("windows", func(k string) string {
		if k == "LOCALAPPDATA" {
			return root
		}
		return ""
	}, "", desktopDiscoveryDeps{
		glob: func(string) ([]string, error) { return []string{desc}, nil },
		readFile: func(string) ([]byte, error) {
			return []byte(`{"schemaVersion":1,"slug":"otto","displayName":"OTTO","homeDir":".otto","gateway":"otto"}`), nil
		},
		exists: func(path string) bool { return path == exe },
	})
	if err != nil || len(got) != 1 || got[0].DescriptorPath != desc {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
}

func TestDiscoverDesktopCandidatesRejectedCanonicalOTTONeverFallsBack(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	appDir := filepath.Join(root, "Programs", "OTTO")
	desc := filepath.Join(appDir, "resources", "brand.json")
	exe := filepath.Join(appDir, "OTTO.exe")
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "invalid descriptor",
			raw:  `{"schemaVersion":2,"slug":"otto","displayName":"OTTO","homeDir":".otto","gateway":"otto"}`,
		},
		{
			name: "foreign gateway",
			raw:  `{"schemaVersion":1,"slug":"otto","displayName":"OTTO","homeDir":".otto","gateway":"other"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := discoverDesktopCandidates("windows", func(k string) string {
				if k == "LOCALAPPDATA" {
					return root
				}
				return ""
			}, "", desktopDiscoveryDeps{
				glob:     func(string) ([]string, error) { return []string{desc}, nil },
				readFile: func(string) ([]byte, error) { return []byte(tt.raw), nil },
				exists:   func(path string) bool { return path == exe },
			})
			if err != nil || len(got) != 0 {
				t.Fatalf("candidates = %+v, err=%v", got, err)
			}
		})
	}
}

func TestDiscoverDesktopCandidatesLegacyOTTOFallback(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	exe := filepath.Join(root, "Programs", "OTTO", "OTTO.exe")
	got, err := discoverDesktopCandidates("windows", func(k string) string {
		if k == "LOCALAPPDATA" {
			return root
		}
		return ""
	}, "", desktopDiscoveryDeps{
		glob:     func(string) ([]string, error) { return nil, nil },
		readFile: func(string) ([]byte, error) { return nil, nil },
		exists:   func(path string) bool { return path == exe },
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
	if got[0].Slug != "otto" || got[0].HomeDir != ".otto" || got[0].AppPath != exe || got[0].DescriptorPath != "" {
		t.Fatalf("fallback candidate = %+v", got[0])
	}
}

func TestDiscoverDesktopCandidatesUsesBoundedPatterns(t *testing.T) {
	roots := map[string]string{
		"LOCALAPPDATA":      filepath.Join("C:", "Local"),
		"PROGRAMFILES":      filepath.Join("C:", "Program Files"),
		"PROGRAMFILES(X86)": filepath.Join("C:", "Program Files (x86)"),
	}
	var gotPatterns []string
	_, err := discoverDesktopCandidates("windows", func(k string) string { return roots[k] }, "", desktopDiscoveryDeps{
		glob: func(pattern string) ([]string, error) {
			gotPatterns = append(gotPatterns, pattern)
			return nil, nil
		},
		readFile: func(string) ([]byte, error) { return nil, nil },
		exists:   func(string) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPatterns := []string{
		filepath.Join(roots["LOCALAPPDATA"], "Programs", "*", "resources", "brand.json"),
		filepath.Join(roots["LOCALAPPDATA"], "*", "resources", "brand.json"),
		filepath.Join(roots["PROGRAMFILES"], "Programs", "*", "resources", "brand.json"),
		filepath.Join(roots["PROGRAMFILES"], "*", "resources", "brand.json"),
		filepath.Join(roots["PROGRAMFILES(X86)"], "Programs", "*", "resources", "brand.json"),
		filepath.Join(roots["PROGRAMFILES(X86)"], "*", "resources", "brand.json"),
	}
	if !reflect.DeepEqual(gotPatterns, wantPatterns) {
		t.Fatalf("patterns = %v, want %v", gotPatterns, wantPatterns)
	}
}
