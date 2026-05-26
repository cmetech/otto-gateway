// Package config provides typed gateway configuration loaded from environment variables.
// All env var names match the Node reference implementation for drop-in binary replacement.
package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrVersionRequested is returned by LoadArgs when --version was passed.
// main() checks for it and prints version.Version then exits 0 — the config
// package itself NEVER calls os.Exit (process exit is main's responsibility).
var ErrVersionRequested = errors.New("version requested")

// HelpRequested is returned by LoadArgs when -h/--help was passed. It carries
// the rendered flag usage so main() can print it to stdout (GNU convention) and
// exit 0. Unwrap returns flag.ErrHelp so errors.Is(err, flag.ErrHelp) still
// matches; the config package itself NEVER calls os.Exit or writes to stdout.
type HelpRequested struct{ Usage string }

func (e *HelpRequested) Error() string { return "help requested" }
func (e *HelpRequested) Unwrap() error { return flag.ErrHelp }

// Config holds all gateway configuration loaded from environment variables.
// Phase 1 reads a subset; later phases add fields without changing Load()'s signature.
type Config struct {
	// HTTPAddr is the address the HTTP server listens on (default
	// "127.0.0.1:11435" — loopback-only, secure-by-default for laptop
	// deployments. Port 11435 (not the Ollama-standard 11434) avoids
	// colliding with the legacy JS acp_server so both can run side-by-side
	// during cutover; set HTTP_ADDR=:11435 to bind all interfaces, or
	// HTTP_ADDR=127.0.0.1:11434 to take over the Ollama port once the JS
	// proxy is retired).
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
	// PoolSize is the number of warm kiro-cli subprocesses (Phase 5
	// POOL-01: default 4 for Node parity; Phase 2 shipped a default of 1).
	// Set-but-unparseable yields a Load() error.
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

	httpAddr := getEnvStr("HTTP_ADDR", "127.0.0.1:11435")
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

	// Phase 5 POOL-01: env default flips from 1 to 4 for Node parity (see
	// 05-CONTEXT.md <domain> Phase Boundary). Note: the package-level
	// default in internal/pool/config.go applyDefaults stays at 1 because
	// the pool's tests construct pool.Config{} directly and expect Size=1
	// when unset. Only this env-load layer flips.
	poolSize, err := getEnvInt("POOL_SIZE", 4)
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

	enabledSurfaces := getEnvStrSliceComma("ENABLED_SURFACES", []string{"ollama", "anthropic", "openai"})
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

// LoadArgs resolves configuration from env+defaults via Load(), then overlays
// ONLY the CLI flags the operator explicitly passed (flag-wins-over-env). It
// uses ONLY the Go stdlib `flag` package — no new dependencies (preserves the
// no-cgo / minimal-deps constraint from CLAUDE.md).
//
// Design notes:
//   - flag-wins via fs.Visit: Visit walks ONLY the flags actually passed on the
//     command line, so any flag the operator omitted leaves the env-resolved
//     value untouched (fall-through to env).
//   - AUTH_TOKEN is deliberately NOT a flag. The bearer token is a secret and
//     must not appear in argv / ps / /proc — it stays env-only (Node parity).
//   - Load() is intentionally left env-only and UNCHANGED; LoadArgs wraps it.
//   - A local flag.FlagSet (not flag.CommandLine) is used so there is no global
//     state and the function is testable. Its output is discarded so --help /
//     parse errors do not pollute stderr during tests.
func LoadArgs(args []string) (Config, error) {
	cfg, err := Load()
	if err != nil {
		return cfg, err
	}

	fs := flag.NewFlagSet("otto-gateway", flag.ContinueOnError)
	// Capture the FlagSet's output (usage text + parse-error messages) into a
	// buffer instead of letting it hit stderr directly. On --help we hand the
	// buffer back via HelpRequested so main prints usage to stdout; on a parse
	// error we discard the buffer and let main log the wrapped error once
	// (avoids the double "error + usage to stderr, then slog" output).
	var usage bytes.Buffer
	fs.SetOutput(&usage)

	// Defaults are seeded from the already-resolved cfg so that an unset flag's
	// "default" mirrors the env-resolved value. We do NOT trust those defaults
	// for the final overlay — only fs.Visit'd (explicitly-set) flags are applied
	// below, which is what gives us true flag-wins/fall-through semantics.
	var (
		httpAddr        = fs.String("http-addr", cfg.HTTPAddr, "HTTP listen address")
		kiroCmd         = fs.String("kiro-cmd", cfg.KiroCmd, "kiro-cli binary name or path")
		kiroArgs        = fs.String("kiro-args", strings.Join(cfg.KiroArgs, " "), "kiro-cli arguments (whitespace-split)")
		kiroCWD         = fs.String("kiro-cwd", cfg.KiroCWD, "working directory for kiro-cli subprocess")
		debug           = fs.Bool("debug", cfg.Debug, "enable debug-level logging")
		pingInterval    = fs.Duration("ping-interval", cfg.PingInterval, "kiro-cli heartbeat interval (Go duration)")
		poolSize        = fs.Int("pool-size", cfg.PoolSize, "number of warm kiro-cli subprocesses")
		enabledSurfaces = fs.String("enabled-surfaces", strings.Join(cfg.EnabledSurfaces, ","), "comma-split list of enabled HTTP surfaces")
		ollamaPath      = fs.String("ollama-path-prefix", cfg.OllamaPathPrefix, "route prefix for the Ollama surface")
		anthropicPath   = fs.String("anthropic-path-prefix", cfg.AnthropicPathPrefix, "route prefix for the Anthropic surface")
		openaiPath      = fs.String("openai-path-prefix", cfg.OpenAIPathPrefix, "route prefix for the OpenAI surface")
		allowedIPs      = fs.String("allowed-ips", "", "comma-split CIDR/IP allowlist")
		authTrustXFF    = fs.Bool("auth-trust-xff", cfg.AuthTrustXFF, "trust X-Forwarded-For in the IP allowlist check")
		version         = fs.Bool("version", false, "print version and exit")
	)
	// NOTE: the bearer token is intentionally NOT registered as a flag — it is
	// a secret and stays env-only (AUTH_TOKEN). See the doc comment above. The
	// acceptance grep gate asserts this token name never appears in this file.

	if err := fs.Parse(args); err != nil {
		// -h/--help is not a failure: hand the rendered usage back so main can
		// print it to stdout and exit 0.
		if errors.Is(err, flag.ErrHelp) {
			return cfg, &HelpRequested{Usage: usage.String()}
		}
		// Wrap with %w so errors.Is still matches in main while satisfying
		// wrapcheck. Unknown flags (e.g. an unregistered secret flag) and other
		// parse errors surface here as non-nil; main logs them once.
		return cfg, fmt.Errorf("config: %w", err)
	}

	if *version {
		return cfg, ErrVersionRequested
	}

	var errs []error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "http-addr":
			cfg.HTTPAddr = *httpAddr
		case "kiro-cmd":
			cfg.KiroCmd = *kiroCmd
		case "kiro-cwd":
			cfg.KiroCWD = *kiroCWD
		case "ollama-path-prefix":
			cfg.OllamaPathPrefix = *ollamaPath
		case "anthropic-path-prefix":
			cfg.AnthropicPathPrefix = *anthropicPath
		case "openai-path-prefix":
			cfg.OpenAIPathPrefix = *openaiPath
		case "debug":
			cfg.Debug = *debug
		case "auth-trust-xff":
			cfg.AuthTrustXFF = *authTrustXFF
		case "pool-size":
			cfg.PoolSize = *poolSize
		case "ping-interval":
			cfg.PingInterval = *pingInterval
		case "kiro-args":
			// Whitespace-split to match getEnvStrSlice (KIRO_ARGS) semantics.
			cfg.KiroArgs = strings.Fields(*kiroArgs)
		case "enabled-surfaces":
			cfg.EnabledSurfaces = splitCommaTrim(*enabledSurfaces)
			if verr := validateEnabledSurfaces(cfg.EnabledSurfaces); verr != nil {
				errs = append(errs, fmt.Errorf("enabled-surfaces: %w", verr))
			}
		case "allowed-ips":
			prefixes, perr := parseCIDRs(splitCommaTrim(*allowedIPs))
			if perr != nil {
				errs = append(errs, fmt.Errorf("allowed-ips: %w", perr))
				return
			}
			cfg.AllowedIPs = prefixes
		}
	})

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: invalid flags: %w", errors.Join(errs...))
	}

	return cfg, nil
}

// splitCommaTrim splits on "," , trims each entry, and drops empties — the same
// shape getEnvStrSliceComma applies to comma-separated env vars. Used for the
// --enabled-surfaces and --allowed-ips flags so CLI parsing matches env parsing.
func splitCommaTrim(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
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
		"openai":    {},
	}
	var errs []error
	for _, s := range surfaces {
		if _, ok := allowed[s]; !ok {
			errs = append(errs, fmt.Errorf("unknown surface %q (allowed: ollama, anthropic, openai)", s))
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
