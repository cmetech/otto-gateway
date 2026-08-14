package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const modelCatalogMaxRetryAfterSeconds = 30

// ModelCatalogModel is the safe dashboard projection of one selectable model.
// It deliberately carries no registry evidence or upstream payload fields.
type ModelCatalogModel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	SelectionMode string            `json:"selection_mode"`
	Capabilities  map[string]string `json:"capabilities"`
}

// ModelCatalogRefreshView describes the bounded refresh lifecycle state shown
// to an administrator. Time fields are normalized by the HTTP handler.
type ModelCatalogRefreshView struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int64  `json:"interval_seconds"`
	InProgress      bool   `json:"in_progress"`
	LastAttemptAt   string `json:"last_attempt_at,omitempty"`
	LastSuccessAt   string `json:"last_success_at,omitempty"`
	LastUpdatedAt   string `json:"last_updated_at,omitempty"`
	NextAttemptAt   string `json:"next_attempt_at,omitempty"`
	LastOutcome     string `json:"last_outcome"`
	PendingRemovals int    `json:"pending_removals"`
}

// ModelCatalogView is the consumer-owned catalog read model supplied through
// Deps. It is intentionally independent of pool, registry, engine, and
// session implementation types.
type ModelCatalogView struct {
	State      string                  `json:"state"`
	Count      int                     `json:"count"`
	Generation uint64                  `json:"generation"`
	Models     []ModelCatalogModel     `json:"models"`
	Refresh    ModelCatalogRefreshView `json:"refresh"`
}

// ModelCatalogActionResult is the consumer-owned result of a manual refresh.
// Message is accepted from the adapter but never returned verbatim: handlers
// use fixed messages so upstream error text cannot cross this boundary.
type ModelCatalogActionResult struct {
	Outcome           string `json:"outcome,omitempty"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
}

// ModelCatalogSource is the narrow admin-owned interface for reading and
// manually refreshing the live model catalog.
type ModelCatalogSource interface {
	Snapshot() ModelCatalogView
	Refresh(context.Context) ModelCatalogActionResult
}

func (h *handler) modelCatalogHandler(w http.ResponseWriter, r *http.Request) {
	view := ModelCatalogView{State: "disabled", Models: []ModelCatalogModel{}}
	if src := h.deps.ModelCatalog; src != nil {
		view = src.Snapshot()
	}
	writeModelCatalogJSON(w, http.StatusOK, sanitizeModelCatalogView(view), h)
}

func (h *handler) modelCatalogRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if rejectCrossOriginModelCatalogRequest(r) {
		writeModelCatalogJSON(w, http.StatusForbidden, ModelCatalogActionResult{
			Code:    "catalog_refresh_forbidden",
			Message: "Cross-origin model catalog refresh is not allowed.",
		}, h)
		return
	}
	if h.deps.ModelCatalog == nil {
		writeModelCatalogJSON(w, http.StatusServiceUnavailable, ModelCatalogActionResult{
			Code:    "catalog_refresh_unavailable",
			Message: "Model catalog refresh is unavailable.",
		}, h)
		return
	}

	result := sanitizeModelCatalogAction(h.deps.ModelCatalog.Refresh(r.Context()))
	status := modelCatalogRefreshStatus(result.Code)
	if result.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfterSeconds))
	}
	writeModelCatalogJSON(w, status, result, h)
}

func sanitizeModelCatalogView(view ModelCatalogView) ModelCatalogView {
	view.State = modelCatalogState(view.State)
	view.Models = sanitizedModelCatalogModels(view.Models)
	view.Count = len(view.Models)
	view.Refresh.IntervalSeconds = clampModelCatalogInterval(view.Refresh.IntervalSeconds)
	view.Refresh.LastAttemptAt = normalizeModelCatalogTime(view.Refresh.LastAttemptAt)
	view.Refresh.LastSuccessAt = normalizeModelCatalogTime(view.Refresh.LastSuccessAt)
	view.Refresh.LastUpdatedAt = normalizeModelCatalogTime(view.Refresh.LastUpdatedAt)
	view.Refresh.NextAttemptAt = normalizeModelCatalogTime(view.Refresh.NextAttemptAt)
	view.Refresh.LastOutcome = modelCatalogOutcome(view.Refresh.LastOutcome)
	if view.Refresh.PendingRemovals < 0 {
		view.Refresh.PendingRemovals = 0
	}
	return view
}

func sanitizedModelCatalogModels(source []ModelCatalogModel) []ModelCatalogModel {
	models := make([]ModelCatalogModel, 0, len(source)+1)
	models = append(models, ModelCatalogModel{
		ID:            "auto",
		Name:          "Automatic",
		SelectionMode: "automatic",
		Capabilities:  unknownModelCatalogCapabilities(),
	})
	seen := map[string]struct{}{"auto": {}}
	for _, sourceModel := range source {
		id := strings.TrimSpace(sourceModel.ID)
		if id == "" || strings.EqualFold(id, "auto") {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(sourceModel.Name)
		if name == "" {
			name = id
		}
		selectionMode := "explicit"
		if sourceModel.SelectionMode == "automatic" {
			selectionMode = "automatic"
		}
		models = append(models, ModelCatalogModel{
			ID:            id,
			Name:          name,
			SelectionMode: selectionMode,
			Capabilities:  sanitizeModelCatalogCapabilities(sourceModel.Capabilities),
		})
	}
	return models
}

func unknownModelCatalogCapabilities() map[string]string {
	return map[string]string{
		"completion": "unknown",
		"tools":      "unknown",
		"vision":     "unknown",
		"reasoning":  "unknown",
	}
}

func sanitizeModelCatalogCapabilities(source map[string]string) map[string]string {
	capabilities := unknownModelCatalogCapabilities()
	for _, name := range []string{"completion", "tools", "vision", "reasoning"} {
		switch source[name] {
		case "supported", "unsupported", "unknown":
			capabilities[name] = source[name]
		}
	}
	return capabilities
}

func modelCatalogState(state string) string {
	switch state {
	case "ready", "refreshing", "pending_shrink", "degraded", "disabled":
		return state
	default:
		return "degraded"
	}
}

func modelCatalogOutcome(outcome string) string {
	switch outcome {
	case "startup", "unchanged", "expanded", "metadata_updated", "pending_shrink", "shrink_confirmed", "skipped_busy", "failed", "cancelled":
		return outcome
	default:
		return "failed"
	}
}

func clampModelCatalogInterval(seconds int64) int64 {
	if seconds < 0 {
		return 0
	}
	if seconds > 86400 {
		return 86400
	}
	return seconds
}

func normalizeModelCatalogTime(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339)
}

func sanitizeModelCatalogAction(result ModelCatalogActionResult) ModelCatalogActionResult {
	if result.Code == "" {
		return ModelCatalogActionResult{
			Outcome: modelCatalogOutcome(result.Outcome),
			Message: "Model catalog refresh completed.",
		}
	}

	result.Outcome = ""
	result.RetryAfterSeconds = boundedModelCatalogRetryAfter(result.RetryAfterSeconds)
	switch result.Code {
	case "catalog_refresh_in_progress":
		result.Message = "A model catalog refresh is already in progress."
	case "catalog_refresh_cooldown":
		result.Message = "Model catalog refresh is temporarily rate limited."
	case "catalog_refresh_busy":
		result.Message = "No idle gateway worker is available for a model catalog refresh."
	case "catalog_refresh_failed":
		result.Message = "Model catalog refresh failed."
	case "catalog_refresh_unavailable":
		result.Message = "Model catalog refresh is unavailable."
	default:
		result.Code = "catalog_refresh_failed"
		result.Message = "Model catalog refresh failed."
	}
	return result
}

func boundedModelCatalogRetryAfter(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	if seconds > modelCatalogMaxRetryAfterSeconds {
		return modelCatalogMaxRetryAfterSeconds
	}
	return seconds
}

func modelCatalogRefreshStatus(code string) int {
	switch code {
	case "":
		return http.StatusOK
	case "catalog_refresh_in_progress":
		return http.StatusConflict
	case "catalog_refresh_cooldown":
		return http.StatusTooManyRequests
	case "catalog_refresh_busy", "catalog_refresh_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func rejectCrossOriginModelCatalogRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	requestOrigin, ok := requestModelCatalogOrigin(r)
	if !ok {
		return true
	}
	providedOrigin, ok := parseModelCatalogOrigin(origin)
	if !ok || providedOrigin != requestOrigin {
		return true
	}
	return false
}

// modelCatalogOrigin is the normalized comparison form of a serialized web
// origin. The port is always explicit so default-port equivalence is safe.
type modelCatalogOrigin struct {
	scheme string
	host   string
	port   string
}

func requestModelCatalogOrigin(r *http.Request) (modelCatalogOrigin, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host, port, ok := modelCatalogHostPort(r.Host, scheme)
	if !ok {
		return modelCatalogOrigin{}, false
	}
	return modelCatalogOrigin{scheme: scheme, host: host, port: port}, true
}

// parseModelCatalogOrigin accepts only a serialized web origin. In particular,
// it rejects a bare trailing slash because serialized Origin header values have
// no path, along with userinfo, query, fragment, and opaque URL forms.
func parseModelCatalogOrigin(value string) (modelCatalogOrigin, bool) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return modelCatalogOrigin{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return modelCatalogOrigin{}, false
	}
	host, port, ok := modelCatalogHostPort(parsed.Host, scheme)
	if !ok {
		return modelCatalogOrigin{}, false
	}
	return modelCatalogOrigin{scheme: scheme, host: host, port: port}, true
}

func modelCatalogHostPort(hostport, scheme string) (host, port string, ok bool) {
	parsed, err := url.Parse("//" + hostport)
	if err != nil || parsed.Host != hostport || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", false
	}
	host = strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", "", false
	}
	port = parsed.Port()
	if modelCatalogHasExplicitPort(hostport) && port == "" {
		return "", "", false
	}
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return host, port, true
}

func modelCatalogHasExplicitPort(hostport string) bool {
	if strings.HasPrefix(hostport, "[") {
		closingBracket := strings.LastIndex(hostport, "]")
		return closingBracket >= 0 && len(hostport) > closingBracket+1 && hostport[closingBracket+1] == ':'
	}
	return strings.Count(hostport, ":") == 1
}

func writeModelCatalogJSON(w http.ResponseWriter, status int, value any, h *handler) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.deps.Logger.Warn("admin: model catalog encode failed", "err", err)
	}
}
