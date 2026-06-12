// Package config provides typed gateway configuration loaded from environment variables.
// All env var names match the Node reference implementation for drop-in binary replacement.
package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// helpUsage renders the canonical --help text using a FlagSet
// seeded with zero-value Config defaults. Called only from the
// meta-flag short-circuit path; the regular Parse flow renders its
// own buffer with env-resolved defaults.
func helpUsage() string {
	var cfg Config
	fs := flag.NewFlagSet("otto-gateway", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.String("http-addr", cfg.HTTPAddr, "HTTP listen address")
	fs.String("kiro-cmd", cfg.KiroCmd, "kiro-cli binary name or path")
	fs.String("kiro-args", strings.Join(cfg.KiroArgs, " "), "kiro-cli arguments (whitespace-split)")
	fs.String("kiro-cwd", cfg.KiroCWD, "working directory for kiro-cli subprocess")
	fs.Bool("debug", cfg.Debug, "enable debug-level logging")
	fs.Duration("ping-interval", cfg.PingInterval, "kiro-cli heartbeat interval (Go duration)")
	fs.Int("pool-size", cfg.PoolSize, "number of warm kiro-cli subprocesses")
	fs.Duration("session-ttl", cfg.SessionTTL, "idle stateful-session reap threshold (Go duration; SESSION_TTL_MS also accepts ms-integer)")
	fs.Int("session-max", cfg.SessionMax, "maximum concurrent stateful sessions (SESSION_MAX)")
	fs.String("enabled-surfaces", strings.Join(cfg.EnabledSurfaces, ","), "comma-split list of enabled HTTP surfaces")
	fs.String("ollama-path-prefix", cfg.OllamaPathPrefix, "route prefix for the Ollama surface")
	fs.String("anthropic-path-prefix", cfg.AnthropicPathPrefix, "route prefix for the Anthropic surface")
	fs.String("openai-path-prefix", cfg.OpenAIPathPrefix, "route prefix for the OpenAI surface")
	fs.String("allowed-ips", joinAllowedIPs(cfg.AllowedIPs), "comma-split CIDR/IP allowlist")
	fs.Bool("auth-trust-xff", cfg.AuthTrustXFF, "trust X-Forwarded-For in the IP allowlist check")
	fs.Bool("version", false, "print version and exit")
	fs.Usage()
	return buf.String()
}

// scanMetaFlag returns "version" or "help" if those meta-flags
// appear anywhere in args (in their `--version`, `-version`,
// `--help`, `-help`, or `-h` forms). It stops at a bare `--`
// (POSIX end-of-flags marker). Returns "" when no meta-flag was
// requested.
func scanMetaFlag(args []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		switch a {
		case "--version", "-version":
			return "version"
		case "--help", "-help", "-h":
			return "help"
		}
	}
	return ""
}

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
	// "127.0.0.1:18080" — loopback-only, secure-by-default for laptop
	// deployments. Port 18080 sits in the IANA unassigned 16000-19999
	// range and avoids collisions with well-known dev ports (8080
	// HTTP-alt, 11434 Ollama, 11435 legacy JS acp_server). Set
	// HTTP_ADDR=:18080 to bind all interfaces, or
	// HTTP_ADDR=127.0.0.1:11434 to take over the Ollama port once the JS
	// proxy is fully retired.
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
	// BodyReadTimeout is the per-request HTTP body-read deadline applied to
	// chat-body POST handlers (REL-HTTP-04 / Plan 16-02). Plan 16-05 owns
	// the config-side parsing of HTTP_BODY_READ_TIMEOUT_SEC; Plan 16-02
	// applies the time.AfterFunc-based deadline wrapper that calls
	// r.Body.Close() on expiry so SSE response writes are unaffected
	// (D-04b). Default 30s; <= 0 is a boot error (D-04 fail-fast). The
	// _SEC env-var suffix matches STREAM_IDLE_TIMEOUT_SEC; the field on
	// Config is a time.Duration so callers do not re-multiply by
	// time.Second.
	BodyReadTimeout time.Duration
	// StreamIdleTimeoutSec is the server-side idle-stream watchdog
	// timeout in seconds (quick 260531-ruv). Default 30. Zero is VALID
	// and disables the watchdog (legacy hang-forever behavior, opt-in).
	// Negative or non-integer values cause a boot error (mirrors
	// PII_REDACTION_MODE typo-fail-fast). Loaded from
	// STREAM_IDLE_TIMEOUT_SEC. Converted to time.Duration in main.go
	// and threaded into engine.Config and each adapter.Config.
	StreamIdleTimeoutSec int
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
	// SessionTTL is the idle-session reap threshold (Phase 5 SESS-02 /
	// D-10). Default 30 minutes for Node parity (SESSION_TTL_MS=1_800_000).
	// Loaded from SESSION_TTL_MS via getEnvDuration which accepts both
	// ms-integers (Node parity) and Go duration strings.
	SessionTTL time.Duration
	// SessionMax is the SESSION_MAX cap on the number of concurrent
	// dedicated sessions (Phase 5 D-06). Default 32. Lazy-create that
	// would exceed the cap returns session.ErrSessionMaxExceeded for
	// surface adapters to render as 503. NEW IN OTTO — no Node
	// equivalent; document accordingly in docs/operating.md.
	SessionMax int
	// SessionTickInterval is the cadence of the registry reaper goroutine
	// (Phase 5 D-10). Default 60s. Exposed primarily as a test-injection
	// seam — the e2e suite (tests/e2e/pool_sessions_e2e_test.go) sets
	// SESSION_TICK_INTERVAL_MS=100 alongside SESSION_TTL_MS=500 to
	// deterministically observe idle-session reaping in <2s. Loaded from
	// SESSION_TICK_INTERVAL_MS via getEnvDuration (accepts ms-integer
	// OR Go duration string).
	SessionTickInterval time.Duration

	// EnabledHooks is the comma-split allowlist of hook type names enabled
	// at boot (Phase 8 D-02). Default empty = all hooks in the chain
	// enabled (matches AUTH_TOKEN semantics — permissive default). A name
	// NOT present in main.go's chain literal causes chain.Filter to return
	// an error at boot (typo-fail-fast — the load-bearing case is
	// ENABLED_HOOKS=PIIRedaction silently disabling PII redaction by
	// missing the "Hook" suffix). Note: typo validation happens at
	// chain.Filter in main.go (it needs the runtime chain to know what's
	// valid); config.Load only parses the SHAPE. Loaded from ENABLED_HOOKS.
	EnabledHooks []string

	// PIIRedactionEnabled controls whether PIIRedactionHook does WORK when
	// invoked (Phase 8 D-02 two-knob model). Composes with EnabledHooks:
	// ENABLED_HOOKS controls whether the hook IS in the chain at all;
	// PII_REDACTION_ENABLED controls whether the hook does work when
	// invoked. Default false (operator must explicitly opt in to PII
	// scrubbing). Loaded from PII_REDACTION_ENABLED.
	PIIRedactionEnabled bool

	// PIIEnabledEntities is the comma-split list of recognizer Names that
	// PIIRedactionHook applies. Empty = all registered recognizers active
	// (13 regex + 2 NER when PII_NER_ENABLED=true). An unknown name causes
	// Load() to return an error (typo-fail-fast). Loaded from
	// PII_ENABLED_ENTITIES.
	PIIEnabledEntities []string

	// PIIRedactionMode is "replace" | "mask" | "hash" | "drop" (Phase 8
	// D-05). Default "replace". An unknown value causes Load() to return
	// an error. mode=hash with empty PIIHashKey is ALSO a boot error
	// (T-8-HASH-BOOT mitigation — RESEARCH Pitfall 6; no silent unkeyed
	// HMAC fallback). Loaded from PII_REDACTION_MODE.
	PIIRedactionMode string

	// PIIHashKey is the HMAC-SHA256 key for PII_REDACTION_MODE=hash
	// (Phase 8 D-05). Required when Mode=="hash"; otherwise unused.
	// Loaded from PII_HASH_KEY. Rotating this key invalidates prior
	// correlation tokens (intentional — key-rotation tool for suspected
	// log leak; documented in docs/operating.md).
	PIIHashKey string

	// PIIEntityActions is the per-entity action override map parsed from
	// PII_ENTITY_ACTIONS. Empty map reproduces today's behavior (global
	// PIIRedactionMode applies to every recognizer). When non-empty,
	// PIIEntityActions[entity] wins over PIIRedactionMode for the named
	// entities. Unknown entity names or unknown action values cause
	// Load() to return an error. The five allowed action values are
	// "replace" | "mask" | "hash" | "drop" | "encrypt".
	PIIEntityActions map[string]string

	// PIIEncryptKey is the raw PII_ENCRYPT_KEY env value (any non-empty
	// string). It is passed through to the slice-5 wiring, which calls
	// pii.DeriveKey to produce the 32-byte AES-256-GCM key. Required
	// when encrypt is active (PII_REDACTION_MODE=encrypt OR any value
	// in PII_ENTITY_ACTIONS is "encrypt"); empty otherwise. Boot error
	// when encrypt is active and this is empty.
	PIIEncryptKey string

	// PIINEREnabled gates the prose-based NER engine that emits PERSON
	// and LOCATION spans alongside the regex recognizers. Default false:
	// the prose tokenizer/tagger state is not allocated unless the
	// operator explicitly opts in. Loaded from PII_NER_ENABLED. English-
	// only; see internal/plugin/pii/ner.go for the documented accuracy
	// ceiling.
	PIINEREnabled bool

	// JSONFormatSteeringEnabled controls whether JSONFormatSteeringHook does
	// WORK when invoked (Phase 08.2 D-06 two-knob model). Composes with
	// EnabledHooks: ENABLED_HOOKS controls whether the hook IS in the chain
	// at all; JSON_FORMAT_STEERING_ENABLED controls whether it does work
	// when invoked. Default true (parity with the Node Ollama shim, which
	// applies GEN_RULES unconditionally when format is set). Loaded from
	// JSON_FORMAT_STEERING_ENABLED.
	JSONFormatSteeringEnabled bool

	// ChatTrace enables the ChatTraceHook NDJSON tracer (quick 260529-ll2).
	// Default false. When true, main.go constructs a dedicated
	// timberjack rotator at ChatTraceFile and prepends ChatTraceHook to
	// chain.Pre so the hook observes the post-adapter canonical request
	// BEFORE PIIRedactionHook mutates it. SENSITIVE — the file contains
	// raw user prompts. Two-knob safety: when this is false, no file is
	// opened on disk and no NDJSON records are written. Loaded from
	// CHAT_TRACE.
	ChatTrace bool

	// ChatTraceFile is the on-disk path of the chat-trace NDJSON log
	// (quick 260529-ll2). Default-derived: if LOG_FILE is set, this is
	// the LOG_FILE basename with the "-chat-trace.log" suffix in the
	// same directory; else "./logs/otto-gateway-chat-trace.log". The
	// timberjack rotator opens this file with mode 0o600 (T-ll2-01
	// mitigation). Loaded from CHAT_TRACE_FILE.
	ChatTraceFile string

	// AdminTailPath is the on-disk path the admin log-tail panel reads
	// from. D-18-08: populated in Load via the SAME deriveChatTraceFile
	// call that produces ChatTraceFile, so the writer (chat-trace rotator
	// at main.go:302 — reads cfg.ChatTraceFile) and the tailer
	// (admin.NewTailer constructed in main.go — reads cfg.AdminTailPath)
	// cannot diverge. Both fields hold the same string by construction;
	// AdminTailPath exists as a separate field so the tailer-wiring site
	// reads the semantically-named field, not the writer-named one.
	AdminTailPath string

	// ChatTraceMaxAgeDays is the timberjack MaxAge in days for chat-
	// trace.log rotation pruning (quick 260529-ll2). Default 3. Rolling
	// over daily at 00:00 with 3-day retention bounds the sensitive
	// content exposure window (T-ll2-05 DoS / T-ll2-01 leak mitigation).
	// Loaded from CHAT_TRACE_MAX_AGE_DAYS.
	ChatTraceMaxAgeDays int
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

	httpAddr := getEnvStr("HTTP_ADDR", "127.0.0.1:18080")
	kiroCmd := getEnvStr("KIRO_CMD", "kiro-cli")
	kiroArgs := getEnvStrSlice("KIRO_ARGS", []string{"acp"})
	kiroCWD := getEnvStr("KIRO_CWD", "")

	// D-18-02 REL-CFG-06: KIRO_CMD validation — exec.LookPath checks the
	// PATH for bare names AND verifies executability for absolute/relative
	// paths. The error is config-named so operators see the offending env
	// var in the boot log rather than a 5-10s-deferred raw exec.ErrNotFound
	// from inside the kiro-cli spawn path.
	if _, lookErr := exec.LookPath(kiroCmd); lookErr != nil {
		errs = append(errs, fmt.Errorf("config: KIRO_CMD (%q): not found in PATH or unreadable", kiroCmd))
	}

	// D-18-02 REL-CFG-06: KIRO_CWD tilde expansion happens FIRST so the
	// stat check operates on the resolved path AND the resolved path is
	// what gets stored in Config.KiroCWD. Only the `~/` prefix and bare
	// `~` are recognized — no $HOME interpolation, no shell-style globbing.
	// Empty KIRO_CWD remains the default and is treated as optional
	// (acp.Client handles empty Cwd by inheriting the parent's wd).
	//
	// WR-02: reject `..` segments in the post-`~/` portion. filepath.Join
	// cleans `..` segments, so `KIRO_CWD=~/../../etc` would resolve to
	// `/etc` and silently pass the stat check (since /etc is a real
	// directory). KIRO_CWD is operator-controlled boot-time env so this
	// is "intentional misconfiguration" rather than a request-time
	// vulnerability, but tilde expansion should not be a path-escape
	// shortcut — operators who want a path outside $HOME can use an
	// absolute path directly.
	if strings.HasPrefix(kiroCWD, "~/") {
		if home, herr := os.UserHomeDir(); herr == nil {
			rest := kiroCWD[2:]
			// Block any `..` segment, whether bare (`..`), at a boundary
			// (`foo/..`), or as a path prefix (`../bar`). strings.Contains
			// would also match `foo..bar` (a legitimate dirname), so split
			// on the OS separator and check each segment exactly.
			containsDotDot := false
			for _, seg := range strings.Split(filepath.ToSlash(rest), "/") {
				if seg == ".." {
					containsDotDot = true
					break
				}
			}
			if containsDotDot {
				errs = append(errs, fmt.Errorf("config: KIRO_CWD (%q): '..' segments not permitted after '~/' (use an absolute path for locations outside $HOME)", kiroCWD))
			} else {
				kiroCWD = filepath.Join(home, rest)
			}
		}
	} else if kiroCWD == "~" {
		if home, herr := os.UserHomeDir(); herr == nil {
			kiroCWD = home
		}
	}
	if kiroCWD != "" {
		stat, sErr := os.Stat(kiroCWD)
		switch {
		case sErr != nil:
			errs = append(errs, fmt.Errorf("config: KIRO_CWD (%q): directory does not exist", kiroCWD))
		case !stat.IsDir():
			errs = append(errs, fmt.Errorf("config: KIRO_CWD (%q): not a directory", kiroCWD))
		}
	}

	debug, err := getEnvBool("DEBUG", false)
	if err != nil {
		errs = append(errs, err)
	}

	pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
	if err != nil {
		errs = append(errs, err)
	}
	// C-2 (REL-CFG-02) fail-fast: PING_INTERVAL <= 0 reaches time.NewTicker in
	// acp/client.go and panics with "non-positive interval for NewTicker" inside
	// the ping goroutine. Boot error here prevents the raw goroutine panic. Use
	// "> 0" (not ">= 0") because zero is also invalid for time.NewTicker.
	if pingInterval <= 0 {
		errs = append(errs, fmt.Errorf("PING_INTERVAL: must be > 0, got %v", pingInterval))
	}

	// D-18-01 REL-CFG-05: detect degenerate AUTH_TOKEN — set but consisting
	// only of whitespace or CSV delimiters so the parsed slice is empty.
	// Emit a Warn naming the variable AND treat as unset (no fail-fast)
	// per CLAUDE.md's "no auth if env unset" posture. Mirrors REL-CFG-03's
	// slog.Default() emission shape at the EMBEDDING_MODEL_DEFAULT site so
	// the regression test (which captures slog.Default() and calls only
	// config.Load) observes the Warn without a full main() spin-up.
	//
	// Use os.Getenv directly (no TrimSpace) for the "is set" predicate so
	// whitespace-only values like "   " still trip the Warn — the operator
	// clearly intended a value and got silently disabled.
	rawAuth := os.Getenv("AUTH_TOKEN")
	authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)
	if rawAuth != "" && len(authTokens) == 0 {
		slog.Default().Warn(
			"AUTH_TOKEN looks degenerate (no entries after trim+CSV split); treating as unset",
			"raw", rawAuth,
		)
	}

	// D-18-01 REL-CFG-05: same pattern for ALLOWED_IPS. The `err == nil`
	// guard prevents the Warn from firing when parseCIDRs has already
	// surfaced a real CIDR-parse error — that path is non-degenerate and
	// belongs in errs/errors.Join, not Warn.
	rawAllowed := os.Getenv("ALLOWED_IPS")
	allowedIPEntries := getEnvStrSliceComma("ALLOWED_IPS", nil)
	allowedIPs, err := parseCIDRs(allowedIPEntries)
	if err != nil {
		errs = append(errs, fmt.Errorf("ALLOWED_IPS: %w", err))
	}
	if rawAllowed != "" && len(allowedIPs) == 0 && err == nil {
		slog.Default().Warn(
			"ALLOWED_IPS looks degenerate (no entries after trim+CSV split); treating as unset",
			"raw", rawAllowed,
		)
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
	// C-1 (REL-CFG-01) fail-fast: POOL_SIZE silently coerced to 1 by
	// pool.Config.applyDefaults when <= 0. Match STREAM_IDLE_TIMEOUT_SEC posture
	// (config.go:366-368) — emit a named boot error so misconfigured deployments
	// fail loudly. Upper bound 256 (D-discretion sanity ceiling: any system that
	// genuinely needs > 256 concurrent kiro-cli workers is unsupported).
	if poolSize < 0 {
		errs = append(errs, fmt.Errorf("POOL_SIZE: must be >= 0, got %d", poolSize))
	}
	if poolSize > 256 {
		errs = append(errs, fmt.Errorf("POOL_SIZE: sanity cap exceeded (max 256), got %d", poolSize))
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

	// Phase 5 SESS-02 / D-10: SESSION_TTL_MS default 30 min (Node parity).
	// getEnvDuration accepts both Go duration strings ("30m") and
	// millisecond integers ("1800000") so the env name matches Node's
	// SESSION_TTL_MS exactly.
	sessionTTL, err := getEnvDuration("SESSION_TTL_MS", 30*time.Minute)
	if err != nil {
		errs = append(errs, err)
	}
	// C-1 (REL-CFG-01) fail-fast: SESSION_TTL_MS silently coerced to 30m by
	// session.Config.applyDefaults when <= 0. Negative values are nonsensical for
	// an idle-reap threshold. Match STREAM_IDLE_TIMEOUT_SEC posture.
	if sessionTTL < 0 {
		errs = append(errs, fmt.Errorf("SESSION_TTL_MS: must be >= 0, got %v", sessionTTL))
	}

	// Phase 5 D-06: SESSION_MAX cap (default 32). NEW env var — no Node
	// equivalent; documented in docs/operating.md.
	sessionMax, err := getEnvInt("SESSION_MAX", 32)
	if err != nil {
		errs = append(errs, err)
	}
	// C-1 (REL-CFG-01) fail-fast: SESSION_MAX silently coerced to 32 by
	// session.Config.applyDefaults when <= 0. Match STREAM_IDLE_TIMEOUT_SEC posture.
	if sessionMax < 0 {
		errs = append(errs, fmt.Errorf("SESSION_MAX: must be >= 0, got %d", sessionMax))
	}

	// Phase 5 Plan 05-03 Task 5 (e2e injection seam): SESSION_TICK_INTERVAL_MS
	// default 60s (matches session.applyDefaults). Accepts both ms-integer
	// AND Go duration string via getEnvDuration. Production callers leave
	// this at the default; the e2e suite sets it to 100ms for
	// deterministic reap observation in IdleReap_RealTime.
	sessionTickInterval, err := getEnvDuration("SESSION_TICK_INTERVAL_MS", 60*time.Second)
	if err != nil {
		errs = append(errs, err)
	}
	// C-1 (REL-CFG-01) fail-fast: SESSION_TICK_INTERVAL_MS silently coerced to
	// 60s by session.Config.applyDefaults when <= 0. Match STREAM_IDLE_TIMEOUT_SEC
	// posture.
	if sessionTickInterval < 0 {
		errs = append(errs, fmt.Errorf("SESSION_TICK_INTERVAL_MS: must be >= 0, got %v", sessionTickInterval))
	}

	// Quick 260531-ruv — STREAM_IDLE_TIMEOUT_SEC. Default 30 (seconds).
	// Zero is VALID (explicit disable). Negative values are a boot error.
	// Non-integer values bubble up from getEnvInt as a wrapped error
	// containing "cannot parse" (matches POOL_SIZE / SESSION_MAX shape).
	streamIdleTimeoutSec, err := getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30)
	if err != nil {
		errs = append(errs, err)
	}
	if streamIdleTimeoutSec < 0 {
		errs = append(errs, fmt.Errorf("STREAM_IDLE_TIMEOUT_SEC: must be >= 0, got %d", streamIdleTimeoutSec))
	}

	// H-4 (REL-HTTP-04) config owner — Plan 16-05 parses HTTP_BODY_READ_TIMEOUT_SEC;
	// Plan 16-02 owns the server-side body-read wrapper that consumes BodyReadTimeout
	// off server.Config. D-04: default 30s; <= 0 is a boot error (fail-fast posture
	// matching C-1/C-2). _SEC suffix matches STREAM_IDLE_TIMEOUT_SEC convention.
	bodyReadTimeoutSec, err := getEnvInt("HTTP_BODY_READ_TIMEOUT_SEC", 30)
	if err != nil {
		errs = append(errs, err)
	}
	if bodyReadTimeoutSec <= 0 {
		errs = append(errs, fmt.Errorf("HTTP_BODY_READ_TIMEOUT_SEC: must be > 0, got %d", bodyReadTimeoutSec))
	}

	// Phase 8 D-02 / D-05: five new env keys for the plugin chain.
	// ENABLED_HOOKS shape-only (chain.Filter does the typo check at
	// boot — see main.go). PII_* knobs validated here for shape +
	// mode-hash-requires-key invariant.
	enabledHooks := getEnvStrSliceComma("ENABLED_HOOKS", nil)

	// Default true: PII redaction is on out of the box. Operators who
	// explicitly want plaintext through the gateway must set
	// PII_REDACTION_ENABLED=false. Combined with PII_REDACTION_MODE=encrypt
	// (also default), the round-trip property is on by default — the
	// LLM never sees plaintext PII unless the operator opts out.
	piiEnabled, err := getEnvBool("PII_REDACTION_ENABLED", true)
	if err != nil {
		errs = append(errs, err)
	}

	// Default FALSE (changed 2026-06-04 — v1.9.8). The prose-v2 small NER
	// model proved too weak for production address coverage: per the
	// 2026-06-04 splunk-box probe it catches popular city names but
	// emits PERSON false positives on street names ("Main Street",
	// "Pennsylvania Avenue", "Apple Park" all tagged PERSON) which
	// would be tokenized as someone's name on round-trip. Operators
	// who explicitly want PERSON / LOCATION NER for non-address text
	// must opt in via PII_NER_ENABLED=true. The bundled prose weights
	// still ship in the binary; no network, no model download — only
	// the boot-time enable-by-default is flipped. Phase 8.4 adds proper
	// USAddress + USZIP + USState regex recognizers that supersede the
	// NER for address text; LOCATION via NER may be re-enabled on a
	// per-deployment basis after that lands. See Phase 8.4 entry in
	// ROADMAP for the followup rationale.
	piiNEREnabled, err := getEnvBool("PII_NER_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}

	piiEntities := getEnvStrSliceComma("PII_ENABLED_ENTITIES", nil)
	if err := validatePIIEntities(piiEntities); err != nil {
		errs = append(errs, fmt.Errorf("PII_ENABLED_ENTITIES: %w", err))
	}

	// Default "encrypt": PII flows to the LLM as AES-256-GCM ciphertext
	// and is decrypted back to plaintext before the client sees the
	// response (round-trip). Requires PII_ENCRYPT_KEY to be set — the
	// install scripts (scripts/install.{sh,ps1}) generate one and write
	// it into the env file. A fresh checkout without the install will
	// fail boot with a clear error naming PII_ENCRYPT_KEY.
	piiMode := getEnvStr("PII_REDACTION_MODE", "encrypt")
	if err := validatePIIMode(piiMode); err != nil {
		errs = append(errs, fmt.Errorf("PII_REDACTION_MODE: %w", err))
	}

	piiHashKey := getEnvStr("PII_HASH_KEY", "")
	// D-05 / Pitfall 6 / T-8-HASH-BOOT: mode=hash with empty key →
	// refuse to start. No silent unkeyed HMAC fallback (which would be
	// rainbow-table-trivial).
	if piiMode == "hash" && piiHashKey == "" {
		errs = append(errs, fmt.Errorf("PII_REDACTION_MODE=hash requires PII_HASH_KEY"))
	}

	// PII-ENCRYPT-08: per-entity action overrides + encrypt-active key check.
	piiEntityActions, err := parsePIIEntityActions(getEnvStr("PII_ENTITY_ACTIONS", ""))
	if err != nil {
		errs = append(errs, fmt.Errorf("PII_ENTITY_ACTIONS: %w", err))
	}

	piiEncryptKey := getEnvStr("PII_ENCRYPT_KEY", "")
	// Encrypt is "active" if the global mode is encrypt OR any override
	// is encrypt. When active, the key MUST be non-empty — there is no
	// silent fallback that would produce decryptable tokens.
	encryptActive := piiMode == "encrypt"
	for _, a := range piiEntityActions {
		if a == "encrypt" {
			encryptActive = true
			break
		}
	}
	if encryptActive && piiEncryptKey == "" {
		errs = append(errs, fmt.Errorf("PII_ENCRYPT_KEY: required when encrypt is active (PII_REDACTION_MODE=encrypt or any PII_ENTITY_ACTIONS value is encrypt)"))
	}

	// PII-ENCRYPT-08 soft validation: warn when PII_ENTITY_ACTIONS names an
	// entity that is absent from PII_ENABLED_ENTITIES (i.e., the override can
	// never fire because the recognizer is filtered out). NOT a boot-blocking
	// error — operator may have intentional partial overrides. The warning is
	// surfaced at boot so operators see it in their logs.
	if len(piiEntities) > 0 && len(piiEntityActions) > 0 {
		enabledSet := make(map[string]struct{}, len(piiEntities))
		for _, e := range piiEntities {
			enabledSet[e] = struct{}{}
		}
		for entity := range piiEntityActions {
			if _, ok := enabledSet[entity]; !ok {
				slog.Default().Warn("config.pii: entity_actions includes entity NOT in enabled_entities",
					"entity", entity)
			}
		}
	}

	// Phase 08.2 D-06: JSON_FORMAT_STEERING_ENABLED. Default true (parity
	// with Node shim which applies GEN_RULES unconditionally). Mirrors the
	// PII_REDACTION_ENABLED load pattern (getEnvBool + error accumulation).
	jsonFormatSteeringEnabled, err := getEnvBool("JSON_FORMAT_STEERING_ENABLED", true)
	if err != nil {
		errs = append(errs, err)
	}

	// Quick 260529-ll2 — ChatTraceHook env knobs. Two-knob: CHAT_TRACE
	// toggles work-doing; CHAT_TRACE_FILE / CHAT_TRACE_MAX_AGE_DAYS
	// tune the rotator. The writable-parent check only runs when
	// CHAT_TRACE=true so the default ./logs/ path is never created on
	// disabled installs.
	chatTrace, err := getEnvBool("CHAT_TRACE", false)
	if err != nil {
		errs = append(errs, err)
	}
	logFileForDerive := strings.TrimSpace(os.Getenv("LOG_FILE"))
	chatTraceFile := getEnvStr("CHAT_TRACE_FILE", deriveChatTraceFile(logFileForDerive))
	chatTraceMaxAgeDays, err := getEnvInt("CHAT_TRACE_MAX_AGE_DAYS", 3)
	if err != nil {
		errs = append(errs, err)
	}
	// C-1 (REL-CFG-01) fail-fast: CHAT_TRACE_MAX_AGE_DAYS negative is nonsensical
	// for timberjack MaxAge — silently bypasses the retention pruning window
	// (T-ll2-01 leak window guard). Match STREAM_IDLE_TIMEOUT_SEC posture.
	if chatTraceMaxAgeDays < 0 {
		errs = append(errs, fmt.Errorf("CHAT_TRACE_MAX_AGE_DAYS: must be >= 0, got %d", chatTraceMaxAgeDays))
	}
	if chatTrace {
		if dir := filepath.Dir(chatTraceFile); dir != "" && dir != "." {
			// G703 exemption: CHAT_TRACE_FILE is operator-supplied at process
			// boot (not request-time), so taint analysis flags but no untrusted
			// inbound surface reaches this mkdir.
			if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil { //nolint:gosec // G703: operator-controlled boot path (CHAT_TRACE_FILE env), not request-time
				errs = append(errs, fmt.Errorf("CHAT_TRACE_FILE: parent unwritable %q: %w", dir, mkErr))
			}
		}
	}

	// C-3 (REL-CFG-03) embedding stub Warn: EMBEDDING_MODEL_DEFAULT is
	// documented in CLAUDE.md's backward-compat env list but is not read
	// anywhere in the binary — the embedding surface (/v1/embeddings,
	// /api/embed, /api/embeddings) is deferred to v1.10+ per
	// docs/briefs/go_port_brief.md §3.4. Emit a single Warn to slog.Default()
	// so operators who set this env var see that their config is silently
	// dropped. Emitted from config.Load() (not main.go) so the regression
	// test in TestRegression_REL_CFG_03_EmbeddingModelDefaultUnimplemented —
	// which captures slog.Default() and calls only config.Load() — observes
	// the Warn record without a full main() spin-up. Not a boot error
	// (D-03): the variable is reserved, not invalid.
	if embeddingModelDefault := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL_DEFAULT")); embeddingModelDefault != "" {
		slog.Default().Warn(
			"embedding surface is not implemented; EMBEDDING_MODEL_DEFAULT will be ignored",
			"value", embeddingModelDefault,
		)
	}

	// D-18-03 REL-CFG-07: bind-then-close TCP probe of HTTP_ADDR. The real
	// bind happens later in server.ListenAndServe (after 5–10s of kiro-cli
	// pool warmup). Doing this probe at the END of validation surfaces
	// port-in-use to the operator BEFORE the warmup cost is paid AND keeps
	// every other config error in the same errors.Join surface. The
	// probe is best-effort: a TOCTOU window between Close() and the real
	// bind exists and is accepted (microseconds wide, same process,
	// CONTEXT.md §D-18-03).
	// noctx: synchronous startup probe; context would never be cancelled.
	if ln, lerr := net.Listen("tcp", httpAddr); lerr != nil { //nolint:noctx
		errs = append(errs, fmt.Errorf("config: HTTP_ADDR (%q): bind probe failed: %w", httpAddr, lerr))
	} else {
		_ = ln.Close()
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: invalid env vars: %w", errors.Join(errs...))
	}

	// Quick 260529-ll2 — typo-fail-fast carry-forward: when CHAT_TRACE=false
	// the hook is naturally absent from the chain in main.go, but operators
	// may legitimately include "ChatTraceHook" in their ENABLED_HOOKS
	// allowlist (forward-compat). Silently drop the entry so chain.Filter
	// does not error on a hook that the boot path never wired.
	if !chatTrace && slices.Contains(enabledHooks, "ChatTraceHook") {
		enabledHooks = slices.DeleteFunc(enabledHooks, func(s string) bool {
			return s == "ChatTraceHook"
		})
	}

	// Symmetric inverse rule (260530 fix): when CHAT_TRACE=true and the
	// operator has set an explicit ENABLED_HOOKS allowlist (non-empty),
	// auto-prepend "ChatTraceHook" if absent. Without this, an operator
	// who enables CHAT_TRACE but forgets to add "ChatTraceHook" to their
	// allowlist gets a wired-but-filtered hook and zero chat-trace
	// records on disk — confusing because the rotator IS constructed in
	// main.go but Before() is never called once chain.Filter strips it.
	//
	// Prepend (not append) preserves the load-bearing chain-order
	// invariant: ChatTraceHook MUST be first in Pre so it observes the
	// pre-redaction request shape (trace.go KEY CORRECTNESS INVARIANT).
	//
	// The len(enabledHooks)>0 guard is critical: empty allowlist means
	// "run every hook in registration order" (default-permissive), and
	// the hook is already in the chain via main.go — auto-injecting
	// into an empty list would change semantics from "all hooks" to
	// "only ChatTraceHook".
	if chatTrace && len(enabledHooks) > 0 && !slices.Contains(enabledHooks, "ChatTraceHook") {
		enabledHooks = append([]string{"ChatTraceHook"}, enabledHooks...)
	}

	return Config{
		HTTPAddr:                  httpAddr,
		KiroCmd:                   kiroCmd,
		KiroArgs:                  kiroArgs,
		KiroCWD:                   kiroCWD,
		Debug:                     debug,
		PingInterval:              pingInterval,
		AuthToken:                 authTokens,
		AllowedIPs:                allowedIPs,
		PoolSize:                  poolSize,
		BodyReadTimeout:           time.Duration(bodyReadTimeoutSec) * time.Second,
		StreamIdleTimeoutSec:      streamIdleTimeoutSec,
		OllamaPathPrefix:          ollamaPath,
		OpenAIPathPrefix:          openaiPath,
		AuthTrustXFF:              trustXFF,
		EnabledSurfaces:           enabledSurfaces,
		AnthropicPathPrefix:       anthropicPath,
		SessionTTL:                sessionTTL,
		SessionMax:                sessionMax,
		SessionTickInterval:       sessionTickInterval,
		EnabledHooks:              enabledHooks,
		PIIRedactionEnabled:       piiEnabled,
		PIIEnabledEntities:        piiEntities,
		PIIRedactionMode:          piiMode,
		PIIHashKey:                piiHashKey,
		PIIEntityActions:          piiEntityActions,
		PIIEncryptKey:             piiEncryptKey,
		PIINEREnabled:             piiNEREnabled,
		JSONFormatSteeringEnabled: jsonFormatSteeringEnabled,
		ChatTrace:                 chatTrace,
		ChatTraceFile:             chatTraceFile,
		// D-18-08 REL-OBSV-04: AdminTailPath shares the same source as
		// ChatTraceFile so the writer and tailer cannot diverge.
		AdminTailPath:       chatTraceFile,
		ChatTraceMaxAgeDays: chatTraceMaxAgeDays,
	}, nil
}

// deriveChatTraceFile builds the default CHAT_TRACE_FILE path from the
// resolved LOG_FILE env value. Sibling-file convention: same directory,
// same basename minus extension, "-chat-trace.log" suffix. When
// logFile is empty, returns the documented packaged-default path
// "./logs/otto-gateway-chat-trace.log".
//
// Used by Load() for the CHAT_TRACE_FILE default, so an operator who
// sets LOG_FILE=/var/log/otto/otto-gateway.log gets a co-located
// chat-trace at /var/log/otto/otto-gateway-chat-trace.log without
// further configuration — same directory permissions, same rotation
// destination, same operator-cognitive home.
func deriveChatTraceFile(logFile string) string {
	logFile = strings.TrimSpace(logFile)
	if logFile == "" {
		return "./logs/otto-gateway-chat-trace.log"
	}
	ext := filepath.Ext(logFile)
	base := strings.TrimSuffix(logFile, ext)
	return base + "-chat-trace.log"
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
	// Meta-flag pre-scan: --version and --help/-h must short-circuit
	// BEFORE Load() runs env validation, so they work even when env
	// is misconfigured (e.g. PII_ENCRYPT_KEY required by env-mode
	// PII but unset). The version/help paths inspect no env state, so
	// running them ahead of Load() loses nothing and unblocks the
	// "otto-gw version" wrapper subcommand on fresh installs.
	if hit := scanMetaFlag(args); hit != "" {
		switch hit {
		case "version":
			return Config{}, ErrVersionRequested
		case "help":
			// Render usage from a FlagSet seeded with a zero-value
			// Config so --help works even when env validation would
			// fail (e.g. PII_ENCRYPT_KEY missing). The flag defaults
			// shown will be zero values rather than env-resolved
			// defaults, but the flag NAMES + descriptions are what
			// matter for --help. Defining the flags below in one
			// place would let us share rendering — for now we accept
			// the duplication and keep the divergence small.
			return Config{}, &HelpRequested{Usage: helpUsage()}
		}
	}

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
		sessionTTL      = fs.Duration("session-ttl", cfg.SessionTTL, "idle stateful-session reap threshold (Go duration; SESSION_TTL_MS also accepts ms-integer)")
		sessionMax      = fs.Int("session-max", cfg.SessionMax, "maximum concurrent stateful sessions (SESSION_MAX)")
		enabledSurfaces = fs.String("enabled-surfaces", strings.Join(cfg.EnabledSurfaces, ","), "comma-split list of enabled HTTP surfaces")
		ollamaPath      = fs.String("ollama-path-prefix", cfg.OllamaPathPrefix, "route prefix for the Ollama surface")
		anthropicPath   = fs.String("anthropic-path-prefix", cfg.AnthropicPathPrefix, "route prefix for the Anthropic surface")
		openaiPath      = fs.String("openai-path-prefix", cfg.OpenAIPathPrefix, "route prefix for the OpenAI surface")
		allowedIPs      = fs.String("allowed-ips", joinAllowedIPs(cfg.AllowedIPs), "comma-split CIDR/IP allowlist")
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
		case "session-ttl":
			cfg.SessionTTL = *sessionTTL
		case "session-max":
			cfg.SessionMax = *sessionMax
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
			// Audit config-loadargs-empty-allowed-ips-flag-silently-disables-allowlist:
			// reject explicit empty strings on a security-relevant knob.
			// A wrapper script with an unset shell variable expanding to
			// --allowed-ips="" previously silently overwrote an
			// env-resolved allowlist with nil and enabled allow-all. The
			// flag default is now env-derived (joinAllowedIPs above) so
			// "flag unset → env wins" works without an explicit pass; an
			// explicit empty is an obvious configuration mistake.
			if strings.TrimSpace(*allowedIPs) == "" {
				errs = append(errs, errors.New("allowed-ips: explicit empty value not allowed; use --allowed-ips=0.0.0.0/0 to opt in to allow-all"))
				return
			}
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

// validatePIIMode rejects unknown PII_REDACTION_MODE values fail-fast
// with a clear error naming both the offending value and the five
// accepted modes (Phase 8 D-05 typo-fail-fast; encrypt added in
// PII-ENCRYPT-08).
//
// Allowed: "replace" | "mask" | "hash" | "drop" | "encrypt". The
// five-mode set is hand-coded here rather than imported from
// internal/plugin/pii because (a) the set is part of the env-var
// contract operators see, so it is owned by config; (b) doing so
// avoids a config→plugin_pii arch-lint edge (TRST-04 boundary stays
// clean — config is upstream of the plugin layer in the dep graph).
func validatePIIMode(m string) error {
	switch m {
	case "replace", "mask", "hash", "drop", "encrypt":
		return nil
	default:
		return fmt.Errorf("unknown mode %q (allowed: replace, mask, hash, drop, encrypt)", m)
	}
}

// validatePIIEntities rejects any name not in the canonical entity
// set (see piiAllowedEntities below). Empty input returns nil
// (default = all entities active per D-02).
//
// The allowlist is hand-coded here for the same TRST-04 / arch-lint
// reason as validatePIIMode — the entity names are part of the
// env-var contract surfaced to operators in docs/operating.md. When
// internal/plugin/pii/recognizers.go ships a new recognizer, the
// docstring there + this validator + the operator docs all change
// together (intentional triple-check). Drift would surface as
// TestLoad_PIIEnabledEntities_Parsing failing if a new entity is
// shipped without updating this list.
// piiAllowedEntities is the hand-coded entity-name allowlist shared by
// validatePIIEntities and parsePIIEntityActions. Single source of truth
// per TRST-04 arch-lint boundary (config does not import internal/plugin/pii).
// When internal/plugin/pii/recognizers.go ships a new recognizer, this
// list + the operator docs change together (intentional triple-check).
var piiAllowedEntities = map[string]struct{}{
	"Email":       {},
	"IPv4":        {},
	"IPv6":        {},
	"SSN":         {},
	"CreditCard":  {},
	"USPhone":     {},
	"SIP_URI":     {},
	"IMEI":        {},
	"IMSI":        {},
	"MSISDN":      {},
	"MAC_ADDRESS": {},
	"COORDINATES": {},
	"SITE":        {},
	// Phase 08.4: US address coverage (PII-01). Order matches Recognizers
	// slice in internal/plugin/pii/recognizers.go (USAddress -> USState -> USZIP).
	"USAddress": {},
	"USState":   {},
	"USZIP":     {},
	// NER-emitted entity names (Task 11). Allowed in PII_ENABLED_ENTITIES
	// and PII_ENTITY_ACTIONS regardless of PII_NER_ENABLED; when NER is
	// disabled these names are dormant (no recognizer wired). When NER
	// is enabled, prose's PERSON / GPE-as-LOCATION outputs flow through
	// the same redact pipeline as regex recognizers.
	"PERSON":   {},
	"LOCATION": {},
}

// piiAllowedEntitiesList returns the allowlist as a sorted slice for use
// in human-readable error messages. Sorted so the order is stable across
// runs and Go's map iteration randomness doesn't leak into error text.
func piiAllowedEntitiesList() string {
	names := make([]string, 0, len(piiAllowedEntities))
	for n := range piiAllowedEntities {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func validatePIIEntities(names []string) error {
	if len(names) == 0 {
		return nil
	}
	var errs []error
	for _, n := range names {
		if _, ok := piiAllowedEntities[n]; !ok {
			errs = append(errs, fmt.Errorf("unknown entity %q (allowed: %s)", n, piiAllowedEntitiesList()))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// parsePIIEntityActions parses the PII_ENTITY_ACTIONS env value.
// Shape: "Entity:action,Entity:action,..." e.g. "Email:encrypt,SSN:drop".
// Returns (nil, nil) for an empty input. Validates every entity name
// against piiAllowedEntities and every action against the five-action
// set.
func parsePIIEntityActions(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	allowedActions := map[string]struct{}{
		"replace": {}, "mask": {}, "hash": {}, "drop": {}, "encrypt": {},
	}
	out := make(map[string]string)
	var errs []error
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			errs = append(errs, fmt.Errorf("malformed pair %q (expected Entity:action)", pair))
			continue
		}
		entity, action := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if _, ok := piiAllowedEntities[entity]; !ok {
			errs = append(errs, fmt.Errorf("unknown entity %q (allowed: %s)", entity, piiAllowedEntitiesList()))
			continue
		}
		if _, ok := allowedActions[action]; !ok {
			errs = append(errs, fmt.Errorf("unknown action %q for entity %q (allowed: replace, mask, hash, drop, encrypt)", action, entity))
			continue
		}
		out[entity] = action
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
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
// joinAllowedIPs renders []netip.Prefix back into a comma-separated
// string for use as the --allowed-ips flag default. Mirrors the
// "default seeded from env-resolved cfg" pattern used by
// --enabled-surfaces (line 585) so flag-unset = env-wins works without
// requiring an explicit Visit case to merge.
func joinAllowedIPs(prefixes []netip.Prefix) string {
	if len(prefixes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, ",")
}

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
