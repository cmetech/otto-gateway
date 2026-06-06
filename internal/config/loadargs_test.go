package config_test

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/config"
)

// These tests exercise config.LoadArgs — the CLI-flag overlay that wraps
// config.Load() with flag-wins-over-env precedence. The flags only overlay
// when explicitly passed (flag.FlagSet.Visit). AUTH_TOKEN is deliberately
// NOT a flag (secret must not appear in argv) — see TestLoadArgs_NoAuthTokenFlag.

// --- flag-wins-over-env, one per type ----------------------------------

func TestLoadArgs_FlagWins_String(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("HTTP_ADDR", ":8080")
	cfg, err := config.LoadArgs([]string{"--http-addr", ":9999"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr: got %q, want %q (flag must win over env)", cfg.HTTPAddr, ":9999")
	}
}

func TestLoadArgs_FlagWins_Bool(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("DEBUG", "")
	cfg, err := config.LoadArgs([]string{"--debug"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	if !cfg.Debug {
		t.Errorf("Debug: got false, want true (--debug must win over empty env)")
	}
}

func TestLoadArgs_FlagWins_Int(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("POOL_SIZE", "2")
	cfg, err := config.LoadArgs([]string{"--pool-size", "4"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	if cfg.PoolSize != 4 {
		t.Errorf("PoolSize: got %d, want 4 (flag must win over env)", cfg.PoolSize)
	}
}

func TestLoadArgs_FlagWins_Duration(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("PING_INTERVAL", "60s")
	cfg, err := config.LoadArgs([]string{"--ping-interval", "30s"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	if cfg.PingInterval != 30*time.Second {
		t.Errorf("PingInterval: got %v, want 30s (flag must win over env)", cfg.PingInterval)
	}
}

func TestLoadArgs_FlagWins_CommaSlice_AllowedIPs(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ALLOWED_IPS", "")
	cfg, err := config.LoadArgs([]string{"--allowed-ips", "10.0.0.0/8,192.168.1.1"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	got := make([]string, 0, len(cfg.AllowedIPs))
	for _, p := range cfg.AllowedIPs {
		got = append(got, p.String())
	}
	sort.Strings(got)
	want := []string{"10.0.0.0/8", "192.168.1.1/32"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedIPs: got %v, want %v (bare IP promoted to /32 via parseCIDRs)", got, want)
	}
}

// TestLoadArgs_AllowedIPs_EmptyFlagRejected is the regression test for
// audit config-loadargs-empty-allowed-ips-flag-silently-disables-allowlist.
// An explicit --allowed-ips="" used to silently overwrite an
// env-resolved allowlist with nil → IPAllowlist middleware took the
// allow-all fast path. Post-fix, --allowed-ips="" is rejected.
func TestLoadArgs_AllowedIPs_EmptyFlagRejected(t *testing.T) {
	t.Setenv("ALLOWED_IPS", "10.0.0.0/24,192.168.1.0/24")
	_, err := config.LoadArgs([]string{"--allowed-ips="})
	if err == nil {
		t.Fatal("LoadArgs with --allowed-ips=\"\" must return error, got nil")
	}
	if !strings.Contains(err.Error(), "allowed-ips") {
		t.Errorf("error = %v; want it to mention allowed-ips", err)
	}
}

// TestLoadArgs_AllowedIPs_FlagUnsetUsesEnv confirms the flag default is
// env-derived: when --allowed-ips is not passed, the env-resolved value
// survives. Companion to TestLoadArgs_AllowedIPs_EmptyFlagRejected.
func TestLoadArgs_AllowedIPs_FlagUnsetUsesEnv(t *testing.T) {
	t.Setenv("ALLOWED_IPS", "10.0.0.0/24")
	cfg, err := config.LoadArgs([]string{})
	if err != nil {
		t.Fatalf("LoadArgs: %v", err)
	}
	if len(cfg.AllowedIPs) != 1 || cfg.AllowedIPs[0].String() != "10.0.0.0/24" {
		t.Errorf("AllowedIPs = %v; want [10.0.0.0/24] from env (flag default must be env-derived)", cfg.AllowedIPs)
	}
}

func TestLoadArgs_FlagWins_WhitespaceSlice_KiroArgs(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("KIRO_ARGS", "acp")
	cfg, err := config.LoadArgs([]string{"--kiro-args", "acp --verbose"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	want := []string{"acp", "--verbose"}
	if !reflect.DeepEqual(cfg.KiroArgs, want) {
		t.Errorf("KiroArgs: got %v, want %v (whitespace-split via strings.Fields)", cfg.KiroArgs, want)
	}
}

func TestLoadArgs_FlagWins_CommaSlice_EnabledSurfaces(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	t.Setenv("ENABLED_SURFACES", "")
	cfg, err := config.LoadArgs([]string{"--enabled-surfaces", "ollama"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	want := []string{"ollama"}
	if !reflect.DeepEqual(cfg.EnabledSurfaces, want) {
		t.Errorf("EnabledSurfaces: got %v, want %v (flag overlay + validation)", cfg.EnabledSurfaces, want)
	}
}

// --- fall-through: unset flags leave env value intact -------------------

func TestLoadArgs_FallThrough(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// PING_INTERVAL set in env; only --debug is passed, so PingInterval must
	// keep its env-resolved value (Visit walks ONLY the flags the user passed).
	t.Setenv("PING_INTERVAL", "30s")
	cfg, err := config.LoadArgs([]string{"--debug"})
	if err != nil {
		t.Fatalf("LoadArgs() returned unexpected error: %v", err)
	}
	if cfg.PingInterval != 30*time.Second {
		t.Errorf("PingInterval: got %v, want 30s (unset flag must fall through to env)", cfg.PingInterval)
	}
	if !cfg.Debug {
		t.Errorf("Debug: got false, want true (--debug was passed)")
	}
}

// --- env-only parity: no args == Load() --------------------------------

func TestLoadArgs_EnvOnlyParity_Defaults(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// Clean env: LoadArgs(nil) and LoadArgs([]) must equal Load().
	want, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	gotNil, err := config.LoadArgs(nil)
	if err != nil {
		t.Fatalf("LoadArgs(nil) returned unexpected error: %v", err)
	}
	if !reflect.DeepEqual(gotNil, want) {
		t.Errorf("LoadArgs(nil): got %+v, want %+v (must equal Load())", gotNil, want)
	}
	gotEmpty, err := config.LoadArgs([]string{})
	if err != nil {
		t.Fatalf("LoadArgs([]) returned unexpected error: %v", err)
	}
	if !reflect.DeepEqual(gotEmpty, want) {
		t.Errorf("LoadArgs([]): got %+v, want %+v (must equal Load())", gotEmpty, want)
	}
}

func TestLoadArgs_EnvOnlyParity_WithEnv(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	// A couple of env values set; LoadArgs(nil) must still equal Load().
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("PING_INTERVAL", "30s")
	t.Setenv("ENABLED_SURFACES", "ollama")
	want, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	got, err := config.LoadArgs(nil)
	if err != nil {
		t.Fatalf("LoadArgs(nil) returned unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadArgs(nil) with env: got %+v, want %+v (must equal Load())", got, want)
	}
}

// --- invalid flag values must error ------------------------------------

func TestLoadArgs_InvalidValues(t *testing.T) {
	// t.Setenv: cannot use t.Parallel().
	cases := []struct {
		name    string
		args    []string
		mention string // substring the error should contain (optional)
	}{
		{"bad-cidr", []string{"--allowed-ips", "not-an-ip"}, ""},
		{"unknown-surface", []string{"--enabled-surfaces", "ollama,olama"}, "olama"},
		{"bad-duration", []string{"--ping-interval", "abc"}, ""},
		{"bad-int", []string{"--pool-size", "abc"}, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadArgs(tc.args)
			if err == nil {
				t.Fatalf("LoadArgs(%v) should return an error, got nil", tc.args)
			}
			if tc.mention != "" && !strings.Contains(err.Error(), tc.mention) {
				t.Errorf("error should mention %q, got: %v", tc.mention, err)
			}
		})
	}
}

// --- --version sentinel -------------------------------------------------

func TestLoadArgs_Version(t *testing.T) {
	// t.Setenv: cannot use t.Parallel() (LoadArgs may read env via Load()).
	_, err := config.LoadArgs([]string{"--version"})
	if !errors.Is(err, config.ErrVersionRequested) {
		t.Fatalf("LoadArgs([--version]): got err %v, want errors.Is(err, ErrVersionRequested)", err)
	}
}

// --- no --auth-token flag (secret must not appear in argv) --------------

func TestLoadArgs_NoAuthTokenFlag(t *testing.T) {
	// Behavioral assertion: this is a blackbox package so it cannot inspect
	// the private FlagSet directly. Passing --auth-token must be rejected as
	// an unknown flag ("flag provided but not defined"). The source-level
	// guarantee (no fs.String("auth-token", ...)) is also asserted by the
	// grep gate in acceptance.
	_, err := config.LoadArgs([]string{"--auth-token", "secret"})
	if err == nil {
		t.Fatal("LoadArgs([--auth-token, secret]) should return an error (flag not defined), got nil")
	}
}
