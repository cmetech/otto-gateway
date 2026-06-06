//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseDotenv_ParsesAndIgnoresCommentsAndBlanks(t *testing.T) {
	body := `# top comment
HTTP_ADDR=:18080

# another
AUTH_TOKEN="quoted value"
EMPTY=
KEY_WITH_EQUALS=foo=bar
`
	got, err := parseDotenv([]byte(body))
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	want := map[string]string{
		"HTTP_ADDR":       ":18080",
		"AUTH_TOKEN":      "quoted value",
		"EMPTY":           "",
		"KEY_WITH_EQUALS": "foo=bar",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: got %q, want %q", k, got[k], v)
		}
	}
}

func TestResolveDashboardURL_DefaultWhenNothingSet(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	tmp := t.TempDir()
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:18080" {
		t.Fatalf("default URL: got %q, want http://127.0.0.1:18080", url)
	}
}

func TestResolveDashboardURL_OverridesEnvFileWins(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	tmp := t.TempDir()
	writeTestFile(t, filepath.Join(tmp, ".env.otto-gw"), "HTTP_ADDR=:19000\n")
	writeTestFile(t, filepath.Join(tmp, ".otto-gw.overrides.env"), "HTTP_ADDR=:19999\n")
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:19999" {
		t.Fatalf("overrides should win: got %q, want :19999", url)
	}
}

func TestResolveDashboardURL_ProcessEnvLowestPriority(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":20000")
	tmp := t.TempDir()
	writeTestFile(t, filepath.Join(tmp, ".env.otto-gw"), "HTTP_ADDR=:21000\n")
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:21000" {
		t.Fatalf("env file should beat process env: got %q, want :21000", url)
	}
}

func TestResolveDashboardURL_NormalizesAnyHostToLoopback(t *testing.T) {
	t.Setenv("HTTP_ADDR", "0.0.0.0:18080")
	tmp := t.TempDir()
	url := resolveDashboardURL(tmp)
	if url != "http://127.0.0.1:18080" {
		t.Fatalf("0.0.0.0:18080 should display as 127.0.0.1:18080, got %q", url)
	}
}
