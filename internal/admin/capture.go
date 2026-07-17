package admin

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

// acpCaptureResponse is the GET /admin/api/acp-capture body (also returned by
// the POST after applying an action).
type acpCaptureResponse struct {
	Enabled            bool           `json:"enabled"`
	AllowRuntimeToggle bool           `json:"allowRuntimeToggle"`
	Count              int            `json:"count"`
	Size               int            `json:"size"`
	Frames             []CaptureFrame `json:"frames"`
}

// acpCaptureActionRequest is the POST body: {"action":"enable"|"disable"|"clear"}.
type acpCaptureActionRequest struct {
	Action string `json:"action"`
}

// acpCaptureHandler serves the capture ring + runtime state as JSON. When no
// source is wired it reports enabled:false / allowRuntimeToggle:false with an
// empty frames array (200, not 404, so a harness can tell "off" from "missing").
func (h *handler) acpCaptureHandler(w http.ResponseWriter, _ *http.Request) {
	resp := acpCaptureResponse{Frames: []CaptureFrame{}}
	if src := h.deps.AcpCapture; src != nil {
		resp.Enabled = src.Enabled()
		resp.AllowRuntimeToggle = src.AllowRuntimeToggle()
		resp.Count = src.Count()
		resp.Size = src.Size()
		if fr := src.Snapshot(); fr != nil {
			resp.Frames = fr
		}
	}
	writeJSONCapture(w, http.StatusOK, resp, h)
}

// acpCapturePostHandler mutates capture state: enable | disable | clear. Guarded
// by the opt-in ACP_CAPTURE_RUNTIME flag (403 when not allowed) — this is the
// admin surface's only write route.
func (h *handler) acpCapturePostHandler(w http.ResponseWriter, req *http.Request) {
	src := h.deps.AcpCapture
	if src == nil {
		writeJSONErr(w, http.StatusForbidden, "capture not available on this gateway")
		return
	}
	if !src.AllowRuntimeToggle() {
		writeJSONErr(w, http.StatusForbidden, "runtime toggle disabled; start the gateway with ACP_CAPTURE_RUNTIME=true")
		return
	}
	mt, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeJSONErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var body acpCaptureActionRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch body.Action {
	case "enable":
		src.Enable()
	case "disable":
		src.Disable()
	case "clear":
		src.Clear()
	default:
		writeJSONErr(w, http.StatusBadRequest, "unknown action; want enable|disable|clear")
		return
	}
	// Echo the updated status so the UI can refresh from one round-trip.
	h.acpCaptureHandler(w, req)
}

func writeJSONCapture(w http.ResponseWriter, status int, v any, h *handler) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.deps.Logger.Warn("admin: acp-capture encode failed", "err", err)
	}
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
