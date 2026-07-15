package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"otto-gateway/internal/admin"
)

type fakeCaptureSource struct{ frames []admin.CaptureFrame }

func (f fakeCaptureSource) Snapshot() []admin.CaptureFrame { return f.frames }

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestAcpCapture_Enabled: with a source wired, the endpoint returns enabled:true
// and the frames as JSON.
func TestAcpCapture_Enabled(t *testing.T) {
	src := fakeCaptureSource{frames: []admin.CaptureFrame{
		{Seq: 1, Ts: time.Unix(1700000000, 0).UTC(), Method: "session/update", Params: `{"x":1}`, Bytes: 7},
	}}
	h := admin.Handler(admin.Deps{AcpCapture: src})

	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Enabled {
		t.Error("enabled = false, want true")
	}
	if len(body.Frames) != 1 || body.Frames[0].Method != "session/update" {
		t.Errorf("frames = %+v", body.Frames)
	}
}

// TestAcpCapture_Disabled: with no source, the endpoint reports enabled:false and
// an empty (non-nil) frames array.
func TestAcpCapture_Disabled(t *testing.T) {
	h := admin.Handler(admin.Deps{})
	rec := doGet(t, h, "/api/acp-capture")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Enabled bool                 `json:"enabled"`
		Frames  []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Enabled {
		t.Error("enabled = true, want false when no source wired")
	}
	if body.Frames == nil {
		t.Error("frames must be a non-nil (empty) array")
	}
}
