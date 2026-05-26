package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/session"
)

// SessionDeleter is the narrow consumer-defined interface used by
// SessionsRouter to tear down a session by sid. *session.Registry's
// Delete(sid) error signature structurally satisfies this interface,
// so cmd/otto-gateway/main.go wires `a.registry` directly without an
// adapter. Tests inject a fake SessionDeleter so they don't need to
// stand up a real Registry.
type SessionDeleter interface {
	Delete(sid string) error
}

// SessionsRouter implements server.RouteRegistrar and mounts the single
// `DELETE /sessions/{id}` route onto the shared auth-protected /v1
// sub-router. The HTTP wire shape is locked by D-08:
//
//	200 {"deleted": "<id>"} on success
//	404 {"error": "unknown session"} on session.ErrSessionNotFound
//	500 {"error": "delete failed"} on any other Registry.Delete error
//
// Wiring lives in cmd/otto-gateway/main.go: a SurfaceMount with
// Prefix=cfg.OpenAIPathPrefix (default /v1) and Router=&SessionsRouter{...}.
// That places the route behind the same auth.Bearer + auth.IPAllowlist
// chain that protects the other /v1 surfaces.
type SessionsRouter struct {
	// Registry is the SessionDeleter the handler calls into. May be
	// supplied as the production *session.Registry directly (it satisfies
	// SessionDeleter structurally) or as a test fake.
	Registry SessionDeleter
	// Logger is used for structured logging of delete failures. May be
	// nil in tests; handleDelete falls back to LoggerFromCtx with a
	// safe fallback path.
	Logger *slog.Logger
}

// RegisterRoutes satisfies server.RouteRegistrar. Registers DELETE
// /sessions/{id} via the chi r.Delete call directly on the shared
// sub-router (never r.Mount("/", …) — D-01 anti-pattern).
func (sr *SessionsRouter) RegisterRoutes(r chi.Router) {
	r.Delete("/sessions/{id}", sr.handleDelete)
}

// handleDelete implements the D-08 wire contract.
//
// Flow:
//  1. Extract {id} via chi.URLParam. If empty (defensive guard — chi
//     usually routes empty {id} to a 404 before this handler runs),
//     write 400 with generic error envelope.
//  2. Call sr.Registry.Delete(sid). On error:
//     - errors.Is(err, session.ErrSessionNotFound) → 404 unknown session.
//     - any other error → log + 500 delete failed.
//  3. On success: 200 with body `{"deleted": "<sid>"}`.
func (sr *SessionsRouter) handleDelete(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "id")
	if sid == "" {
		writeJSONError(w, http.StatusBadRequest, "missing session id")
		return
	}

	if err := sr.Registry.Delete(sid); err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeJSONError(w, http.StatusNotFound, "unknown session")
			return
		}
		LoggerFromCtx(r.Context(), sr.Logger).Error("session: delete failed", "sid", sid, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": sid})
}
