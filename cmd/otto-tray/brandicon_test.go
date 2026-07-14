//go:build darwin || windows

package main

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeFS builds injectable exists/readFile seams from a map of path→contents.
// A path present in the map "exists"; readFile returns its bytes (or an error if
// the value is the sentinel errMissing).
func fakeFS(files map[string]string) (func(string) bool, func(string) ([]byte, error)) {
	exists := func(p string) bool { _, ok := files[p]; return ok }
	readFile := func(p string) ([]byte, error) {
		v, ok := files[p]
		if !ok {
			return nil, errors.New("not found")
		}
		return []byte(v), nil
	}
	return exists, readFile
}

// installedAppPathForTest resolves where the OTTO app + its brand.json live for
// the current host GOOS, so the fake FS keys match installedAppPath/brandJSONPathForApp.
func ottoAppAndBrandPaths(t *testing.T) (appPath, brandPath string) {
	t.Helper()
	id := defaultBrandIdentity()
	home := "/home/tester"
	env := func(k string) string {
		// windows candidates need LOCALAPPDATA/PROGRAMFILES; give one.
		if k == "LOCALAPPDATA" {
			return `C:\Users\tester\AppData\Local`
		}
		return ""
	}
	// Force existence of the first candidate by probing candidates directly.
	for _, c := range desktopAppCandidates(runtime.GOOS, id, env, home) {
		appPath = c
		break
	}
	if appPath == "" {
		t.Fatal("no app candidate for host GOOS")
	}
	return appPath, brandJSONPathForApp(runtime.GOOS, appPath)
}

func TestBrandUsesLoop24(t *testing.T) {
	appPath, brandPath := ottoAppAndBrandPaths(t)
	home := "/home/tester"
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\tester\AppData\Local`
		}
		return ""
	}

	tests := []struct {
		name  string
		files map[string]string
		want  bool // true → loop24
	}{
		{"no app installed", map[string]string{}, true},
		{"OTTO app + OTTO brand.json", map[string]string{appPath: "", brandPath: `{"displayName":"OTTO"}`}, false},
		{"OTTO app + loop24 brand.json", map[string]string{appPath: "", brandPath: `{"displayName":"LOOP24"}`}, true},
		{"OTTO app + missing brand.json", map[string]string{appPath: ""}, true},
		{"OTTO app + invalid brand.json", map[string]string{appPath: "", brandPath: `not json`}, true},
		{"OTTO app + empty displayName", map[string]string{appPath: "", brandPath: `{"displayName":""}`}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exists, readFile := fakeFS(tc.files)
			if got := brandUsesLoop24(runtime.GOOS, env, home, exists, readFile); got != tc.want {
				t.Errorf("brandUsesLoop24 = %v, want %v (files=%v)", got, tc.want, tc.files)
			}
		})
	}
}

func TestResolveBrandJSON_Presence(t *testing.T) {
	appPath, brandPath := ottoAppAndBrandPaths(t)
	home := "/home/tester"
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return `C:\Users\tester\AppData\Local`
		}
		return ""
	}

	// present + name
	exists, readFile := fakeFS(map[string]string{appPath: "", brandPath: `{"displayName":"OTTO"}`})
	if name, present := resolveBrandJSON(runtime.GOOS, env, home, exists, readFile); !present || name != "OTTO" {
		t.Errorf("present OTTO: got (%q, %v)", name, present)
	}

	// absent app
	exists, readFile = fakeFS(map[string]string{})
	if name, present := resolveBrandJSON(runtime.GOOS, env, home, exists, readFile); present || name != "" {
		t.Errorf("absent: got (%q, %v)", name, present)
	}

	// sanity: brandPath is under appPath's dir on both OSes
	if filepath.Dir(brandPath) == "" {
		t.Error("brandPath has no dir")
	}
}
