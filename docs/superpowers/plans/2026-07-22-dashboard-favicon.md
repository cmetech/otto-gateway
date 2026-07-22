# Dashboard Favicon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Display the existing colored Gateway tray glyph as the favicon on every admin UI page.

**Architecture:** Copy the existing multi-resolution tray ICO into `internal/admin/static/`, where the admin package's current `go:embed` directive and `/admin/static/*` handler already serve browser assets. Reference that asset once from the shared base template so Dashboard, About, and Docs inherit it without new handlers or package dependencies.

**Tech Stack:** Go `embed.FS`, `net/http`, `html/template`, Go `testing`, ICO browser asset.

## Global Constraints

- The admin favicon bytes must match `cmd/otto-tray/icon/gateway.ico` exactly.
- The browser asset must live at `/admin/static/favicon.ico` and use the existing embedded static route.
- Dashboard, About, and Docs must receive the favicon through `templates/base.html.tmpl`; do not duplicate page markup.
- Add no runtime dependency, filesystem lookup, handler, route, or installation step.
- Do not change or regenerate any tray icon.
- Do not add dynamic status favicons, Apple touch icons, a web manifest, PWA metadata, or a shared tray/admin Go package.

---

### Task 1: Embed and reference the Gateway favicon

**Files:**
- Create: `internal/admin/static/favicon.ico`
- Modify: `internal/admin/templates/base.html.tmpl:3-9`
- Test: `internal/admin/handlers_test.go`

**Interfaces:**
- Consumes: the existing tray asset `cmd/otto-tray/icon/gateway.ico`, the existing `//go:embed templates/*.html.tmpl static` declaration, and the existing `GET /static/*` handler.
- Produces: an embedded browser asset at `/admin/static/favicon.ico` and a shared `<link rel="icon">` declaration on all admin pages.

- [ ] **Step 1: Write the failing favicon integration test**

Add `bytes`, `os`, and `path/filepath` to the imports in `internal/admin/handlers_test.go`, then add this test:

```go
func TestAdmin_FaviconUsesGatewayTrayIcon(t *testing.T) {
	defer goleak.VerifyNone(t)

	h := Handler(Deps{Logger: testutil.Logger(t)})
	const faviconLink = `<link rel="icon" href="/admin/static/favicon.ico" sizes="any">`

	for _, page := range []string{"/", "/about", "/docs"} {
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
```

- [ ] **Step 2: Run the focused test and verify the red state**

Run:

```bash
go test ./internal/admin -run TestAdmin_FaviconUsesGatewayTrayIcon -count=1
```

Expected: FAIL because all rendered pages lack the favicon declaration and `/static/favicon.ico` does not exist.

- [ ] **Step 3: Add the exact tray icon to the embedded admin static tree**

Copy the file without transforming it:

```bash
cp cmd/otto-tray/icon/gateway.ico internal/admin/static/favicon.ico
cmp cmd/otto-tray/icon/gateway.ico internal/admin/static/favicon.ico
```

Expected: `cmp` exits 0 with no output.

- [ ] **Step 4: Reference the favicon from the shared base template**

In `internal/admin/templates/base.html.tmpl`, add the favicon link after the viewport metadata and before the page title:

```html
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <link rel="icon" href="/admin/static/favicon.ico" sizes="any">
  <title>Gateway — Admin</title>
```

- [ ] **Step 5: Run the focused test and verify the green state**

Run:

```bash
go test ./internal/admin -run TestAdmin_FaviconUsesGatewayTrayIcon -count=1
```

Expected: PASS. This proves the shared template covers all three pages, the embedded route serves a non-empty image, and its bytes match the tray icon.

- [ ] **Step 6: Run the complete verification gate**

Run:

```bash
gofumpt -w internal/admin/handlers_test.go
go test ./... -count=1
go vet ./...
go build ./cmd/otto-gateway
golangci-lint run ./... --timeout=5m
git diff --check
```

Expected: every command exits 0 and `golangci-lint` reports `0 issues`.

- [ ] **Step 7: Commit the implementation**

```bash
git add internal/admin/handlers_test.go internal/admin/templates/base.html.tmpl internal/admin/static/favicon.ico
git commit -m "feat(admin): add Gateway favicon"
```

Expected: one implementation commit containing only the favicon asset, shared template declaration, and regression test.
