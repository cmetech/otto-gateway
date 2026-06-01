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
	r.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// chi sets the wildcard URLParam("*") to the path portion after /static/
		// regardless of whether the handler is mounted or called directly.
		// This makes the FileServer mount-path-agnostic.
		filePath := chi.URLParam(req, "*")
		http.ServeFileFS(w, req, staticFS, filePath)
	}))

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
	OllamaPathPrefix     string
	OpenAIPathPrefix     string
	AnthropicPathPrefix  string
}

// aboutHandler serves GET /admin/about — renders six populated cards
// (Identity, Build, Runtime, Feature flags, Upstream worker, Endpoints,
// Project links) from cfg + runtime values (quick 260601-a3z step 3).
// Uses the same WR-05 buffer-then-write pattern as dashboardHandler so a
// template render failure produces a clean 500 rather than a truncated 200.
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
		OllamaPathPrefix:     h.deps.OllamaPathPrefix,
		OpenAIPathPrefix:     h.deps.OpenAIPathPrefix,
		AnthropicPathPrefix:  h.deps.AnthropicPathPrefix,
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

// docsHandler serves GET /admin/docs — a placeholder page during step 1
// of the admin UI redesign. Real content lands in a later step. Uses the
// same WR-05 buffer-then-write pattern as dashboardHandler so a template
// render failure produces a clean 500 rather than a truncated 200.
func (h *handler) docsHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		TabActive string
		PageTitle string
	}{
		TabActive: "docs",
		PageTitle: "Documentation",
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
