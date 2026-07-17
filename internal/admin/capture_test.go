package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/admin"
)

type fakeCaptureSource struct {
	frames   []admin.CaptureFrame
	enabled  bool
	allow    bool
	size     int
	enableN  int
	disableN int
	clearN   int
}

func (f *fakeCaptureSource) Snapshot() []admin.CaptureFrame { return f.frames }
func (f *fakeCaptureSource) Enabled() bool                  { return f.enabled }
func (f *fakeCaptureSource) AllowRuntimeToggle() bool       { return f.allow }
func (f *fakeCaptureSource) Count() int                     { return len(f.frames) }
func (f *fakeCaptureSource) Size() int                      { return f.size }
func (f *fakeCaptureSource) Enable()                        { f.enableN++; f.enabled = true }
func (f *fakeCaptureSource) Disable()                       { f.disableN++; f.enabled = false }
func (f *fakeCaptureSource) Clear()                         { f.clearN++; f.frames = nil }

func doCapturePost(t *testing.T, src admin.AcpCaptureSource, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequest(http.MethodPost, "/api/acp-capture", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	return rec
}

// TestAcpCapture_Enabled: with a source wired, the endpoint returns enabled:true
// and the frames as JSON.
func TestAcpCapture_Enabled(t *testing.T) {
	src := &fakeCaptureSource{enabled: true, frames: []admin.CaptureFrame{
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
	hOn := admin.Handler(admin.Deps{AcpCapture: &fakeCaptureSource{enabled: true}})
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

func TestAcpCapturePost_EnableWhenAllowed(t *testing.T) {
	src := &fakeCaptureSource{allow: true, size: 512}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if src.enableN != 1 || !src.enabled {
		t.Fatalf("enable not applied: enableN=%d enabled=%v", src.enableN, src.enabled)
	}
}

func TestAcpCapturePost_ForbiddenWhenToggleDisallowed(t *testing.T) {
	src := &fakeCaptureSource{allow: false}
	rec := doCapturePost(t, src, `{"action":"enable"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when allow=false, got %d", rec.Code)
	}
	if src.enableN != 0 {
		t.Fatalf("enable applied despite 403: enableN=%d", src.enableN)
	}
}

func TestAcpCapturePost_UnknownAction400(t *testing.T) {
	src := &fakeCaptureSource{allow: true}
	rec := doCapturePost(t, src, `{"action":"frobnicate"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown action, got %d", rec.Code)
	}
}

func TestAcpCaptureGet_ExtendedShape(t *testing.T) {
	src := &fakeCaptureSource{allow: true, enabled: true, size: 512, frames: []admin.CaptureFrame{{Seq: 1, Method: "session/update"}}}
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequest(http.MethodGet, "/api/acp-capture", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got struct {
		Enabled            bool                 `json:"enabled"`
		AllowRuntimeToggle bool                 `json:"allowRuntimeToggle"`
		Count              int                  `json:"count"`
		Size               int                  `json:"size"`
		Frames             []admin.CaptureFrame `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || !got.AllowRuntimeToggle || got.Count != 1 || got.Size != 512 || len(got.Frames) != 1 {
		t.Fatalf("extended GET shape wrong: %+v", got)
	}
}

// TestAcpCapturePost_RejectsNonJSONContentType: a POST without an
// application/json Content-Type must be rejected with 415 before the body is
// decoded or any mutator runs — this closes off a cross-origin "simple
// request" (text/plain) form-POST that would otherwise bypass CORS
// preflight and blind-toggle capture.
func TestAcpCapturePost_RejectsNonJSONContentType(t *testing.T) {
	src := &fakeCaptureSource{allow: true}
	h := admin.Handler(admin.Deps{AcpCapture: src})
	req := httptest.NewRequest(http.MethodPost, "/api/acp-capture", strings.NewReader(`{"action":"enable"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for non-JSON Content-Type, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if src.enableN != 0 {
		t.Fatalf("enable applied despite non-JSON Content-Type: enableN=%d", src.enableN)
	}
}
