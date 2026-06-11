# Phase 14 Ledger Fragment 02 — HTTP Surface Findings (Plan 14-02)

| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |
|---|---|---|---|---|---|---|
| H-1 | H | REL-HTTP-01 | confirmed | internal/server/server.go:346-383 + internal/admin/sse.go:167-203 | 14-FINDING-H-1.md | 15 |
| H-2 | H | REL-HTTP-02 | confirmed | internal/adapter/openai/sse.go:460-462 + :482-484 | 14-FINDING-H-2.md | 15 |
| H-3 | H | REL-HTTP-03 | confirmed | internal/adapter/openai/sse.go:543-557 + internal/adapter/ollama/ndjson.go:541-549 | 14-FINDING-H-3.md | 15 |
| H-4 | M | REL-HTTP-04 | confirmed | internal/server/server.go:347-360 | 14-FINDING-H-4.md | 16 |
| H-5 | M | REL-HTTP-05 | confirmed | internal/admin/tail.go:393-427 (cap at :402) | 14-FINDING-H-5.md | 16 |
