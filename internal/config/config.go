// Package config provides typed gateway configuration loaded from environment variables.
// All env var names match the Node reference implementation for drop-in binary replacement.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all gateway configuration loaded from environment variables.
// Phase 1 reads a subset; later phases add fields without changing Load()'s signature.
type Config struct {
	// HTTPAddr is the address the HTTP server listens on (default ":11434").
	HTTPAddr string
	// KiroCmd is the kiro-cli binary name or path (default "kiro-cli").
	KiroCmd string
	// KiroArgs is the list of arguments passed to kiro-cli (default ["acp"]).
	KiroArgs []string
	// KiroCWD is the working directory for the kiro-cli subprocess (default "").
	KiroCWD string
	// Debug enables debug-level logging (default false).
	Debug bool
	// PingInterval is the heartbeat interval for kiro-cli (default 60s).
	PingInterval time.Duration
	// AuthToken is the list of accepted bearer tokens loaded from AUTH_TOKEN
	// (comma-split). Empty (nil) means auth is disabled (Node parity).
	AuthToken []string
	// AllowedIPs is the list of CIDR prefixes accepted by the IP allowlist,
	// loaded from ALLOWED_IPS (comma-split; each entry is a CIDR or bare IP
	// which is promoted to a /32 or /128 prefix). Empty (nil) means allow-all
	// (Node parity).
	AllowedIPs []netip.Prefix
	// PoolSize is the number of warm kiro-cli subprocesses (default 1 in
	// Phase 2; Phase 5 raises the default to 4). Set-but-unparseable yields
	// a Load() error.
	PoolSize int
	// OllamaPathPrefix is the route prefix under which the Ollama adapter is
	// mounted (default "/api"). Loaded from OLLAMA_PATH_PREFIX.
	OllamaPathPrefix string
	// OpenAIPathPrefix is the route prefix under which the OpenAI adapter is
	// mounted (default "/v1"). Loaded from OPENAI_PATH_PREFIX. Read-only
	// forward-design in Phase 2 — Phase 3 begins consuming it.
	OpenAIPathPrefix string
	// AuthTrustXFF is the operator opt-in for trusting the X-Forwarded-For
	// header in the IP allowlist check (default false; Codex H-7). When false
	// the allowlist sees only r.RemoteAddr and laptop deployments are
	// safe-by-default; set true ONLY when a known reverse proxy is in front
	// of the gateway. Loaded from AUTH_TRUST_XFF.
	AuthTrustXFF bool
	// EnabledSurfaces is the comma-split list of HTTP surfaces the gateway
	// constructs at boot (Phase 3.1 D-16). Default is ["ollama","anthropic"];
	// Phase 3 will widen the default to include "openai". Unknown surface
	// names cause Load() to return an error (fail-fast — RESEARCH.md
	// Pitfall 10 mitigation). Loaded from ENABLED_SURFACES.
	EnabledSurfaces []string
	// AnthropicPathPrefix is the route prefix under which the Anthropic
	// adapter mounts (Phase 3.1 D-19; default "/v1"). Shares the prefix
	// with the OpenAI surface per SURF-08 — endpoint-level disambiguation
	// distinguishes /v1/messages (Anthropic) from /v1/chat/completions
	// (OpenAI). Loaded from ANTHROPIC_PATH_PREFIX.
	AnthropicPathPrefix string
}

// LogLevel returns the slog.Level implied by the Debug flag.
func (c Config) LogLevel() slog.Level {
	if c.Debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// Load reads environment variables and returns a validated Config.
// Returns a non-nil error if any env var is present but has an unparseable value.
// Missing env vars (empty string) use the documented default — only set-but-invalid values are errors.
func Load() (Config, error) {
	var errs []error

	httpAddr := getEnvStr("HTTP_ADDR", ":11434")
	kiroCmd := getEnvStr("KIRO_CMD", "kiro-cli")
	kiroArgs := getEnvStrSlice("KIRO_ARGS", []string{"acp"})
	kiroCWD := getEnvStr("KIRO_CWD", "")

	debug, err := getEnvBool("DEBUG", false)
	if err != nil {
		errs = append(errs, err)
	}

	pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
	if err != nil {
		errs = append(errs, err)
	}

	authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)

	allowedIPEntries := getEnvStrSliceComma("ALLOWED_IPS", nil)
	allowedIPs, err := parseCIDRs(allowedIPEntries)
	if err != nil {
		errs = append(errs, fmt.Errorf("ALLOWED_IPS: %w", err))
	}

	poolSize, err := getEnvInt("POOL_SIZE", 1)
	if err != nil {
		errs = append(errs, err)
	}

	ollamaPath := getEnvStr("OLLAMA_PATH_PREFIX", "/api")
	openaiPath := getEnvStr("OPENAI_PATH_PREFIX", "/v1")
	anthropicPath := getEnvStr("ANTHROPIC_PATH_PREFIX", "/v1")

	trustXFF, err := getEnvBool("AUTH_TRUST_XFF", false)
	if err != nil {
		errs = append(errs, err)
	}

	enabledSurfaces := getEnvStrSliceComma("ENABLED_SURFACES", []string{"ollama", "anthropic"})
	if err := validateEnabledSurfaces(enabledSurfaces); err != nil {
		errs = append(errs, fmt.Errorf("ENABLED_SURFACES: %w", err))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: invalid env vars: %w", errors.Join(errs...))
	}

	return Config{
		HTTPAddr:            httpAddr,
		KiroCmd:             kiroCmd,
		KiroArgs:            kiroArgs,
		KiroCWD:             kiroCWD,
		Debug:               debug,
		PingInterval:        pingInterval,
		AuthToken:           authTokens,
		AllowedIPs:          allowedIPs,
		PoolSize:            poolSize,
		OllamaPathPrefix:    ollamaPath,
		OpenAIPathPrefix:    openaiPath,
		AuthTrustXFF:        trustXFF,
		EnabledSurfaces:     enabledSurfaces,
		AnthropicPathPrefix: anthropicPath,
	}, nil
}

// validateEnabledSurfaces checks every entry in surfaces against the
// Phase 3.1 allow-list {"ollama","anthropic"} and returns a joined
// error naming each offending value. Returns nil for an empty / nil
// list (Load() injects the default before calling — empties never
// reach us in production, but the helper tolerates them so direct
// callers don't crash).
//
// D-16 fail-fast contract: the error message MUST name the offending
// surface so an operator can diagnose `ENABLED_SURFACES=anthrpic`
// (typo) without re-reading the env (RESEARCH.md Pitfall 10). Phase 3
// will widen the allow-list to include "openai".
func validateEnabledSurfaces(surfaces []string) error {
	if len(surfaces) == 0 {
		return nil
	}
	allowed := map[string]struct{}{
		"ollama":    {},
		"anthropic": {},
	}
	var errs []error
	for _, s := range surfaces {
		if _, ok := allowed[s]; !ok {
			errs = append(errs, fmt.Errorf("unknown surface %q (allowed: ollama, anthropic)", s))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// getEnvStr reads an env var, trims whitespace, and returns the default if empty.
func getEnvStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// getEnvStrSlice reads an env var and splits on whitespace.
// Returns def if the env var is empty.
func getEnvStrSlice(key string, def []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return strings.Fields(v)
}

// getEnvStrSliceComma reads an env var, splits on "," (comma — distinct from
// getEnvStrSlice's whitespace-split used for KIRO_ARGS), trims each entry, and
// drops empty entries. Returns def if the env var is empty or contains only
// whitespace/separators. Used for AUTH_TOKEN and ALLOWED_IPS per Node parity.
func getEnvStrSliceComma(key string, def []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// getEnvInt reads an integer env var. Returns an error if the var is set but
// not parseable as an int. Returns def on empty. Matches getEnvBool's error
// shape ("%s: cannot parse %q as int").
func getEnvInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: cannot parse %q as int", key, v)
	}
	return n, nil
}

// parseCIDRs converts a slice of CIDR / bare-IP strings into netip.Prefix
// values. Each entry is first tried as a CIDR via netip.ParsePrefix; on
// failure it falls back to netip.ParseAddr and is promoted to a host prefix
// (/32 for IPv4, /128 for IPv6). All entry-level errors are accumulated via
// errors.Join and returned together. Returns (nil, nil) when entries is nil;
// returns an empty non-nil slice when entries is non-nil but zero-length.
func parseCIDRs(entries []string) ([]netip.Prefix, error) {
	if entries == nil {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(entries))
	var errs []error
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			out = append(out, p)
			continue
		}
		addr, err := netip.ParseAddr(e)
		if err != nil {
			errs = append(errs, fmt.Errorf("invalid CIDR or IP %q", e))
			continue
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// getEnvBool reads a boolean env var. Returns an error if the var is set but not parseable.
// Accepts "1", "true", "0", "false" (case-insensitive). TrimSpace handles Windows trailing-space bug.
func getEnvBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s: cannot parse %q as bool", key, v)
	}
}

// getEnvDuration reads a duration env var. Returns an error if the var is set but not parseable.
// Accepts both millisecond integers (Node compat: "60000" → 60s) and Go duration strings ("60s").
func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	// Accept milliseconds for Node compat (PING_INTERVAL=60000 means 60s).
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	// Accept Go duration strings (e.g., "60s", "1m").
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	return 0, fmt.Errorf("%s: cannot parse %q as duration (expected integer ms or Go duration string)", key, v)
}
