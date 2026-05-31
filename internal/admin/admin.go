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
	"log/slog"
	"net/http"
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
//	GET /             → pageHandler  (renders HTML page from embed.FS template)
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

	// GET / — serve the HTML admin page (template rendered with version/commit).
	r.Get("/", h.pageHandler)

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

// pageHandler serves GET /admin (the rendered HTML page).
// The template is executed against a struct containing version and commit
// strings baked in at render time; live pool/session data is hydrated
// by admin.js polling /admin/api/snapshot every 30s (D-06).
//
// WR-05 mitigation: render into a bytes.Buffer first so a template
// execution failure (corrupted embed, panic recovered as error) can
// still emit a clean 500 Internal Server Error envelope rather than
// committing 200 OK with truncated HTML on the wire.
func (h *handler) pageHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Version   string
		Commit    string
		Debug     bool
		ChatTrace bool
	}{
		Version:   h.deps.Version,
		Commit:    h.deps.Commit,
		Debug:     h.deps.Debug,
		ChatTrace: h.deps.ChatTrace,
	}
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
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
