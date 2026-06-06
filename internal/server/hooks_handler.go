// Phase 8 OBSV-04 — GET /health/hooks JSON envelope handler.
//
// Read-only chain introspection. Auth-exempt (mounted on the OUTER
// router alongside /health and /health/agents per server.go:196-200
// precedent). Per SC7: NO runtime mutate path — restart to change
// config. Mutating verbs (POST/PUT/DELETE) return 405 with Allow: GET.
//
// T-8-LEAK enforcement (RESEARCH Pitfall 9): the response body MUST
// NOT contain secrets — no token values, no regex sources, no hash key
// bytes. Each hook's Describe() owns its safe-to-publish whitelist;
// the handler is a pass-through. The handler-level
// TestHooksHandler_SecretOmissionAudit asserts the wire shape cannot
// regress to leaking sentinels even if a future hook accidentally
// publishes one.
//
// LoggingHook dedup: LoggingHook implements both engine.PreHook and
// engine.PostHook and therefore appears in BOTH chain.Pre and
// chain.Post slices. To keep the /health/hooks response shape
// de-duplicated, the handler concatenates Pre followed by Post but
// elides any Post-side entry whose Name already appeared on the Pre
// side. The convention is: the FIRST occurrence (Pre side) reports
// the combined kind via its Describe() return ("Pre,Post") and
// represents the hook in the response. This is the agreed pinned
// convention for slice 5 /health/hooks output.

package server

import (
	"encoding/json"
	"net/http"
)

// HookDescription is the per-hook wire row returned in /health/hooks
// (OBSV-04). JSON tags are the load-bearing wire contract — operators
// and dashboards depend on the snake_case-equivalent names.
//
// This type is declared STRUCTURALLY here in server/ (NOT imported
// from internal/plugin) to keep server's import surface free of
// internal/plugin. The cmd/otto-gateway hooksDescriptionAdapter
// (Task 5) does the field-copy from plugin.HookDescription. Mirror of
// the server.AgentSlot pattern at agents.go:40-45.
type HookDescription struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
	// LastError is the most recent error the hook surfaced during a
	// Pre/Post invocation, or empty when the hook is healthy. The
	// engine's HookErrorReporter populates this; the tray's degraded
	// state lights up when any enabled hook has a non-empty value.
	LastError string `json:"last_error,omitempty"`
}

// HooksDescriptionSource is the consumer-defined interface hooksHandler
// uses to fetch the registered chain. The cmd/otto-gateway
// hooksDescriptionAdapter wraps *plugin.Chain to satisfy this without
// importing internal/plugin into server's public surface (TRST-04).
// A nil source produces the canonical empty-shape response.
type HooksDescriptionSource interface {
	Describe() (pre, post []HookDescription)
}

// HooksResponse is the body returned by GET /health/hooks. Shape is
// locked by RESEARCH Example 6 and Phase 8 SC7 — consumer dashboards
// depend on the single flat `hooks` array (Pre rows then Post rows,
// with dedup of names that appear in both slices).
type HooksResponse struct {
	Hooks []HookDescription `json:"hooks"`
}

// hooksHandler handles GET /health/hooks (OBSV-04).
//
// Wire shape:
//
//	{"hooks": [{"name": "...", "kind": "Pre|Post|Pre,Post",
//	            "enabled": true, "config": {...}}]}
//
// Nil-safety: when s.hooks is nil, response is {"hooks": []} (empty
// non-nil array; never null).
//
// SC7: non-GET methods return 405 with Allow: GET. Defense-in-depth —
// chi's .Get() registration already routes only GET, so a POST would
// normally return 405 via chi's MethodNotAllowedHandler, but spelling
// it out here makes the no-mutate-path contract explicit at the
// handler layer.
func (s *Server) hooksHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	resp := HooksResponse{Hooks: []HookDescription{}}
	if s.hooks != nil {
		pre, post := s.hooks.Describe()
		// Build a name-set of Pre entries so we can elide Post-side
		// duplicates (LoggingHook dedup convention). The FIRST
		// occurrence (Pre) wins; its Describe() return reports the
		// combined kind via "Pre,Post".
		seen := make(map[string]struct{}, len(pre))
		for _, p := range pre {
			resp.Hooks = append(resp.Hooks, p)
			seen[p.Name] = struct{}{}
		}
		for _, p := range post {
			if _, dup := seen[p.Name]; dup {
				continue
			}
			resp.Hooks = append(resp.Hooks, p)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		LoggerFromCtx(r.Context(), s.logger).Error("hooks encode", "err", err)
	}
}
