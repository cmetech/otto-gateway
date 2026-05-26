package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// AgentsResponse is the body returned by GET /health/agents (D-14).
// Shape is locked by D-14/D-15/D-16; consumer dashboards depend on the
// snake_case wire contract verbatim. The handler always writes BOTH
// top-level keys (pool + sessions) so clients can decode into a
// strictly-typed struct without nil-key fallbacks.
type AgentsResponse struct {
	Pool     AgentsPool     `json:"pool"`
	Sessions []AgentSession `json:"sessions"`
}

// AgentsPool is the pool-side sub-object of AgentsResponse. Size/Alive/Busy
// summarize the slot vector; Slots is the per-row detail (D-15).
type AgentsPool struct {
	Size  int         `json:"size"`
	Alive int         `json:"alive"`
	Busy  int         `json:"busy"`
	Slots []AgentSlot `json:"slots"`
}

// AgentSlot is the per-slot detail row consumed by GET /health/agents (D-15).
// The JSON tags are the load-bearing wire contract. CurrentSessionID is
// *string so an idle slot renders as `"current_session_id": null` rather than
// the empty string.
//
// Note (Task 1 architectural choice): server declares its OWN AgentSlot
// type rather than re-exporting pool.AgentSlot. The cmd/otto-gateway
// poolDetailAdapter (in main.go) converts pool.AgentSlot → server.AgentSlot
// field-by-field. This keeps the engine-boundary discipline: server does
// not import internal/pool, the adapter does the mapping. The two types
// have structurally identical JSON shapes so the wire contract is
// preserved at compile-time by the field set.
type AgentSlot struct {
	Label            string  `json:"label"`
	Alive            bool    `json:"alive"`
	Busy             bool    `json:"busy"`
	CurrentSessionID *string `json:"current_session_id"`
}

// AgentSession is the per-session detail row (D-16) for /health/agents.
// JSON tags lock the wire contract. Model is *string so an unset model
// renders as `"model": null` rather than an empty string. LastUsed
// marshals to RFC 3339 via stdlib time.Time.MarshalJSON.
type AgentSession struct {
	ID       string    `json:"id"`
	Alive    bool      `json:"alive"`
	Busy     bool      `json:"busy"`
	LastUsed time.Time `json:"last_used"`
	Model    *string   `json:"model"`
}

// PoolDetailSource is the consumer-defined interface used by agentsHandler
// to render per-slot detail rows. *pool.Pool's Detail() method returns
// []pool.AgentSlot — the cmd/otto-gateway poolDetailAdapter converts those
// to []server.AgentSlot and satisfies this interface.
type PoolDetailSource interface {
	Detail() []AgentSlot
}

// agentsHandler handles GET /health/agents (D-14).
//
// Wire shape:
//
//	{
//	  "pool": {"size": N, "alive": A, "busy": B, "slots": [...]},
//	  "sessions": [...]
//	}
//
// Nil-safety:
//   - When s.poolDetail is nil, Pool defaults to {0,0,0,nil}.
//   - When s.registry is nil, Sessions stays nil (encodes as null per
//     stdlib JSON; consumers tolerate null in addition to []).
//
// D-18 routing: registered on the OUTER router (auth-exempt) by
// NewFromConfig alongside /health.
func (s *Server) agentsHandler(w http.ResponseWriter, r *http.Request) {
	resp := AgentsResponse{}
	if s.poolDetail != nil {
		slots := s.poolDetail.Detail()
		resp.Pool.Slots = slots
		resp.Pool.Size = len(slots)
		for _, sl := range slots {
			if sl.Alive {
				resp.Pool.Alive++
			}
			if sl.Busy {
				resp.Pool.Busy++
			}
		}
	}
	if s.registry != nil {
		resp.Sessions = s.registry.Detail()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Cannot write error response after WriteHeader — just log it.
		LoggerFromCtx(r.Context(), s.logger).Error("agents encode", "err", err)
	}
}

// writeJSONError writes a generic JSON error envelope used by server-internal
// endpoints whose response shape is operator-only (not surface-shaped). The
// envelope is `{"error": "<msg>"}`. Per-surface error envelopes (Anthropic,
// OpenAI, Ollama) are rendered by their respective adapters.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
