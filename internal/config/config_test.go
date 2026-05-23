package config_test

import (
	"testing"
	"time"

	"loop24-gateway/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	// No t.Setenv, safe to run in parallel.
	t.Parallel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":11434" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, ":11434")
	}
	if cfg.KiroCmd != "kiro-cli" {
		t.Errorf("KiroCmd: got %q, want %q", cfg.KiroCmd, "kiro-cli")
	}
	if len(cfg.KiroArgs) != 1 || cfg.KiroArgs[0] != "acp" {
		t.Errorf("KiroArgs: got %v, want [acp]", cfg.KiroArgs)
	}
	if cfg.KiroCWD != "" {
		t.Errorf("KiroCWD: got %q, want empty", cfg.KiroCWD)
	}
	if cfg.Debug != false {
		t.Errorf("Debug: got %v, want false", cfg.Debug)
	}
	if cfg.PingInterval != 60*time.Second {
		t.Errorf("PingInterval: got %v, want 60s", cfg.PingInterval)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().

	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("KIRO_CMD", "/usr/local/bin/kiro-cli")
	t.Setenv("KIRO_ARGS", "acp --verbose")
	t.Setenv("KIRO_CWD", "/tmp/test")
	t.Setenv("DEBUG", "true")
	t.Setenv("PING_INTERVAL", "30s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.KiroCmd != "/usr/local/bin/kiro-cli" {
		t.Errorf("KiroCmd: got %q, want %q", cfg.KiroCmd, "/usr/local/bin/kiro-cli")
	}
	if len(cfg.KiroArgs) != 2 || cfg.KiroArgs[0] != "acp" || cfg.KiroArgs[1] != "--verbose" {
		t.Errorf("KiroArgs: got %v, want [acp --verbose]", cfg.KiroArgs)
	}
	if cfg.KiroCWD != "/tmp/test" {
		t.Errorf("KiroCWD: got %q, want %q", cfg.KiroCWD, "/tmp/test")
	}
	if !cfg.Debug {
		t.Error("Debug: got false, want true")
	}
	if cfg.PingInterval != 30*time.Second {
		t.Errorf("PingInterval: got %v, want 30s", cfg.PingInterval)
	}
}

func TestLoadEnvBool(t *testing.T) {
	// t.Setenv used in sub-tests: subtests manage their own env isolation.

	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"0", false},
		{"false", false},
		{"FALSE", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.val, func(t *testing.T) {
			// t.Setenv: cannot use t.Parallel() in sub-test either.
			t.Setenv("DEBUG", tc.val)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load() returned unexpected error for DEBUG=%q: %v", tc.val, err)
			}
			if cfg.Debug != tc.want {
				t.Errorf("DEBUG=%q: got Debug=%v, want %v", tc.val, cfg.Debug, tc.want)
			}
		})
	}
}

func TestLoadEnvDurationGoString(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().

	t.Setenv("PING_INTERVAL", "2m30s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := 2*time.Minute + 30*time.Second
	if cfg.PingInterval != want {
		t.Errorf("PingInterval: got %v, want %v", cfg.PingInterval, want)
	}
}

func TestLoadEnvDurationMilliseconds(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Node compat: integer value is interpreted as milliseconds.

	t.Setenv("PING_INTERVAL", "60000")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.PingInterval != 60*time.Second {
		t.Errorf("PingInterval: got %v, want 60s", cfg.PingInterval)
	}
}

func TestLoadInvalidPingInterval(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// An env var that is set but unparseable must cause Load() to return an error.

	t.Setenv("PING_INTERVAL", "abc")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for PING_INTERVAL=abc, got nil")
	}
}

func TestLoadInvalidDebug(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().

	t.Setenv("DEBUG", "yes_please")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for DEBUG=yes_please, got nil")
	}
}

func TestLogLevel(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Debug: false}
	if cfg.LogLevel().String() != "INFO" {
		t.Errorf("LogLevel(): got %q, want INFO", cfg.LogLevel().String())
	}

	cfg.Debug = true
	if cfg.LogLevel().String() != "DEBUG" {
		t.Errorf("LogLevel(): got %q, want DEBUG", cfg.LogLevel().String())
	}
}
