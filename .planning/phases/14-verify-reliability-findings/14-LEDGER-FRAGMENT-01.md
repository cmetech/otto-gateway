# Phase 14 Verification Ledger Fragment 01: Pool / ACP (P-1..P-6)

**Plan:** 14-01
**Subsystem:** Pool / ACP
**Generated:** 2026-06-11

| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |
|---|---|---|---|---|---|---|
| P-1 | C | REL-POOL-01 | confirmed | `internal/pool/pool.go:534` (removeSlot on genuine spawn failure), `:491-505` (blocking acquire), `:297-306` (removeSlot impl) | 14-FINDING-P-1.md | 15 |
| P-2 | H | REL-POOL-02 | confirmed | `cmd/otto-gateway/main.go:131` (os.Exit skips defer), `:127` (deferred cleanup), `internal/server/server.go:377-381` (30s shutdown) | 14-FINDING-P-2.md | 15 |
| P-3 | H | REL-POOL-03 | confirmed | `internal/acp/client.go:868-870` (unconditional nil, ctx arm), `:894-896` (unconditional nil, frame arm), `internal/pool/pool.go:618-635` (concurrent slot release) | 14-FINDING-P-3.md | 15 |
| P-4 | M | REL-POOL-04 | confirmed | `internal/acp/stream.go:105-122` (push blocks readLoop), `internal/acp/client.go:1085` (push uses c.clientCtx), `:503-526` (pingLoop escalation) | 14-FINDING-P-4.md | 16 |
| P-5 | M | REL-POOL-05 | confirmed | `internal/session/registry.go:206` (write under r.mu), `internal/session/entry_acp.go:77-79` (write under e.Mu), `internal/session/registry.go:358` (read unguarded) | 14-FINDING-P-5.md | 16 |
| P-6 | M | REL-POOL-06 | confirmed | `internal/acp/pool_pgid_windows.go:15` (applyPgidAttr no-op), `:21` (killProcessGroup no-op returning nil) | 14-FINDING-P-6.md | 16 |
