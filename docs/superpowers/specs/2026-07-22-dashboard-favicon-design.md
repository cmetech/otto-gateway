# Dashboard Favicon Design

**Date:** 2026-07-22

## Goal

Use the Gateway glyph already shipped for the desktop tray as the favicon for
the browser-based admin UI. The favicon must appear on the Dashboard, About,
and Docs pages without adding a new runtime dependency or changing the tray
asset behavior.

## Selected approach

Copy `cmd/otto-tray/icon/gateway.ico` into the admin static asset tree as
`internal/admin/static/favicon.ico`. The ICO is the preferred source because it
already contains multiple browser-relevant sizes and uses a fixed-color Gateway
glyph that remains visible in both light and dark browser chrome. The macOS
`gateway.png` is a monochrome template image intended for operating-system
recoloring and may disappear against dark browser tabs.

Add the following favicon declaration to the shared admin base template:

```html
<link rel="icon" href="/admin/static/favicon.ico" sizes="any">
```

The shared template covers all three admin pages, so no page-specific markup is
needed.

## Asset delivery

The favicon will use the existing embedded admin-static pipeline and
`/admin/static/*` route. No new handler, route, filesystem lookup, package
dependency, or installation step is introduced. The favicon is compiled into
the gateway binary alongside the existing CSS and JavaScript assets.

The tray and admin copies intentionally contain identical bytes. Keeping the
admin copy within the existing static tree avoids coupling the `internal/admin`
package to the platform-specific tray command package and its build-tagged icon
embeds.

## Error behavior

Missing or invalid static assets remain build/test failures rather than runtime
fallbacks. Existing static-route behavior continues to handle unknown paths.

## Verification

Automated tests will verify that:

1. rendered admin HTML contains the favicon declaration;
2. `GET /admin/static/favicon.ico` succeeds through the embedded static route;
3. the response has an image-compatible content type and non-empty body; and
4. the admin favicon bytes match the existing tray `gateway.ico` source.

The normal Go test, formatting, lint, and build gates will remain unchanged.

## Out of scope

- Dynamic favicon changes for running, warning, or error states.
- Apple touch icons, web manifests, or installable-PWA metadata.
- Changing or regenerating the tray icons.
- Refactoring tray assets into a shared Go package.
