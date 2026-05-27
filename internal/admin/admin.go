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
//   - LogPath: path to the gateway log file consumed by the tailer in Plan 03.
//     Wired in Plan 01 for forward-compatibility; the tailer is not started
//     until Plan 03.
type Deps struct {
	Logger     *slog.Logger
	Version    string
	Commit     string
	Start      time.Time
	PoolDetail PoolDetailSource
	Registry   RegistryStatsSource
	LogPath    string
}

// handler holds the runtime state for the admin sub-router.
type handler struct {
	deps   Deps
	tailer *Tailer // wired in Plan 03 via NewTailer(deps.LogPath, deps.Logger)
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

	// Insertion point 1: construct the shared Tailer.
	// NewTailer does NOT start a goroutine — the first SSE Subscribe does.
	// This wiring does not affect the Plan 01 PageHandler/SnapshotHandler tests
	// because goleak only fires when a goroutine is actually started.
	h.tailer = NewTailer(deps.LogPath, deps.Logger)

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
func (h *handler) pageHandler(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Version string
		Commit  string
	}{
		Version: h.deps.Version,
		Commit:  h.deps.Commit,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := pageTemplate.Execute(w, data); err != nil {
		// Cannot write error response after WriteHeader — just log it.
		// The partial HTML body has already been written; the browser
		// will see a truncated page rather than an error envelope.
		h.deps.Logger.Error("admin: page render", "err", err)
	}
}
