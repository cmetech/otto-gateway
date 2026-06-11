# Phase 14 Verification Ledger Fragment 04 — Config / Hooks / Observability

**Plan:** 14-04
**Findings covered:** G-1, C-1, C-2, C-3, O-1
**Generated:** 2026-06-11

| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |
|---|---|---|---|---|---|---|
| G-1 | M | REL-HOOKS-01 | confirmed | internal/engine/collect.go:165,171 (return before PostHook loop at :187); internal/adapter/anthropic/collect.go:177,184 (return before RunPostHooks at :207) | 14-FINDING-G-1.md | 16 |
| C-1 | M | REL-CFG-01 | confirmed | internal/config/config.go:313,336,343,353,487 (no sign check); internal/pool/config.go:119-122; internal/session/config.go:99-108 | 14-FINDING-C-1.md | 16 |
| C-2 | M | REL-CFG-02 | confirmed | internal/config/config.go:295 (no sign check); internal/acp/client.go:59 (only defaults == 0); internal/acp/client.go:505 (time.NewTicker panics) | 14-FINDING-C-2.md | 16 |
| C-3 | M | REL-CFG-03 | confirmed | CLAUDE.md (documented); internal/ + cmd/ grep: zero occurrences (never read) | 14-FINDING-C-3.md | 16 |
| O-1 | M | REL-CFG-04 | confirmed | internal/pool/pool.go:490-505 (blocks with no Warn log; debugLog only after acquire at :506) | 14-FINDING-O-1.md | 16 |
