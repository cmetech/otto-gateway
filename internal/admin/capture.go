package admin

import (
	"encoding/json"
	"net/http"
)

// acpCaptureResponse is the GET /admin/api/acp-capture body.
type acpCaptureResponse struct {
	Enabled bool           `json:"enabled"`
	Frames  []CaptureFrame `json:"frames"`
}

// acpCaptureHandler serves the raw-frame capture ring as JSON. When no source is
// wired (ACP_CAPTURE off), it reports enabled:false with an empty frames array —
// a 200 (not 404) so a harness can distinguish "off" from "route missing".
func (h *handler) acpCaptureHandler(w http.ResponseWriter, _ *http.Request) {
	resp := acpCaptureResponse{Frames: []CaptureFrame{}}
	if h.deps.AcpCapture != nil {
		resp.Enabled = true
		if fr := h.deps.AcpCapture.Snapshot(); fr != nil {
			resp.Frames = fr
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.deps.Logger.Warn("admin: acp-capture encode failed", "err", err)
	}
}
