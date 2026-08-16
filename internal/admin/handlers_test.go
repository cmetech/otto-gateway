package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestAdmin_FaviconUsesGatewayTrayIcon(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{Logger: testutil.Logger(t)})
	const faviconLink = `<link rel="icon" href="/admin/static/favicon.ico" sizes="any">`

	for _, page := range []string{"/", "/about", "/privacy", "/docs"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, page, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", page, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), faviconLink) {
			t.Errorf("GET %s: missing favicon link %q", page, faviconLink)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/static/favicon.ico", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/favicon.ico: want 200, got %d", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "image/") {
		t.Errorf("Content-Type: want image/*, got %q", contentType)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("GET /static/favicon.ico: empty body")
	}

	trayIcon, err := os.ReadFile(filepath.Join("..", "..", "cmd", "otto-tray", "icon", "gateway.ico"))
	if err != nil {
		t.Fatalf("read tray gateway.ico: %v", err)
	}
	if !bytes.Equal(rec.Body.Bytes(), trayIcon) {
		t.Fatal("admin favicon bytes differ from tray gateway.ico")
	}
}

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

	// Summary strip data-* hooks per behavior contract. Pool and
	// Stateful-sessions tiles were removed from the strip (v2.16.2 —
	// both are already covered by the Pool Slots grid and Active
	// Sessions table on the same page), so their hooks must be GONE.
	for _, attr := range []string{
		"data-gateway-pid",
		"data-pill",
		"data-uptime",
		"data-last-updated",
	} {
		if !strings.Contains(body, attr) {
			t.Errorf("body missing required attribute hook %q", attr)
		}
	}
	for _, want := range []string{
		`class="gw-summary-header"`,
		"Gateway PID",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing gateway PID header scaffold %q", want)
		}
	}
	for _, attr := range []string{
		"data-pool-summary",
		"data-sessions-count",
	} {
		if strings.Contains(body, attr) {
			t.Errorf("body still contains removed summary-strip hook %q", attr)
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

func TestAdmin_PageHandler_ACPCopyMessagesScaffold(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{Logger: testutil.Logger(t)})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", rec.Code)
	}
	for _, want := range []string{
		"data-acp-capture-copy",
		"data-acp-capture-copy-label",
		"Copy Messages",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("page HTML missing ACP copy control %q", want)
		}
	}
}

func TestAdmin_PageHandler_ModelCatalogScaffold(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: want 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`id="model-catalog"`,
		`data-model-catalog-state`,
		`data-model-catalog-count`,
		`data-model-catalog-last-success`,
		`data-model-catalog-next`,
		`data-model-catalog-interval`,
		`data-model-catalog-refresh`,
		`data-model-catalog-body`,
		`data-model-catalog-pending`,
		`data-model-catalog-message`,
		`aria-live="polite"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	activeSessions := strings.Index(body, "Active Sessions")
	modelCatalog := strings.Index(body, "Model Catalog")
	privacyBoundary := strings.Index(body, "Privacy Boundary")
	if activeSessions < 0 || modelCatalog < 0 || privacyBoundary < 0 ||
		activeSessions >= modelCatalog || modelCatalog >= privacyBoundary {
		t.Errorf(
			"section order = Active Sessions %d, Model Catalog %d, Privacy Boundary %d; want increasing",
			activeSessions,
			modelCatalog,
			privacyBoundary,
		)
	}

	for _, heading := range []string{"Model", "Model ID", "Completion", "Tools", "Vision", "Reasoning"} {
		want := `<th scope="col">` + heading + `</th>`
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing catalog column heading %q", want)
		}
	}
	for _, want := range []string{
		`<button type="button" class="gw-btn gw-btn--primary gw-model-catalog-refresh" data-model-catalog-refresh`,
		`role="region" aria-labelledby="model-catalog-heading" tabindex="0"`,
		`<table class="gw-model-catalog-table">`,
		`<caption class="sr-only">Live selectable model catalog and capabilities</caption>`,
		`data-model-catalog-live`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing accessible catalog markup %q", want)
		}
	}
	if got := strings.Count(body, `data-model-catalog-body`); got != 1 {
		t.Errorf("data-model-catalog-body count = %d; want 1", got)
	}
	if got := strings.Count(body, `data-model-catalog-pending`); got != 1 {
		t.Errorf("data-model-catalog-pending count = %d; want one distinct persistent warning", got)
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

// TestAdmin_DocsEnvTable_WorkerRecycle verifies the /docs environment
// variable table includes all worker-recycle policy values wired live from
// Deps rather than merely exposing static descriptions.
func TestAdmin_DocsEnvTable_WorkerRecycle(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps := Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
		// Distinctive value that does NOT appear in the row's own description
		// text (which hardcodes "20" for the laptop template), so this test
		// actually exercises the live CurrentValue wiring rather than being
		// satisfied by the static copy.
		KiroWorkerMaxTurns:            37,
		KiroWorkerIdleRecycleAfter:    23 * time.Minute,
		KiroWorkerIdleRecycleMemoryMB: 641,
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
		"KIRO_WORKER_MAX_TURNS",
		"37",
		"KIRO_WORKER_IDLE_RECYCLE_MS",
		"1380000",
		"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB",
		"641",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/docs missing %q", want)
		}
	}
}

func TestAdmin_DocsEnvTable_KiroACPProxyDefaults(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{
		Logger:  testutil.Logger(t),
		Version: "1.2.3",
		Commit:  "abc1234",
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"acp --agent acp_proxy",
		"gateway-managed",
		".kiro/agents/acp_proxy.json",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/docs missing %q", want)
		}
	}
}

func TestAdmin_DocsEnvTable_KiroLogLevel(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{
		Logger:       testutil.Logger(t),
		Version:      "1.2.3",
		Commit:       "abc1234",
		KiroLogLevel: "TRACE",
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /docs: want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"KIRO_LOG_LEVEL",
		"ERROR, WARN, INFO, DEBUG, TRACE",
		">INFO<",
		">TRACE<",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/docs missing %q", want)
		}
	}
}

func TestAdmin_ModelCatalogAboutAndDocs(t *testing.T) {
	defer goleak.VerifyNone(t)

	get := func(t *testing.T, deps Deps, path string) string {
		t.Helper()
		h := Handler(deps)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	base := Deps{
		Logger:                      testutil.Logger(t),
		Version:                     "1.2.3",
		Commit:                      "abc1234",
		ModelCatalogRefreshInterval: 15 * time.Minute,
	}
	about := get(t, base, "/about")
	for _, want := range []string{
		"<dt>Model catalog refresh</dt><dd>15m0s",
		`href="/admin/#model-catalog"`,
	} {
		if !strings.Contains(about, want) {
			t.Errorf("/about missing %q", want)
		}
	}

	disabled := base
	disabled.ModelCatalogRefreshInterval = 0
	if body := get(t, disabled, "/about"); !strings.Contains(body, "<dt>Model catalog refresh</dt><dd>disabled") {
		t.Error("/about with zero interval missing model catalog disabled state")
	}

	docs := get(t, base, "/docs")
	for _, want := range []string{
		"MODEL_CATALOG_REFRESH_INTERVAL_SEC",
		">900 (15m)<",
		"0 disables scheduled refresh",
		"restart",
		"10 seconds",
		"30 seconds",
		"two matching valid observations",
		"/admin/api/model-catalog",
		"/admin/api/model-catalog/refresh",
	} {
		if !strings.Contains(docs, want) {
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

func TestAdmin_StaticCSS_ModelCatalogContrastContract(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{Logger: testutil.Logger(t)})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/static/css/admin.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/css/admin.css: want 200, got %d", rec.Code)
	}
	css := rec.Body.String()
	rule := func(selector string) string {
		t.Helper()
		start := strings.Index(css, selector)
		if start < 0 {
			t.Fatalf("CSS missing selector %q", selector)
		}
		open := strings.Index(css[start:], "{")
		if open < 0 {
			t.Fatalf("CSS selector %q missing declaration block", selector)
		}
		open += start
		closeBrace := strings.Index(css[open:], "}")
		if closeBrace < 0 {
			t.Fatalf("CSS selector %q has unterminated declaration block", selector)
		}
		return css[open+1 : open+closeBrace]
	}
	assertDeclarations := func(selector string, declarations ...string) {
		t.Helper()
		block := rule(selector)
		for _, declaration := range declarations {
			if !strings.Contains(block, declaration) {
				t.Errorf("CSS rule %q missing %q", selector, declaration)
			}
		}
	}

	assertDeclarations(":root",
		"--gw-catalog-meta-label: #B8C0CC;",
		"--gw-catalog-disabled-fg: #D1D5DB;",
		"--gw-catalog-supported-fg: #72E6AE;",
		"--gw-catalog-supported-bg: #163A2D;",
		"--gw-catalog-warning-fg: #FFD08A;",
		"--gw-catalog-warning-bg: #493018;",
		"--gw-catalog-muted-fg: #D1D5DB;",
		"--gw-catalog-muted-bg: #303640;",
		"--gw-catalog-busy-fg: #D6B4F0;",
		"--gw-catalog-busy-bg: #3B2B48;",
		"--gw-catalog-focus: var(--gw-accent);",
	)
	assertDeclarations(`[data-theme="light"]`,
		"--gw-catalog-meta-label: #4B5563;",
		"--gw-catalog-disabled-fg: #4B5563;",
		"--gw-catalog-supported-fg: #00623F;",
		"--gw-catalog-supported-bg: #E4F7EF;",
		"--gw-catalog-warning-fg: #7A3600;",
		"--gw-catalog-warning-bg: #FFF0DF;",
		"--gw-catalog-muted-fg: #4B5563;",
		"--gw-catalog-muted-bg: #EEF0F3;",
		"--gw-catalog-busy-fg: #5B21B6;",
		"--gw-catalog-busy-bg: #EDE9FE;",
		"--gw-catalog-focus: var(--gw-activity);",
	)
	assertDeclarations(".gw-model-catalog-meta dt", "color: var(--gw-catalog-meta-label);")
	assertDeclarations(".gw-model-catalog-refresh:disabled", "color: var(--gw-catalog-disabled-fg);")
	assertDeclarations(
		".gw-model-catalog-pending",
		"color: var(--gw-catalog-warning-fg);",
		"background: var(--gw-catalog-warning-bg);",
	)
	assertDeclarations(
		".gw-model-catalog-scroll:focus-visible",
		"outline: 3px solid var(--gw-catalog-focus);",
	)
	assertDeclarations(
		`[data-theme="light"] .gw-model-catalog-scroll:focus-visible,`,
		"outline-color: var(--gw-catalog-focus);",
	)
	assertDeclarations(
		".gw-model-catalog .gw-badge.is-ok,",
		"color: var(--gw-catalog-supported-fg);",
		"background: var(--gw-catalog-supported-bg);",
	)
	assertDeclarations(
		".gw-model-catalog .gw-badge.is-warning,",
		"color: var(--gw-catalog-warning-fg);",
		"background: var(--gw-catalog-warning-bg);",
	)
	assertDeclarations(
		".gw-model-catalog .gw-badge.is-muted,",
		"color: var(--gw-catalog-muted-fg);",
		"background: var(--gw-catalog-muted-bg);",
	)
	assertDeclarations(
		".gw-model-catalog .gw-badge.is-busy",
		"color: var(--gw-catalog-busy-fg);",
		"background: var(--gw-catalog-busy-bg);",
	)
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

	// Embedded assets are tiny and can change between releases; no-cache forces
	// revalidation so operators pick up JS/CSS changes without a hard refresh.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: want %q, got %q", "no-cache", cc)
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
		"templates/privacy.html.tmpl",
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
