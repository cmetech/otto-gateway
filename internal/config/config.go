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
	"path/filepath"
	"slices"
	"sort"
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
	// PIIRedactionHook applies. Empty = all six recognizers active. An
	// unknown name causes Load() to return an error (typo-fail-fast).
	// Loaded from PII_ENABLED_ENTITIES.
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

	// Phase 5 SESS-02 / D-10: SESSION_TTL_MS default 30 min (Node parity).
	// getEnvDuration accepts both Go duration strings ("30m") and
	// millisecond integers ("1800000") so the env name matches Node's
	// SESSION_TTL_MS exactly.
	sessionTTL, err := getEnvDuration("SESSION_TTL_MS", 30*time.Minute)
	if err != nil {
		errs = append(errs, err)
	}

	// Phase 5 D-06: SESSION_MAX cap (default 32). NEW env var — no Node
	// equivalent; documented in docs/operating.md.
	sessionMax, err := getEnvInt("SESSION_MAX", 32)
	if err != nil {
		errs = append(errs, err)
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

	// Phase 8 D-02 / D-05: five new env keys for the plugin chain.
	// ENABLED_HOOKS shape-only (chain.Filter does the typo check at
	// boot — see main.go). PII_* knobs validated here for shape +
	// mode-hash-requires-key invariant.
	enabledHooks := getEnvStrSliceComma("ENABLED_HOOKS", nil)

	piiEnabled, err := getEnvBool("PII_REDACTION_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}

	piiEntities := getEnvStrSliceComma("PII_ENABLED_ENTITIES", nil)
	if err := validatePIIEntities(piiEntities); err != nil {
		errs = append(errs, fmt.Errorf("PII_ENABLED_ENTITIES: %w", err))
	}

	piiMode := getEnvStr("PII_REDACTION_MODE", "replace")
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
	if chatTrace {
		if dir := filepath.Dir(chatTraceFile); dir != "" && dir != "." {
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				errs = append(errs, fmt.Errorf("CHAT_TRACE_FILE: parent unwritable %q: %w", dir, mkErr))
			}
		}
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
		JSONFormatSteeringEnabled: jsonFormatSteeringEnabled,
		ChatTrace:                 chatTrace,
		ChatTraceFile:             chatTraceFile,
		ChatTraceMaxAgeDays:       chatTraceMaxAgeDays,
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

// validatePIIEntities rejects any name not in the canonical six-entity
// recognizer set (Email, IPv4, IPv6, SSN, CreditCard, USPhone). Empty
// input returns nil (default = all entities active per D-02).
//
// The allowlist is hand-coded here for the same TRST-04 / arch-lint
// reason as validatePIIMode — the six entity names are part of the
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
// against the canonical six-entity set and every action against the
// five-action set.
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
