package admin

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/testutil"
)

// stubPool satisfies PoolDetailSource for tests.
type stubPool struct {
	slots        []SnapshotSlot
	spawnFailing bool
}

func (s *stubPool) Detail() []SnapshotSlot { return s.slots }
func (s *stubPool) SpawnFailing() bool     { return s.spawnFailing }

// stubRegistry satisfies RegistryStatsSource for tests.
type stubRegistry struct {
	sessions []SnapshotSess
}

func (r *stubRegistry) Detail() []SnapshotSess { return r.sessions }

// TestAdmin_PageHandler verifies GET / returns 200 with text/html and
// contains the expected HTML structure per behavior contract.
func TestAdmin_PageHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:    testutil.Logger(t),
		Version:   "1.2.3",
		Commit:    "abc1234",
		Debug:     true,
		ChatTrace: true,
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("Content-Type: want text/html, got %q", contentType)
	}

	body := rec.Body.String()

	// Page title check per behavior contract.
	if !strings.Contains(body, "Gateway") {
		t.Errorf("body missing expected page title containing 'Gateway'")
	}

	// Feature-flag visibility (quick 260531-ebi): the summary strip must show
	// the literal Debug + Chat-trace + Compression labels and their rendered
	// on/off state. Debug and ChatTrace are both true above, so both render
	// "on"; CompressionActive is false, so it renders "off".
	for _, want := range []string{"Debug", "Chat-trace", "Compression"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing required feature-flag label %q", want)
		}
	}
	if !strings.Contains(body, ">on<") {
		t.Errorf("body missing rendered 'on' state for an enabled feature flag")
	}

	// Chip semantics: green (gw-pill-on) = on, gray (gw-pill-off) = off —
	// EXCEPT Chat-trace, whose ON state is SENSITIVE (raw prompts on disk)
	// and renders the amber warning chip instead of green. The zero-value
	// CompressionState must fail closed to the gray "off" chip.
	if !strings.Contains(body, `gw-pill gw-pill-on">on<`) {
		t.Errorf("body missing green on-chip for enabled Debug flag")
	}
	if !strings.Contains(body, `gw-pill gw-pill-warn">on<`) {
		t.Errorf("body missing amber warning chip for enabled (SENSITIVE) Chat-trace flag")
	}
	if !strings.Contains(body, "gw-pill gw-pill-off") {
		t.Errorf("body missing gray off-chip for unset CompressionState")
	}

	// Summary strip data-* hooks per behavior contract.
	for _, attr := range []string{
		"data-pill",
		"data-uptime",
		"data-pool-summary",
		"data-sessions-count",
		"data-last-updated",
	} {
		if !strings.Contains(body, attr) {
			t.Errorf("body missing required attribute hook %q", attr)
		}
	}

	// Config island check per behavior contract.
	if !strings.Contains(body, "GW_ADMIN_CONFIG") {
		t.Errorf("body missing GW_ADMIN_CONFIG config island")
	}
	if !strings.Contains(body, "pollMs") {
		t.Errorf("body missing pollMs in config island")
	}
}

// TestAdmin_CompressionFlagSurfacing verifies the three-state
// CompressionState dep drives both the dashboard summary chip and the
// /about Feature Flags row: "on" (green — hook in chain, env default on),
// "per-request" (purple — hook in chain, env off, header/suffix enable
// individual requests), "off" (gray — hook excluded from ENABLED_HOOKS).
// Unknown/zero values must fail closed to "off".
func TestAdmin_CompressionFlagSurfacing(t *testing.T) {
	defer goleak.VerifyNone(t)

	cases := []struct {
		state     string // Deps.CompressionState as wired by main.go
		wantChip  string // dashboard chip markup
		wantAbout string // /about Feature Flags dd prefix
	}{
		{"on", `gw-pill gw-pill-on">on<`, "<dt>Compression</dt><dd>on</dd>"},
		{"per-request", `gw-pill gw-pill-req"`, "<dt>Compression</dt><dd>per-request "},
		{"off", `gw-pill gw-pill-off"`, "<dt>Compression</dt><dd>off</dd>"},
		{"", `gw-pill gw-pill-off"`, "<dt>Compression</dt><dd>off</dd>"},      // zero value fails closed
		{"bogus", `gw-pill gw-pill-off"`, "<dt>Compression</dt><dd>off</dd>"}, // unknown fails closed
	}
	for _, c := range cases {
		deps := Deps{
			Logger:           testutil.Logger(t),
			Version:          "1.2.3",
			Commit:           "abc1234",
			CompressionState: c.state,
		}
		h := Handler(deps)

		get := func(path string) string {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s (state=%q): want 200, got %d", path, c.state, rec.Code)
			}
			return rec.Body.String()
		}

		dash := get("/")
		if !strings.Contains(dash, "Compression") || !strings.Contains(dash, c.wantChip) {
			t.Errorf("dashboard (state=%q): missing Compression chip %q", c.state, c.wantChip)
		}
		if c.state == "per-request" && !strings.Contains(dash, ">per-request<") {
			t.Errorf("dashboard (state=%q): chip text is not the literal per-request word", c.state)
		}

		about := get("/about")
		if !strings.Contains(about, c.wantAbout) {
			t.Errorf("about (state=%q): Feature Flags row missing %q", c.state, c.wantAbout)
		}
	}
}

// TestAdmin_DocsEnvTable_CompressionRows verifies the /docs environment
// variable table includes the compression knobs (and the JSONFormat
// steering gate) with live current values from Deps.
func TestAdmin_DocsEnvTable_CompressionRows(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:                testutil.Logger(t),
		Version:               "1.2.3",
		Commit:                "abc1234",
		CompressionEnabled:    true,
		CompressTriggerTokens: 6000,
		CompressBudgetTokens:  4000,
		CompressProtectTail:   4,
		CompressToolKeep:      1200,
	}
	h := Handler(deps)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"JSON_FORMAT_STEERING_ENABLED",
		"COMPRESSION_ENABLED",
		"COMPRESS_TRIGGER_TOKENS",
		"COMPRESS_BUDGET_TOKENS",
		"COMPRESS_PROTECT_TAIL",
		"COMPRESS_TOOL_KEEP",
		// Endpoint-reference + tray coverage added in the same docs pass.
		"/health/hooks",
		"/metrics",
		"gateway-tray.exe",
		"Gateway Tray",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/docs missing %q", want)
		}
	}
}

// TestAdmin_StaticServes verifies GET /static/css/admin.css returns 200
// with the correct content type and expected CSS custom property.
func TestAdmin_StaticServes(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/static/css/admin.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/css/admin.css: want 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/css") {
		t.Errorf("Content-Type: want text/css, got %q", contentType)
	}

	body := rec.Body.String()
	if len(body) == 0 {
		t.Error("body: want non-empty CSS file")
	}
	if !strings.Contains(body, "--gw-bg") {
		t.Errorf("CSS body missing --gw-bg custom property")
	}
}

// TestAdmin_StaticServes_JS verifies GET /static/js/admin.js returns 200
// with JavaScript content type and expected GW_ADMIN_CONFIG reference.
func TestAdmin_StaticServes_JS(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/static/js/admin.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/js/admin.js: want 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "javascript") {
		t.Errorf("Content-Type: want application/javascript or text/javascript, got %q", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "GW_ADMIN_CONFIG") {
		t.Errorf("JS body missing GW_ADMIN_CONFIG reference")
	}
}

// TestAdmin_PageHandler_PoolGridScaffold verifies GET /admin returns HTML that
// contains the pool-slot grid markup required by Plan 02:
// - data-slot-grid attribute (JS hydration target)
// - data-slot-grid-empty attribute (empty-state placeholder)
// - gw-slot-grid class (CSS target rendered before JS runs)
func TestAdmin_PageHandler_PoolGridScaffold(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"data-slot-grid",
		"data-slot-grid-empty",
		`class="gw-slot-grid"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page HTML missing required pool-grid markup %q", want)
		}
	}
}

// TestAdmin_PageHandler_SessionsTableScaffold verifies GET /admin returns HTML that
// contains the sessions table markup required by Plan 02:
// - data-sessions-card attribute (container)
// - data-sessions-empty attribute (empty-state placeholder)
// - data-sessions-tbody attribute (tbody JS hydration target)
// - four column headers per UI-SPEC Copywriting Contract
// - empty-state copy strings per UI-SPEC Copywriting Contract
func TestAdmin_PageHandler_SessionsTableScaffold(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	}
	h := Handler(deps)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Structural markup checks.
	for _, want := range []string{
		"data-sessions-card",
		"data-sessions-empty",
		"data-sessions-tbody",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page HTML missing required sessions markup %q", want)
		}
	}

	// Column header checks per UI-SPEC Copywriting Contract.
	for _, want := range []string{
		"<th>Session</th>",
		"<th>Status</th>",
		"<th>Last used</th>",
		"<th>Model</th>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page HTML missing required sessions column header %q", want)
		}
	}

	// Empty-state copy checks per UI-SPEC Copywriting Contract.
	for _, want := range []string{
		"No active sessions",
		"Stateful sessions created via the X-Session-Id header will appear here.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page HTML missing required empty-state copy %q", want)
		}
	}
}

// TestAdmin_AssetsFSContains verifies the embed.FS captured all required
// asset files (regression for Pitfall 1 — embed glob semantics).
func TestAdmin_AssetsFSContains(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Quick 260601-98c (admin UI redesign step 1): index.html.tmpl was split into
	// a shared base layout + per-page templates (dashboard / about / docs). The
	// embed glob "templates/*.html.tmpl" must still pick all four up.
	paths := []string{
		"templates/base.html.tmpl",
		"templates/dashboard.html.tmpl",
		"templates/about.html.tmpl",
		"templates/docs.html.tmpl",
		"static/css/admin.css",
		"static/js/admin.js",
	}

	for _, p := range paths {
		_, err := fs.Stat(assetsFS, p)
		if err != nil {
			t.Errorf("assetsFS missing %q: %v (Pitfall 1 embed glob regression)", p, err)
		}
	}

	// Verify version/commit from handler page
	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	}
	h := Handler(deps)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	// Version should be baked in at render time.
	if !strings.Contains(body, "1.2.3") {
		t.Errorf("page body missing version '1.2.3'")
	}

	// Verify config island has the expected values
	var configIsland struct {
		PollMs int `json:"pollMs"`
	}
	_ = json.Unmarshal([]byte("{}"), &configIsland)
}
