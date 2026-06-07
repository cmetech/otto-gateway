// Package admin provides the admin observability UI handler.
// It exposes GET /admin (HTML page), GET /admin/api/snapshot (JSON),
// and GET /admin/static/* (embedded CSS/JS assets) — all auth-exempt per D-01.
//
// Architectural boundary (TRST-04): this package imports only stdlib +
// github.com/go-chi/chi/v5 + otto-gateway/internal/version. It must NOT
// import internal/pool, internal/session, or internal/engine. The narrow
// consumer-defined interfaces PoolDetailSource and RegistryStatsSource
// (declared below) are the seam through which pool/session data flows in
// without breaking the dependency boundary. The cmd/otto-gateway wiring
// (main.go) provides adapter shims that translate from server's types to
// admin's types at the boundary.
package admin

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// PoolDetailSource is the consumer-defined interface the admin handler uses
// to retrieve per-slot detail rows. The method returns []SnapshotSlot (admin's
// own wire type) — satisfying adapters in cmd/otto-gateway/main.go translate
// from server.AgentSlot to admin.SnapshotSlot field-by-field.
//
// admin owns this interface so the package never needs to import internal/pool
// or internal/server to get slot data (TRST-04 invariant).
type PoolDetailSource interface {
	Detail() []SnapshotSlot
}

// RegistryStatsSource is the consumer-defined interface the admin handler uses
// to retrieve per-session detail rows. Like PoolDetailSource, it returns
// admin's own wire type ([]SnapshotSess) so this package stays boundary-clean.
type RegistryStatsSource interface {
	Detail() []SnapshotSess
}

// Deps bundles the inputs the admin Handler needs. All fields are optional
// (nil-safe guards in the handlers).
//
//   - Logger: structured logger; if nil a no-op logger is substituted.
//   - Version: binary version string, baked into the page at render time.
//   - Commit: VCS commit hash, baked into the page and the snapshot JSON.
//   - Start: the time the handler was wired up; used for uptime calculation.
//     Per RESEARCH Open Question 3 (RESOLVED): pass time.Now() at wire-up
//     rather than exporting server.Server.start; uptime drifts by a few ms,
//     which is acceptable.
//   - PoolDetail: nil-safe; when nil the snapshot returns pool.size=0.
//   - Registry: nil-safe; when nil the snapshot returns sessions=[].
//   - LogPaths: name→path map of tailable log sources (quick 260529-ll2).
//     Empty / nil means no sources are available; snapshot renders
//     log_sources: [] and SSE /logs/stream returns 400 for any source
//     value.
//   - LogPathOrder: the deterministic order LogPaths keys appear in the
//     admin UI dropdown and the snapshot log_sources array. The first
//     entry is the default SSE source. Filling LogPaths without
//     LogPathOrder means the SSE handler cannot resolve sources (the
//     validation uses slices.Contains on LogPathOrder).
//   - Debug: mirrors cfg.Debug — whether DEBUG-level structured logging is
//     enabled. Surfaced in the snapshot JSON and the HTML page so operators
//     can tell at a glance whether verbose logging is on.
//   - ChatTrace: mirrors cfg.ChatTrace — whether the SENSITIVE chat-trace
//     tracer is enabled. When on, raw prompts are written to disk; surfacing
//     this is a safety affordance so operators are not surprised by it.
type Deps struct {
	Logger       *slog.Logger
	Version      string
	Commit       string
	Start        time.Time
	PoolDetail   PoolDetailSource
	Registry     RegistryStatsSource
	LogPaths     map[string]string
	LogPathOrder []string
	Debug        bool
	ChatTrace    bool

	// Runtime cfg surfacing (quick 260601-a3z, step 3 of admin UI redesign).
	// These twelve fields mirror cfg.* values that the operator can otherwise
	// only inspect by grepping environment variables. They are rendered on the
	// /admin/about page (aboutHandler builds AboutData from these). All fields
	// are read-only snapshots taken at admin.Handler wire-up time; the admin
	// package never mutates them and never imports internal/config (TRST-04).
	HTTPAddr             string
	PoolSize             int
	SessionTTL           time.Duration
	StreamIdleTimeoutSec int
	AuthEnabled          bool
	IPAllowlistEnabled   bool
	KiroCmd              string
	KiroArgs             []string
	KiroCwd              string
	OllamaPathPrefix     string
	OpenAIPathPrefix     string
	AnthropicPathPrefix  string

	// Chat-trace file location and retention surfaced on /admin/docs
	// (quick 260601-aix, step 4 of admin UI redesign). Read-only
	// snapshots of cfg.ChatTraceFile / cfg.ChatTraceMaxAgeDays.
	ChatTraceFile       string
	ChatTraceMaxAgeDays int

	// PII config surfaced on /admin/about so an operator can read the
	// full PII posture without grepping env vars or /health/hooks.
	// T-8-LEAK: HashKey and EncryptKey VALUES are NEVER surfaced; only
	// booleans indicating whether the operator supplied a key. The
	// fields below are read-only snapshots taken at admin.Handler
	// wire-up time; the admin package never imports internal/config
	// (TRST-04 / arch-lint boundary).
	PIIRedactionEnabled bool
	PIIRedactionMode    string
	PIINEREnabled       bool
	PIIEnabledEntities  []string          // empty = all registered recognizers
	PIIEntityActions    map[string]string // empty = global Mode applies to all
	PIIHashKeySet       bool              // T-8-LEAK: bool only, never the value
	PIIEncryptKeySet    bool              // T-8-LEAK: bool only, never the value
}

// handler holds the runtime state for the admin sub-router.
type handler struct {
	deps    Deps
	tailers *TailerRegistry // quick 260529-ll2: replaces single *Tailer
}

// Handler returns a chi.Router mounting the admin sub-routes. The caller
// (internal/server.NewFromConfig) mounts the returned handler at /admin
// on the OUTER router, making all sub-routes auth-exempt per D-01/D-07.
//
// Routes:
//
//	GET /             → dashboardHandler (renders dashboard HTML from embed.FS templates)
//	GET /about        → aboutHandler (About page — placeholder, real content in a later step)
//	GET /docs         → docsHandler (Docs page — placeholder, real content in a later step)
//	GET /api/snapshot → snapshotHandler (aggregates pool+session data → JSON)
//	GET /static/*     → http.FileServer over staticFS (embedded CSS/JS)
//	GET /logs/stream  → sseHandler (SSE live log tail — D-08/D-09)
func Handler(deps Deps) http.Handler {
	// Nil-safe logger: if no logger is provided substitute a no-op so
	// handler paths that call h.deps.Logger never panic on a nil deref.
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	h := &handler{deps: deps}

	// Quick 260529-ll2: replace the single eager *Tailer with a lazy
	// TailerRegistry. Subscribe-time Get(name, path) constructs each
	// per-source *Tailer the first time it's requested; subsequent
	// requests share the cached instance. NewTailerRegistry does NOT
	// start any goroutine — the underlying NewTailer does that on the
	// first Subscribe call.
	h.tailers = NewTailerRegistry(deps.Logger)

	r := chi.NewRouter()

	// GET / — serve the dashboard HTML page (template rendered with version/commit).
	r.Get("/", h.dashboardHandler)

	// GET /about — placeholder About page (real content lands in a later step).
	r.Get("/about", h.aboutHandler)

	// GET /docs — placeholder Docs page (real content lands in a later step).
	r.Get("/docs", h.docsHandler)

	// GET /api/snapshot — return unified AdminSnapshot JSON (D-05).
	r.Get("/api/snapshot", h.snapshotHandler)

	// GET /static/* — serve embedded CSS/JS assets.
	// chi.Mount("/admin", h) does NOT rewrite r.URL.Path in the sub-router;
	// when called via the outer server the handler sees /admin/static/css/admin.css.
	// When called directly (unit tests) it sees /static/css/admin.css.
	// http.StripPrefix is applied with the path prefix seen in each context:
	//   - mounted (real server): strip "/admin/static/"
	//   - direct (unit tests):   strip "/static/"
	// We use the chi wildcard URLParam("*") to extract the file path and bypass
	// StripPrefix entirely, serving directly from staticFS regardless of mount context.
	// Audit admin-static-handler-accepts-any-http-method: r.Handle accepts
	// arbitrary methods (POST/PUT/DELETE etc.) and serves the asset 200 OK.
	// Not a security issue (static assets are public) but it diverges from
	// the rest of the admin surface which uses r.Get exclusively. Restrict
	// to GET so non-GET correctly returns 405.
	r.Get("/static/*", func(w http.ResponseWriter, req *http.Request) {
		// chi sets the wildcard URLParam("*") to the path portion after /static/
		// regardless of whether the handler is mounted or called directly.
		// This makes the FileServer mount-path-agnostic.
		filePath := chi.URLParam(req, "*")
		// G703 exemption: embed.FS rejects '..' traversal per Go 1.16+ spec;
		// staticFS is rooted at internal/admin/static/ (operator-public assets only).
		http.ServeFileFS(w, req, staticFS, filePath) //nolint:gosec // G703: embed.FS rejects '..' per Go 1.16+ spec; staticFS rooted at internal/admin/static/
	})

	// Insertion point 2: register the SSE log-tail route (D-08).
	// Route ordering: page → snapshot → static → SSE.
	r.Get("/logs/stream", h.sseHandler)

	return r
}

// dashboardHandler serves GET /admin (the rendered dashboard HTML page).
// The template is executed against a struct containing version and commit
// strings baked in at render time; live pool/session data is hydrated
// by admin.js polling /admin/api/snapshot every 30s (D-06).
//
// WR-05 mitigation: render into a bytes.Buffer first so a template
// execution failure (corrupted embed, panic recovered as error) can
// still emit a clean 500 Internal Server Error envelope rather than
// committing 200 OK with truncated HTML on the wire.
func (h *handler) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Version   string
		Commit    string
		Debug     bool
		ChatTrace bool
		TabActive string
	}{
		Version:   h.deps.Version,
		Commit:    h.deps.Commit,
		Debug:     h.deps.Debug,
		ChatTrace: h.deps.ChatTrace,
		TabActive: "dashboard",
	}
	var buf bytes.Buffer
	if err := dashboardTemplate.ExecuteTemplate(&buf, "base", data); err != nil {
		// Nothing committed to the wire yet — return a clean 500.
		h.deps.Logger.Error("admin: page render", "err", err)
		http.Error(w, "admin page render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		// Header already committed — log only. The client connection
		// likely went away; no recovery path exists.
		h.deps.Logger.Debug("admin: page write", "err", err)
	}
}

// aboutData is the render-time view-model for the /admin/about page
// (quick 260601-a3z, step 3 of admin UI redesign). All empty-string
// fallbacks (KiroCmd/KiroArgs/KiroCwd) and conditional display strings
// (StreamIdleDisplay, SessionTTL stringification) are substituted in
// aboutHandler so the template stays presentation-only.
type aboutData struct {
	TabActive            string
	PageTitle            string
	Version              string
	Commit               string
	GoVersion            string
	GOOS                 string
	GOARCH               string
	StartedAt            string
	HTTPAddr             string
	PoolSize             int
	SessionTTL           string
	StreamIdleTimeoutSec int
	StreamIdleDisplay    string
	AuthEnabled          bool
	IPAllowlistEnabled   bool
	Debug                bool
	ChatTrace            bool
	KiroCmd              string
	KiroArgs             string
	KiroCwd              string

	// PII view-model fields. Populated by aboutHandler from the cfg
	// snapshot in Deps. EntityActions is rendered as a sorted slice
	// (templates can't iterate a Go map in a stable order) and
	// EnabledEntitiesDisplay is "(all 15 active)" or the comma-joined
	// allowlist.
	PIIRedactionEnabled    bool
	PIIRedactionMode       string
	PIINEREnabled          bool
	PIIEncryptActive       bool // mode==encrypt OR any entity action is encrypt
	PIIEnabledEntitiesText string
	PIIEntityActionRows    []piiEntityActionRow
	PIIHashKeySet          bool
	PIIEncryptKeySet       bool
}

// piiEntityActionRow is one row in the per-entity action override
// table on /admin/about. Sorted by entity name in aboutHandler so the
// template iterates a stable list.
type piiEntityActionRow struct {
	Entity string
	Action string
}

// aboutHandler serves GET /admin/about — renders four populated cards
// (Build info, Runtime status, Feature flags, Upstream worker) plus the
// Identity banner from cfg + runtime values. Quick 260601-cx3 trimmed
// the Endpoints card and project-links footer (moved Endpoints reference
// to /admin/docs). Uses the same WR-05 buffer-then-write pattern as
// dashboardHandler so a template render failure produces a clean 500
// rather than a truncated 200.
func (h *handler) aboutHandler(w http.ResponseWriter, r *http.Request) {
	// Empty-string fallbacks done in the handler (not the template) so the
	// template stays presentation-only.
	kiroCmd := h.deps.KiroCmd
	if kiroCmd == "" {
		kiroCmd = "(unset — degraded mode)"
	}
	kiroArgs := strings.Join(h.deps.KiroArgs, " ")
	if kiroArgs == "" {
		kiroArgs = "(none)"
	}
	kiroCwd := h.deps.KiroCwd
	if kiroCwd == "" {
		kiroCwd = "(empty)"
	}
	streamIdleDisplay := "disabled"
	if h.deps.StreamIdleTimeoutSec != 0 {
		streamIdleDisplay = fmt.Sprintf("%ds", h.deps.StreamIdleTimeoutSec)
	}
	startedAt := ""
	if !h.deps.Start.IsZero() {
		startedAt = h.deps.Start.Format(time.RFC3339)
	}

	// PII view-model assembly. EncryptActive mirrors the predicate from
	// internal/plugin/pii (mode==encrypt OR any per-entity override is
	// encrypt). EnabledEntitiesText collapses the empty-allowlist case
	// to a human-readable "(all registered recognizers active)" so the
	// template doesn't need conditional logic. EntityActions is sorted
	// by entity for stable rendering.
	piiEncryptActive := h.deps.PIIRedactionMode == "encrypt"
	for _, a := range h.deps.PIIEntityActions {
		if a == "encrypt" {
			piiEncryptActive = true
			break
		}
	}
	piiEnabledEntitiesText := "(all registered recognizers active)"
	if len(h.deps.PIIEnabledEntities) > 0 {
		piiEnabledEntitiesText = strings.Join(h.deps.PIIEnabledEntities, ", ")
	}
	var piiEntityActionRows []piiEntityActionRow
	if len(h.deps.PIIEntityActions) > 0 {
		entities := make([]string, 0, len(h.deps.PIIEntityActions))
		for e := range h.deps.PIIEntityActions {
			entities = append(entities, e)
		}
		sort.Strings(entities)
		piiEntityActionRows = make([]piiEntityActionRow, 0, len(entities))
		for _, e := range entities {
			piiEntityActionRows = append(piiEntityActionRows, piiEntityActionRow{
				Entity: e,
				Action: h.deps.PIIEntityActions[e],
			})
		}
	}

	data := aboutData{
		TabActive:            "about",
		PageTitle:            "About",
		Version:              h.deps.Version,
		Commit:               h.deps.Commit,
		GoVersion:            runtime.Version(),
		GOOS:                 runtime.GOOS,
		GOARCH:               runtime.GOARCH,
		StartedAt:            startedAt,
		HTTPAddr:             h.deps.HTTPAddr,
		PoolSize:             h.deps.PoolSize,
		SessionTTL:           h.deps.SessionTTL.String(),
		StreamIdleTimeoutSec: h.deps.StreamIdleTimeoutSec,
		StreamIdleDisplay:    streamIdleDisplay,
		AuthEnabled:          h.deps.AuthEnabled,
		IPAllowlistEnabled:   h.deps.IPAllowlistEnabled,
		Debug:                h.deps.Debug,
		ChatTrace:            h.deps.ChatTrace,
		KiroCmd:              kiroCmd,
		KiroArgs:             kiroArgs,
		KiroCwd:              kiroCwd,

		PIIRedactionEnabled:    h.deps.PIIRedactionEnabled,
		PIIRedactionMode:       h.deps.PIIRedactionMode,
		PIINEREnabled:          h.deps.PIINEREnabled,
		PIIEncryptActive:       piiEncryptActive,
		PIIEnabledEntitiesText: piiEnabledEntitiesText,
		PIIEntityActionRows:    piiEntityActionRows,
		PIIHashKeySet:          h.deps.PIIHashKeySet,
		PIIEncryptKeySet:       h.deps.PIIEncryptKeySet,
	}
	var buf bytes.Buffer
	if err := aboutTemplate.ExecuteTemplate(&buf, "base", data); err != nil {
		h.deps.Logger.Error("admin: about render", "err", err)
		http.Error(w, "admin about render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.deps.Logger.Debug("admin: about write", "err", err)
	}
}

// envVarRow is one row in the /admin/docs environment-variables
// reference table (quick 260601-aix). CurrentValue is computed by
// docsHandler at request time from the Deps snapshot (taken at
// wire-up). AUTH_TOKEN row's CurrentValue is "(set)"/"(unset)" —
// never the plaintext token characters.
type envVarRow struct {
	Name         string
	Default      string
	Description  string
	CurrentValue string
}

// cliFlagRow is one row in the /admin/docs CLI-flag / env-var
// mapping table (quick 260601-aix). The flag/env mapping is
// hand-mirrored from internal/config/config.go LoadArgs() so the
// admin package does NOT import internal/config (TRST-04 boundary).
type cliFlagRow struct {
	Flag       string
	EnvMapping string
	Notes      string
}

// docsData is the render-time view-model for the /admin/docs page
// (quick 260601-aix). The two table slices are populated in
// docsHandler from in-handler seed lists; path prefixes and the
// chat-trace block are copied from h.deps.*.
type docsData struct {
	TabActive           string
	PageTitle           string
	Version             string
	Commit              string
	EnvVars             []envVarRow
	CliFlags            []cliFlagRow
	ChatTraceEnabled    bool
	ChatTraceFile       string
	ChatTraceMaxAgeDays int
	OllamaPathPrefix    string
	OpenAIPathPrefix    string
	AnthropicPathPrefix string
}

// docsHandler serves GET /admin/docs — operator reference page with
// environment variable table (live current values), files & paths,
// CLI flags, endpoints reference, basic usage, and troubleshooting
// (quick 260601-aix, step 4 of admin UI redesign). Uses the same
// WR-05 buffer-then-write pattern as aboutHandler so a template
// render failure produces a clean 500 rather than a truncated 200.
//
// AUTH_TOKEN safety: the AUTH_TOKEN row's CurrentValue is rendered
// as "(set)" or "(unset)" — never the plaintext token characters.
// ALLOWED_IPS is rendered as on/off for the same compactness reason
// (and because rendering CIDR lists in a table is noisy).
func (h *handler) docsHandler(w http.ResponseWriter, r *http.Request) {
	boolOnOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	truncate := func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	}

	authCurrent := "(unset)"
	if h.deps.AuthEnabled {
		authCurrent = "(set)"
	}
	allowedIPsCurrent := "(unset)"
	if h.deps.IPAllowlistEnabled {
		allowedIPsCurrent = "(set)"
	}
	kiroCmdCurrent := h.deps.KiroCmd
	if kiroCmdCurrent == "" {
		kiroCmdCurrent = "(unset — degraded mode)"
	}
	kiroArgsCurrent := strings.Join(h.deps.KiroArgs, " ")
	if kiroArgsCurrent == "" {
		kiroArgsCurrent = "(none)"
	}
	kiroArgsCurrent = truncate(kiroArgsCurrent, 40)
	kiroCwdCurrent := h.deps.KiroCwd
	if kiroCwdCurrent == "" {
		kiroCwdCurrent = "(empty)"
	}
	streamIdleCurrent := "disabled"
	if h.deps.StreamIdleTimeoutSec != 0 {
		streamIdleCurrent = fmt.Sprintf("%ds", h.deps.StreamIdleTimeoutSec)
	}
	chatTraceFileCurrent := h.deps.ChatTraceFile
	if !h.deps.ChatTrace {
		chatTraceFileCurrent = "(disabled — CHAT_TRACE=false)"
	}

	envVars := []envVarRow{
		{Name: "HTTP_ADDR", Default: "127.0.0.1:18080", Description: "HTTP listen address. Set to :18080 to bind all interfaces.", CurrentValue: h.deps.HTTPAddr},
		{Name: "KIRO_CMD", Default: "kiro-cli", Description: "kiro-cli binary name or path resolved on PATH. Empty value puts the gateway in degraded mode.", CurrentValue: kiroCmdCurrent},
		{Name: "KIRO_ARGS", Default: "acp", Description: "Whitespace-split argv passed to KIRO_CMD.", CurrentValue: kiroArgsCurrent},
		{Name: "KIRO_CWD", Default: "(empty)", Description: "Working directory for the kiro-cli subprocess. Empty = inherit gateway cwd.", CurrentValue: kiroCwdCurrent},
		{Name: "POOL_SIZE", Default: "4", Description: "Number of warm kiro-cli subprocesses kept in the pool.", CurrentValue: strconv.Itoa(h.deps.PoolSize)},
		{Name: "SESSION_TTL_MS", Default: "1800000 (30m)", Description: "Idle stateful-session reap threshold. Accepts ms-integer (Node parity) or Go duration string.", CurrentValue: h.deps.SessionTTL.String()},
		{Name: "STREAM_IDLE_TIMEOUT_SEC", Default: "30", Description: "Server-side idle-stream watchdog (0 disables, negative = boot error).", CurrentValue: streamIdleCurrent},
		{Name: "AUTH_TOKEN", Default: "(unset)", Description: "Comma-split bearer-token allowlist. Empty = auth disabled (Node parity). Rendered as on/off — never the plaintext value.", CurrentValue: authCurrent},
		{Name: "ALLOWED_IPS", Default: "(unset)", Description: "Comma-split CIDR/IP allowlist. Empty = allow-all (Node parity). Rendered as on/off for compactness.", CurrentValue: allowedIPsCurrent},
		{Name: "AUTH_TRUST_XFF", Default: "false", Description: "Trust X-Forwarded-For in the IP allowlist check. Enable ONLY behind a known reverse proxy.", CurrentValue: "(see startup log)"},
		{Name: "DEBUG", Default: "false", Description: "Enables debug-level structured logging (slog JSON).", CurrentValue: boolOnOff(h.deps.Debug)},
		{Name: "CHAT_TRACE", Default: "false", Description: "SENSITIVE — when true, writes raw user prompts to CHAT_TRACE_FILE.", CurrentValue: boolOnOff(h.deps.ChatTrace)},
		{Name: "CHAT_TRACE_FILE", Default: "./logs/otto-gateway-chat-trace.log (or sibling of LOG_FILE)", Description: "On-disk path of the chat-trace NDJSON log. Only opened when CHAT_TRACE=true.", CurrentValue: chatTraceFileCurrent},
		{Name: "CHAT_TRACE_MAX_AGE_DAYS", Default: "3", Description: "timberjack MaxAge in days for chat-trace.log rotation pruning.", CurrentValue: strconv.Itoa(h.deps.ChatTraceMaxAgeDays)},
		{Name: "OLLAMA_PATH_PREFIX", Default: "/api", Description: "Route prefix mounting the Ollama surface.", CurrentValue: h.deps.OllamaPathPrefix},
		{Name: "OPENAI_PATH_PREFIX", Default: "/v1", Description: "Route prefix mounting the OpenAI surface.", CurrentValue: h.deps.OpenAIPathPrefix},
		{Name: "ANTHROPIC_PATH_PREFIX", Default: "/v1", Description: "Route prefix mounting the Anthropic surface (shared with OpenAI; endpoint-level disambiguation).", CurrentValue: h.deps.AnthropicPathPrefix},
		{Name: "ENABLED_SURFACES", Default: "ollama,anthropic,openai", Description: "Comma-split list of HTTP surfaces constructed at boot.", CurrentValue: "(see startup log)"},
		{Name: "ENABLED_HOOKS", Default: "(empty = all)", Description: "Comma-split allowlist of plugin hook names. Empty = all hooks in the chain enabled (permissive default).", CurrentValue: "(see startup log)"},
		{Name: "PII_REDACTION_ENABLED", Default: "true", Description: "Master switch for PIIRedactionHook (secure-by-default). Set false to keep the hook in the chain but inert.", CurrentValue: "(see startup log)"},
		{Name: "PII_REDACTION_MODE", Default: "encrypt", Description: "One of replace / mask / hash / drop / encrypt. Default 'encrypt': PII flows to the worker as AES-256-GCM ciphertext and is decrypted back to plaintext before the client sees the response (round-trip). mode=hash REQUIRES PII_HASH_KEY; mode=encrypt (or any PII_ENTITY_ACTIONS entry of the form Entity:encrypt) REQUIRES PII_ENCRYPT_KEY. Boot error otherwise.", CurrentValue: "(see startup log)"},
		{Name: "PII_NER_ENABLED", Default: "true", Description: "Master switch for the prose-based NER recognizer that emits PERSON and LOCATION spans alongside the regex recognizers. Default true (secure-by-default); set false to skip the runtime tokenizer/tagger allocation. English-only; weaker on Asian / multilingual names.", CurrentValue: "(see startup log)"},
		{Name: "PII_ENABLED_ENTITIES", Default: "(empty = all 15)", Description: "Comma-split allowlist. Regex: Email, IPv4, IPv6, SSN, CreditCard, USPhone, SIP_URI, IMEI, IMSI, MSISDN, MAC_ADDRESS, COORDINATES, SITE. NER (requires PII_NER_ENABLED=true): PERSON, LOCATION.", CurrentValue: "(see startup log)"},
		{Name: "PII_ENTITY_ACTIONS", Default: "(empty)", Description: "Per-entity action override map. Shape: Entity:action,Entity:action,… (e.g. Email:encrypt,SSN:drop,PERSON:mask). Allowed actions: replace, mask, hash, drop, encrypt. Unlisted entities fall back to PII_REDACTION_MODE. Unknown entity or action ⇒ boot error.", CurrentValue: "(see startup log)"},
		{Name: "PII_HASH_KEY", Default: "(auto-minted by `otto-gw init`)", Description: "HMAC-SHA256 key required when PII_REDACTION_MODE=hash. Auto-generated at install; --regenerate-secrets rotates it. Rotating invalidates prior correlation tokens.", CurrentValue: "(see startup log)"},
		{Name: "PII_ENCRYPT_KEY", Default: "(auto-minted by `otto-gw init`)", Description: "Required when encrypt is active anywhere (the default PII_REDACTION_MODE=encrypt, OR any PII_ENTITY_ACTIONS entry of the form Entity:encrypt). Any non-empty string; the gateway derives a 32-byte AES-256-GCM key via SHA-256 at boot. Auto-generated at install; --regenerate-secrets rotates it alongside AUTH_TOKEN and PII_HASH_KEY. Rotating invalidates every prior encrypted token (breaking change for round-tripping clients).", CurrentValue: "(see startup log)"},
		{Name: "SESSION_MAX", Default: "32", Description: "Cap on concurrent stateful sessions. Lazy-create over the cap returns 503.", CurrentValue: "(see startup log)"},
		{Name: "SESSION_TICK_INTERVAL_MS", Default: "60000 (60s)", Description: "Cadence of the registry reaper goroutine. Test injection seam.", CurrentValue: "(see startup log)"},
		{Name: "PING_INTERVAL", Default: "60s", Description: "kiro-cli heartbeat interval. Accepts ms-integer or Go duration string.", CurrentValue: "(see startup log)"},
		{Name: "LOG_FILE", Default: "(unset)", Description: "When set, slog JSON also writes to this rotated file. Empty = stdout/stderr only.", CurrentValue: "(see startup log)"},
	}

	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	cliFlags := []cliFlagRow{
		{Flag: "--http-addr", EnvMapping: "HTTP_ADDR", Notes: "HTTP listen address."},
		{Flag: "--kiro-cmd", EnvMapping: "KIRO_CMD", Notes: "kiro-cli binary name or path."},
		{Flag: "--kiro-args", EnvMapping: "KIRO_ARGS", Notes: "Whitespace-split argv."},
		{Flag: "--kiro-cwd", EnvMapping: "KIRO_CWD", Notes: "Working directory for kiro-cli."},
		{Flag: "--debug", EnvMapping: "DEBUG", Notes: "Enable debug-level slog output."},
		{Flag: "--ping-interval", EnvMapping: "PING_INTERVAL", Notes: "Go duration string."},
		{Flag: "--pool-size", EnvMapping: "POOL_SIZE", Notes: "Warm subprocess count."},
		{Flag: "--session-ttl", EnvMapping: "SESSION_TTL_MS", Notes: "Go duration; env also accepts ms-integer."},
		{Flag: "--session-max", EnvMapping: "SESSION_MAX", Notes: "Concurrent stateful-session cap."},
		{Flag: "--enabled-surfaces", EnvMapping: "ENABLED_SURFACES", Notes: "Comma-split list."},
		{Flag: "--ollama-path-prefix", EnvMapping: "OLLAMA_PATH_PREFIX", Notes: "Ollama surface route prefix."},
		{Flag: "--openai-path-prefix", EnvMapping: "OPENAI_PATH_PREFIX", Notes: "OpenAI surface route prefix."},
		{Flag: "--anthropic-path-prefix", EnvMapping: "ANTHROPIC_PATH_PREFIX", Notes: "Anthropic surface route prefix."},
		{Flag: "--allowed-ips", EnvMapping: "ALLOWED_IPS", Notes: "Comma-split CIDR/IP allowlist."},
		{Flag: "--auth-trust-xff", EnvMapping: "AUTH_TRUST_XFF", Notes: "Trust X-Forwarded-For in allowlist check."},
		{Flag: "--version", EnvMapping: "(n/a)", Notes: "Print version and exit."},
		{Flag: "(env-only — no flag)", EnvMapping: "AUTH_TOKEN", Notes: "Secret; intentionally env-only (never argv)."},
	}

	data := docsData{
		TabActive:           "docs",
		PageTitle:           "Documentation",
		Version:             h.deps.Version,
		Commit:              h.deps.Commit,
		EnvVars:             envVars,
		CliFlags:            cliFlags,
		ChatTraceEnabled:    h.deps.ChatTrace,
		ChatTraceFile:       h.deps.ChatTraceFile,
		ChatTraceMaxAgeDays: h.deps.ChatTraceMaxAgeDays,
		OllamaPathPrefix:    h.deps.OllamaPathPrefix,
		OpenAIPathPrefix:    h.deps.OpenAIPathPrefix,
		AnthropicPathPrefix: h.deps.AnthropicPathPrefix,
	}

	var buf bytes.Buffer
	if err := docsTemplate.ExecuteTemplate(&buf, "base", data); err != nil {
		h.deps.Logger.Error("admin: docs render", "err", err)
		http.Error(w, "admin docs render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.deps.Logger.Debug("admin: docs write", "err", err)
	}
}
