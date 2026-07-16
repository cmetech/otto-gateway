package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

func TestLoad_ToolAliases_Default_Hermes(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Default (KIRO_TOOL_ALIASES unset) is the Hermes-aligned map: kiro's
	// shell built-in (execute/shell) → Hermes `terminal`, and its file
	// built-ins → read_file/write_file.
	want := map[string]string{
		"execute":  "terminal",
		"shell":    "terminal",
		"fs_read":  "read_file",
		"fs_write": "write_file",
	}
	if len(cfg.ToolAliases) != len(want) {
		t.Fatalf("ToolAliases default = %v, want %v", cfg.ToolAliases, want)
	}
	for k, v := range want {
		if cfg.ToolAliases[k] != v {
			t.Errorf("ToolAliases[%q] = %q, want %q", k, cfg.ToolAliases[k], v)
		}
	}
}

func TestLoad_ToolAliases_Disabled_EmptyString(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	// Explicit empty string disables aliasing (overrides the Hermes default).
	t.Setenv("KIRO_TOOL_ALIASES", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ToolAliases) != 0 {
		t.Errorf("ToolAliases with empty KIRO_TOOL_ALIASES = %v, want empty", cfg.ToolAliases)
	}
}

func TestLoad_ToolAliases_Parsed(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_TOOL_ALIASES", "execute:run_shell, shell:run_shell ,fs_read:read_file")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"execute": "run_shell", "shell": "run_shell", "fs_read": "read_file"}
	if len(cfg.ToolAliases) != len(want) {
		t.Fatalf("ToolAliases = %v, want %v", cfg.ToolAliases, want)
	}
	for k, v := range want {
		if cfg.ToolAliases[k] != v {
			t.Errorf("ToolAliases[%q] = %q, want %q", k, cfg.ToolAliases[k], v)
		}
	}
}

func TestLoad_ToolAliases_Malformed_Errors(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_TOOL_ALIASES", "execute:run_shell,bogus_no_colon")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load: got nil error, want KIRO_TOOL_ALIASES malformed error")
	}
	if !strings.Contains(err.Error(), "KIRO_TOOL_ALIASES") {
		t.Errorf("error should mention KIRO_TOOL_ALIASES; got %v", err)
	}
}
