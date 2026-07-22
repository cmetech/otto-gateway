package config_test

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/config"
	gatewayembed "otto-gateway/internal/embed"
)

func TestLoadDefaults(t *testing.T) {
	// No t.Setenv, safe to run in parallel.
	t.Parallel()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:18080" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, "127.0.0.1:18080")
	}
	// Phase 18-01 D-18-02: KIRO_CMD now passes through exec.LookPath
	// validation. TestMain (testmain_test.go) stamps KIRO_CMD="go" for
	// the package-wide default so the default-load path remains valid on
	// CI runners without kiro-cli installed. Assertion follows the
	// stamped value rather than the production default "kiro-cli".
	if cfg.KiroCmd != "go" {
		t.Errorf("KiroCmd: got %q, want %q (TestMain stamped)", cfg.KiroCmd, "go")
	}
	wantArgs := []string{"acp", "--agent", "acp_proxy"}
	if !reflect.DeepEqual(cfg.KiroArgs, wantArgs) {
		t.Errorf("KiroArgs: got %v, want %v", cfg.KiroArgs, wantArgs)
	}
	wantCWD, err := gatewayembed.GatewayDir()
	if err != nil {
		t.Fatalf("GatewayDir: %v", err)
	}
	if cfg.KiroCWD != wantCWD || !cfg.KiroCWDIsDefault {
		t.Errorf("KiroCWD: got %q default=%v, want %q default=true", cfg.KiroCWD, cfg.KiroCWDIsDefault, wantCWD)
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

	// Phase 18-01 D-18-02: KIRO_CMD now passes through exec.LookPath
	// validation and KIRO_CWD is os.Stat-checked. Use real, present
	// values so the override-takes-effect assertion remains the focus
	// of this test (test what we're testing, not env validation).
	tmpDir := t.TempDir()
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("KIRO_CMD", "go") // present in PATH on dev + CI
	t.Setenv("KIRO_ARGS", "acp --verbose")
	t.Setenv("KIRO_CWD", tmpDir)
	t.Setenv("DEBUG", "true")
	t.Setenv("PING_INTERVAL", "30s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr: got %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.KiroCmd != "go" {
		t.Errorf("KiroCmd: got %q, want %q", cfg.KiroCmd, "go")
	}
	if len(cfg.KiroArgs) != 2 || cfg.KiroArgs[0] != "acp" || cfg.KiroArgs[1] != "--verbose" {
		t.Errorf("KiroArgs: got %v, want [acp --verbose]", cfg.KiroArgs)
	}
	if cfg.KiroCWD != tmpDir {
		t.Errorf("KiroCWD: got %q, want %q", cfg.KiroCWD, tmpDir)
	}
	if cfg.KiroCWDIsDefault {
		t.Error("KiroCWDIsDefault: got true, want false for explicit KIRO_CWD")
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

// --- KIRO_WORKER_MAX_TURNS coverage --------------------------------------

func TestLoad_KiroWorkerMaxTurns(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "default disabled", value: "", want: 0},
		{name: "enabled", value: "20", want: 20},
		{name: "negative", value: "-1", wantErr: "KIRO_WORKER_MAX_TURNS: must be >= 0"},
		{name: "above cap", value: "10001", wantErr: "KIRO_WORKER_MAX_TURNS: sanity cap exceeded (max 10000)"},
		{name: "malformed", value: "twenty", wantErr: "KIRO_WORKER_MAX_TURNS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KIRO_WORKER_MAX_TURNS", tc.value)
			cfg, err := config.Load()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v; want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.KiroWorkerMaxTurns != tc.want {
				t.Fatalf("KiroWorkerMaxTurns = %d; want %d", cfg.KiroWorkerMaxTurns, tc.want)
			}
		})
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

// --- CHAT_TRACE coverage (quick 260529-ll2) -----------------------------

func TestLoad_ChatTrace_DefaultDisabled(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// CHAT_TRACE defaults to false so the two-knob design stays safe-
	// by-default (raw user content never written without operator opt-in).
	t.Setenv("CHAT_TRACE", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.ChatTrace {
		t.Errorf("ChatTrace: got true, want false (default-disabled)")
	}
	if cfg.ChatTraceMaxAgeDays != 3 {
		t.Errorf("ChatTraceMaxAgeDays: got %d, want 3 (default)", cfg.ChatTraceMaxAgeDays)
	}
}

func TestLoad_ChatTraceFile_DefaultFromLogFile(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// When LOG_FILE is set, the chat-trace default sibling-file derivation
	// must use the same directory + basename + "-chat-trace.log" suffix.
	t.Setenv("LOG_FILE", "/tmp/x/y.log")
	t.Setenv("CHAT_TRACE_FILE", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := "/tmp/x/y-chat-trace.log"
	if cfg.ChatTraceFile != want {
		t.Errorf("ChatTraceFile: got %q, want %q", cfg.ChatTraceFile, want)
	}
}

func TestLoad_ChatTraceFile_DefaultWhenLogFileUnset(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// When LOG_FILE is unset, default to the packaged distribution path.
	t.Setenv("LOG_FILE", "")
	t.Setenv("CHAT_TRACE_FILE", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	want := "./logs/gateway-chat-trace.log"
	if cfg.ChatTraceFile != want {
		t.Errorf("ChatTraceFile: got %q, want %q", cfg.ChatTraceFile, want)
	}
}

func TestLoad_ChatTraceMaxAgeDays_InvalidParse_Errors(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Set-but-unparseable CHAT_TRACE_MAX_AGE_DAYS must surface as a Load
	// error (matches getEnvInt semantics for POOL_SIZE et al.).
	t.Setenv("CHAT_TRACE_MAX_AGE_DAYS", "not-a-number")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for CHAT_TRACE_MAX_AGE_DAYS=not-a-number, got nil")
	}
	if !strings.Contains(err.Error(), "CHAT_TRACE_MAX_AGE_DAYS") {
		t.Errorf("error should name the offending env var, got: %v", err)
	}
}

func TestLoad_ChatTrace_RejectsUnwritableParent(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// When CHAT_TRACE=true and the resolved CHAT_TRACE_FILE parent
	// directory cannot be created (e.g., a regular FILE already exists at
	// the parent path), Load must return an error naming the env var and
	// the failed parent.
	t.Setenv("CHAT_TRACE", "true")
	// Create a regular file at a path; then ask CHAT_TRACE_FILE to live
	// "inside" that path so MkdirAll fails.
	tmp := t.TempDir()
	blocker := tmp + "/blocker"
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	t.Setenv("CHAT_TRACE_FILE", blocker+"/sub/chat-trace.log")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error when CHAT_TRACE_FILE parent is unwritable, got nil")
	}
	if !strings.Contains(err.Error(), "CHAT_TRACE_FILE") {
		t.Errorf("error should name CHAT_TRACE_FILE, got: %v", err)
	}
}

func TestLoad_ChatTrace_AllowsChatTraceHookInAllowlist_WhenDisabled(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Forward-compat: when CHAT_TRACE=false but operator left
	// ChatTraceHook in their ENABLED_HOOKS allowlist, Load must SILENTLY
	// drop the entry rather than fail chain.Filter at boot. main.go does
	// not wire ChatTraceHook into the chain when CHAT_TRACE=false, so
	// chain.Filter would otherwise error on a missing hook.
	t.Setenv("CHAT_TRACE", "false")
	t.Setenv("ENABLED_HOOKS", "ChatTraceHook,LoggingHook")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	for _, h := range cfg.EnabledHooks {
		if h == "ChatTraceHook" {
			t.Errorf("EnabledHooks should drop ChatTraceHook when CHAT_TRACE=false; got %v", cfg.EnabledHooks)
		}
	}
}

// --- ACP_CAPTURE_RUNTIME coverage -------------------------------------------

func TestLoad_AcpCaptureRuntime(t *testing.T) {
	t.Setenv("ACP_CAPTURE_RUNTIME", "true")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AcpCaptureRuntime {
		t.Fatal("ACP_CAPTURE_RUNTIME=true did not set cfg.AcpCaptureRuntime")
	}
}

func TestLoad_AcpCaptureRuntime_DefaultsFalse(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AcpCaptureRuntime {
		t.Fatal("ACP_CAPTURE_RUNTIME unset should default to false")
	}
}

// --- STREAM_IDLE_TIMEOUT_SEC coverage (quick 260531-ruv) ---------------

func TestLoad_StreamIdleTimeoutSec_Default(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.StreamIdleTimeoutSec != 30 {
		t.Errorf("StreamIdleTimeoutSec: got %d, want 30 (default)", cfg.StreamIdleTimeoutSec)
	}
}

func TestLoad_StreamIdleTimeoutSec_Explicit(t *testing.T) {
	t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "60")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.StreamIdleTimeoutSec != 60 {
		t.Errorf("StreamIdleTimeoutSec: got %d, want 60", cfg.StreamIdleTimeoutSec)
	}
}

func TestLoad_StreamIdleTimeoutSec_Zero(t *testing.T) {
	// Zero is VALID (disables the idle watchdog).
	t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.StreamIdleTimeoutSec != 0 {
		t.Errorf("StreamIdleTimeoutSec: got %d, want 0 (explicit disable)", cfg.StreamIdleTimeoutSec)
	}
}

func TestLoad_StreamIdleTimeoutSec_Negative(t *testing.T) {
	t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "-5")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for STREAM_IDLE_TIMEOUT_SEC=-5, got nil")
	}
	if !strings.Contains(err.Error(), "STREAM_IDLE_TIMEOUT_SEC") {
		t.Errorf("error should mention STREAM_IDLE_TIMEOUT_SEC, got: %v", err)
	}
	if !strings.Contains(err.Error(), "must be >= 0") {
		t.Errorf("error should explain the constraint, got: %v", err)
	}
}

func TestLoad_StreamIdleTimeoutSec_NonInt(t *testing.T) {
	t.Setenv("STREAM_IDLE_TIMEOUT_SEC", "abc")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() should return an error for STREAM_IDLE_TIMEOUT_SEC=abc, got nil")
	}
	if !strings.Contains(err.Error(), "STREAM_IDLE_TIMEOUT_SEC") {
		t.Errorf("error should mention STREAM_IDLE_TIMEOUT_SEC, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot parse") {
		t.Errorf("error should explain the parse failure, got: %v", err)
	}
}

// TestLoad_JSONFormatSteeringEnabled covers the three canonical env-bool
// states that match the PII_REDACTION_ENABLED test conventions.
func TestLoad_JSONFormatSteeringEnabled(t *testing.T) {
	t.Run("unset defaults to true", func(t *testing.T) {
		t.Setenv("JSON_FORMAT_STEERING_ENABLED", "")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if !cfg.JSONFormatSteeringEnabled {
			t.Error("JSONFormatSteeringEnabled: want true (default), got false")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		t.Setenv("JSON_FORMAT_STEERING_ENABLED", "true")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if !cfg.JSONFormatSteeringEnabled {
			t.Error("JSONFormatSteeringEnabled: want true, got false")
		}
	})
	t.Run("explicit false", func(t *testing.T) {
		t.Setenv("JSON_FORMAT_STEERING_ENABLED", "false")
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.JSONFormatSteeringEnabled {
			t.Error("JSONFormatSteeringEnabled: want false, got true")
		}
	})
	t.Run("invalid value error", func(t *testing.T) {
		t.Setenv("JSON_FORMAT_STEERING_ENABLED", "maybe")
		_, err := config.Load()
		if err == nil {
			t.Fatal("Load() should error on invalid bool, got nil")
		}
		if !strings.Contains(err.Error(), "JSON_FORMAT_STEERING_ENABLED") {
			t.Errorf("error should mention JSON_FORMAT_STEERING_ENABLED, got: %v", err)
		}
	})
}

// --- CompressionHook env knobs (Task 6) ---------------------------------

func TestLoad_CompressionDefaults(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("COMPRESSION_ENABLED", "")
	t.Setenv("COMPRESS_TRIGGER_TOKENS", "")
	t.Setenv("COMPRESS_BUDGET_TOKENS", "")
	t.Setenv("COMPRESS_PROTECT_TAIL", "")
	t.Setenv("COMPRESS_TOOL_KEEP", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.CompressionEnabled {
		t.Error("CompressionEnabled default must be false")
	}
	if cfg.CompressTriggerTokens != 6000 || cfg.CompressBudgetTokens != 4000 ||
		cfg.CompressProtectTail != 4 || cfg.CompressToolKeep != 1200 {
		t.Errorf("compress defaults wrong: %+v", cfg)
	}
}

func TestLoad_CompressionValidation(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	cases := []struct{ key, val, wantSub string }{
		{"COMPRESS_TRIGGER_TOKENS", "0", "COMPRESS_TRIGGER_TOKENS"},
		{"COMPRESS_BUDGET_TOKENS", "-1", "COMPRESS_BUDGET_TOKENS"},
		{"COMPRESS_PROTECT_TAIL", "-2", "COMPRESS_PROTECT_TAIL"},
		{"COMPRESS_TOOL_KEEP", "0", "COMPRESS_TOOL_KEEP"},
		{"COMPRESS_TOOL_KEEP", "9223372036854775807", "COMPRESS_TOOL_KEEP"}, // upper bound (overflow guard)
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("want boot error naming %s, got %v", c.wantSub, err)
			}
		})
	}
}

func TestLoad_CompressBudgetOverTriggerIsBootError(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("COMPRESS_TRIGGER_TOKENS", "1000")
	t.Setenv("COMPRESS_BUDGET_TOKENS", "2000")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "COMPRESS_BUDGET_TOKENS") {
		t.Errorf("budget > trigger must be a boot error, got %v", err)
	}
}
