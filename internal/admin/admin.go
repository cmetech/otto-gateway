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
// tailer is nil in Plan 01 — it is wired in Plan 03 when the SSE log-tail
// route is added. The field is declared here so the struct shape is stable
// across plans.
type handler struct {
	deps   Deps
	tailer interface{} // *Tailer — nil until Plan 03; typed as interface{} to avoid a forward-ref import cycle
}

// Handler returns a chi.Router mounting the admin sub-routes. The caller
// (internal/server.NewFromConfig) mounts the returned handler at /admin
// on the OUTER router, making all sub-routes auth-exempt per D-01/D-07.
//
// Routes (Plan 01 only — Plan 03 adds /logs/stream):
//
//	GET /          → pageHandler  (renders HTML page from embed.FS template)
//	GET /api/snapshot → snapshotHandler (aggregates pool+session data → JSON)
//	GET /static/*  → http.FileServer over staticFS (embedded CSS/JS)
func Handler(deps Deps) http.Handler {
	// Nil-safe logger: if no logger is provided substitute a no-op so
	// handler paths that call h.deps.Logger never panic on a nil deref.
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	h := &handler{deps: deps}

	r := chi.NewRouter()

	// GET / — serve the HTML admin page (template rendered with version/commit).
	r.Get("/", h.pageHandler)

	// GET /api/snapshot — return unified AdminSnapshot JSON (D-05).
	r.Get("/api/snapshot", h.snapshotHandler)

	// GET /static/* — serve embedded CSS/JS assets.
	// chi strips the /admin mount prefix before forwarding to this handler,
	// so this handler sees /static/css/admin.css (not /admin/static/...).
	// StripPrefix removes "/static/" so http.FileServer resolves paths
	// within the staticFS sub-FS correctly (css/admin.css → staticFS root).
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

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
