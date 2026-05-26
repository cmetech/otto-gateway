package config_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	// No t.Setenv, safe to run in parallel.
	t.Parallel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:11435" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, "127.0.0.1:11435")
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

// --- AUTH_TOKEN coverage ------------------------------------------------

func TestLoad_AuthToken_Empty(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Unset env var → AuthToken should be nil/zero-length (auth disabled, Node parity).
	t.Setenv("AUTH_TOKEN", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if len(cfg.AuthToken) != 0 {
		t.Errorf("AuthToken: got %v, want empty", cfg.AuthToken)
	}
}

func TestLoad_AuthToken_Multiple(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("AUTH_TOKEN", "a,b,c")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(cfg.AuthToken, want) {
		t.Errorf("AuthToken: got %v, want %v", cfg.AuthToken, want)
	}
}

func TestLoad_AuthToken_WithSpaces(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Whitespace around comma-split entries must be trimmed; empties dropped.
	t.Setenv("AUTH_TOKEN", " a , b ")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(cfg.AuthToken, want) {
		t.Errorf("AuthToken: got %v, want %v", cfg.AuthToken, want)
	}
}

// --- ALLOWED_IPS coverage -----------------------------------------------

func TestLoad_AllowedIPs_Empty(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ALLOWED_IPS", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if len(cfg.AllowedIPs) != 0 {
		t.Errorf("AllowedIPs: got %v, want empty", cfg.AllowedIPs)
	}
}

func TestLoad_AllowedIPs_CIDRMix(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Mix of v4 CIDR, bare v4 (→ /32), and v6 CIDR.
	t.Setenv("ALLOWED_IPS", "10.0.0.0/8,192.168.1.1,::1/128")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	got := make([]string, 0, len(cfg.AllowedIPs))
	for _, p := range cfg.AllowedIPs {
		got = append(got, p.String())
	}
	sort.Strings(got)
	want := []string{"10.0.0.0/8", "192.168.1.1/32", "::1/128"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedIPs: got %v, want %v", got, want)
	}
}

func TestLoad_AllowedIPs_Malformed(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Set-but-unparseable must surface as a Load() error (T-02-11 mitigation).
	t.Setenv("ALLOWED_IPS", "not-an-ip")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for ALLOWED_IPS=not-an-ip, got nil")
	}
	if !strings.Contains(err.Error(), "ALLOWED_IPS") {
		t.Errorf("error should mention ALLOWED_IPS, got: %v", err)
	}
}

// --- POOL_SIZE coverage -------------------------------------------------

func TestLoad_PoolSize_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Phase 5 POOL-01: env default flipped from 1 to 4 for Node parity.
	t.Setenv("POOL_SIZE", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.PoolSize != 4 {
		t.Errorf("PoolSize: got %d, want 4 (Phase 5 POOL-01 Node-parity default)", cfg.PoolSize)
	}
}

func TestLoad_PoolSize_Override(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("POOL_SIZE", "4")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.PoolSize != 4 {
		t.Errorf("PoolSize: got %d, want 4", cfg.PoolSize)
	}
}

func TestLoad_PoolSize_Malformed(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// T-02-12 mitigation: a non-integer must error at startup, not silently default.
	t.Setenv("POOL_SIZE", "abc")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for POOL_SIZE=abc, got nil")
	}
	if !strings.Contains(err.Error(), "POOL_SIZE") {
		t.Errorf("error should mention POOL_SIZE, got: %v", err)
	}
}

// --- OLLAMA_PATH_PREFIX / OPENAI_PATH_PREFIX coverage -------------------

func TestLoad_OllamaPathPrefix_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("OLLAMA_PATH_PREFIX", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.OllamaPathPrefix != "/api" {
		t.Errorf("OllamaPathPrefix: got %q, want %q", cfg.OllamaPathPrefix, "/api")
	}
}

func TestLoad_OllamaPathPrefix_Override(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("OLLAMA_PATH_PREFIX", "/ollama")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.OllamaPathPrefix != "/ollama" {
		t.Errorf("OllamaPathPrefix: got %q, want %q", cfg.OllamaPathPrefix, "/ollama")
	}
}

func TestLoad_OpenAIPathPrefix_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("OPENAI_PATH_PREFIX", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.OpenAIPathPrefix != "/v1" {
		t.Errorf("OpenAIPathPrefix: got %q, want %q", cfg.OpenAIPathPrefix, "/v1")
	}
}

// --- AUTH_TRUST_XFF coverage (Codex H-7) --------------------------------

func TestLoad_AuthTrustXFF_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Default false: laptop deployments are safe-by-default (T-02-39 mitigation).
	t.Setenv("AUTH_TRUST_XFF", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.AuthTrustXFF {
		t.Errorf("AuthTrustXFF: got true, want false (safe-by-default)")
	}
}

func TestLoad_AuthTrustXFF_True(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Opt-in path: only takes effect when operator deliberately sets the flag.
	t.Setenv("AUTH_TRUST_XFF", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if !cfg.AuthTrustXFF {
		t.Errorf("AuthTrustXFF: got false, want true (AUTH_TRUST_XFF=true)")
	}
}

func TestLoad_AuthTrustXFF_Malformed(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// T-02-39 mitigation: a non-boolean value must surface as startup error so
	// operator typos don't silently fall back to "ambiguous default" state.
	t.Setenv("AUTH_TRUST_XFF", "not-a-bool")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for AUTH_TRUST_XFF=not-a-bool, got nil")
	}
	if !strings.Contains(err.Error(), "AUTH_TRUST_XFF") {
		t.Errorf("error should mention AUTH_TRUST_XFF, got: %v", err)
	}
}

// --- ENABLED_SURFACES coverage (Phase 3.1 D-16) -------------------------

func TestLoad_EnabledSurfaces_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-16: default is ollama,anthropic,openai (Phase 3 widened from ollama,anthropic).
	t.Setenv("ENABLED_SURFACES", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := []string{"ollama", "anthropic", "openai"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v", cfg.EnabledSurfaces, want)
	}
}

func TestLoad_EnabledSurfaces_OllamaOnly(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ENABLED_SURFACES", "ollama")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := []string{"ollama"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v (anthropic must be disabled)", cfg.EnabledSurfaces, want)
	}
}

func TestLoad_EnabledSurfaces_AnthropicOnly(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ENABLED_SURFACES", "anthropic")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := []string{"anthropic"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v (ollama must be disabled)", cfg.EnabledSurfaces, want)
	}
}

func TestLoad_EnabledSurfaces_UnknownName_Errors(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-16 fail-fast: an unknown surface (typo) must cause Load() to
	// error so the binary exits non-zero rather than silently disabling
	// a surface (RESEARCH.md Pitfall 10). Allow-list is {"ollama",
	// "anthropic", "openai"} after Phase 3 widening.
	t.Setenv("ENABLED_SURFACES", "ollama,olama")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for ENABLED_SURFACES=ollama,olama (typo), got nil")
	}
	if !strings.Contains(err.Error(), "ENABLED_SURFACES") {
		t.Errorf("error should mention ENABLED_SURFACES, got: %v", err)
	}
	if !strings.Contains(err.Error(), "olama") {
		t.Errorf("error should name the offending surface 'olama', got: %v", err)
	}
}

func TestLoad_EnabledSurfaces_OpenAIOnly(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-05: "openai" is now in the allow-list; enabling only openai
	// must succeed without error.
	t.Setenv("ENABLED_SURFACES", "openai")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error for ENABLED_SURFACES=openai: %v", err)
	}
	want := []string{"openai"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v", cfg.EnabledSurfaces, want)
	}
}

func TestLoad_EnabledSurfaces_AllThree(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-05: all three surfaces together must be accepted.
	t.Setenv("ENABLED_SURFACES", "openai,ollama,anthropic")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error for ENABLED_SURFACES=openai,ollama,anthropic: %v", err)
	}
	want := []string{"openai", "ollama", "anthropic"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v", cfg.EnabledSurfaces, want)
	}
}

func TestLoad_EnabledSurfaces_OpenAI_Typo_Errors(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-05 fail-fast: "openia" (typo for openai) must still fail.
	t.Setenv("ENABLED_SURFACES", "openia")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for ENABLED_SURFACES=openia (typo), got nil")
	}
	if !strings.Contains(err.Error(), "openia") {
		t.Errorf("error should name the offending surface 'openia', got: %v", err)
	}
}

// --- ANTHROPIC_PATH_PREFIX coverage (Phase 3.1 D-19) --------------------

func TestLoad_AnthropicPathPrefix_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// D-19: default is /v1 (shares with OpenAI; SURF-08 endpoint-level
	// disambiguation).
	t.Setenv("ANTHROPIC_PATH_PREFIX", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.AnthropicPathPrefix != "/v1" {
		t.Errorf("AnthropicPathPrefix: got %q, want %q", cfg.AnthropicPathPrefix, "/v1")
	}
}

func TestLoad_AnthropicPathPrefix_Override(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ANTHROPIC_PATH_PREFIX", "/anthropic/v1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.AnthropicPathPrefix != "/anthropic/v1" {
		t.Errorf("AnthropicPathPrefix: got %q, want %q", cfg.AnthropicPathPrefix, "/anthropic/v1")
	}
}
