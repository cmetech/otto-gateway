// Package admin provides the admin observability UI handler for Gateway.
// It exposes GET /admin (HTML page), GET /admin/api/snapshot (JSON aggregator),
// and GET /admin/static/* (embedded CSS/JS assets) — all auth-exempt per D-01.
package admin

import (
	"embed"
	"html/template"
	"io/fs"
)

// assetsFS embeds the templates and static assets at compile time.
// The bare directory name "static" (not "static/*") recursively embeds
// the entire subtree — Pitfall 1: using "static/*" would only match
// top-level files inside static/, not subdirectories.
//
//go:embed templates/*.html.tmpl static
var assetsFS embed.FS

// staticFS is rooted at the "static/" subtree of assetsFS so that
// http.FileServer(http.FS(staticFS)) serves /admin/static/css/admin.css
// → "css/admin.css" inside the embed rather than "static/css/admin.css".
var staticFS = mustSub(assetsFS, "static")

// Per-page templates are parsed ONCE at package init from the embedded FS.
// Each template is composed from the shared base layout + a per-page content
// template. Re-parsing per request burns CPU for zero benefit — the templates
// are baked into the binary and cannot change at runtime.
//
// Handlers execute these via ExecuteTemplate(buf, "base", data) so the
// {{define "base"}} layout drives rendering and pulls in the page's
// {{define "content"}} block.
var (
	dashboardTemplate = template.Must(template.ParseFS(assetsFS, "templates/base.html.tmpl", "templates/dashboard.html.tmpl"))
	aboutTemplate     = template.Must(template.ParseFS(assetsFS, "templates/base.html.tmpl", "templates/about.html.tmpl"))
	docsTemplate      = template.Must(template.ParseFS(assetsFS, "templates/base.html.tmpl", "templates/docs.html.tmpl"))
)

// mustSub returns fs.Sub(f, dir) or panics. This is init-time only;
// a broken embed = a broken binary, and panicking immediately is the
// correct behavior (fail fast, not silently incorrect serving).
func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("admin: embed sub " + dir + ": " + err.Error())
	}
	return sub
}
