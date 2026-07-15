package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestAbout_AcpCaptureRow: the About page's Feature Flags card shows an
// "ACP capture" row reflecting whether ACP_CAPTURE is on — "on" (with the
// SENSITIVE badge) when a capture source is wired, "off" when it is nil. The
// wired-vs-nil AcpCaptureSource is the canonical enabled signal.
func TestAbout_AcpCaptureRow(t *testing.T) {
	const onRow = `<dt>ACP capture</dt><dd>on <span class="gw-badge is-warning">SENSITIVE</span></dd>`
	const offRow = `<dt>ACP capture</dt><dd>off</dd>`

	// Wired source → on.
	hOn := admin.Handler(admin.Deps{AcpCapture: fakeCaptureSource{}})
	recOn := doGet(t, hOn, "/about")
	if recOn.Code != http.StatusOK {
		t.Fatalf("GET /about (wired): status = %d, want 200", recOn.Code)
	}
	if body := recOn.Body.String(); !strings.Contains(body, onRow) {
		t.Errorf("About page missing ACP-capture ON row %q", onRow)
	}

	// No source → off.
	hOff := admin.Handler(admin.Deps{})
	recOff := doGet(t, hOff, "/about")
	if recOff.Code != http.StatusOK {
		t.Fatalf("GET /about (nil): status = %d, want 200", recOff.Code)
	}
	if body := recOff.Body.String(); !strings.Contains(body, offRow) {
		t.Errorf("About page missing ACP-capture OFF row %q", offRow)
	}
}

// TestDocs_AcpCaptureRows: the operator Docs page env-var table documents
// ACP_CAPTURE and ACP_CAPTURE_SIZE (added so the diagnostics flag is
// discoverable alongside the other env vars).
func TestDocs_AcpCaptureRows(t *testing.T) {
	h := admin.Handler(admin.Deps{})
	rec := doGet(t, h, "/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs: status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"ACP_CAPTURE", "ACP_CAPTURE_SIZE"} {
		if !strings.Contains(body, want) {
			t.Errorf("Docs page env table missing %q row", want)
		}
	}
}
