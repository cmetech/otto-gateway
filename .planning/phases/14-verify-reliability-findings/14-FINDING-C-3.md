---
finding: C-3
severity: M
rel_id: REL-CFG-03
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding C-3: EMBEDDING_MODEL_DEFAULT is documented but never read

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §5 (Config, secrets, and startup):

> **[Medium] C-3: EMBEDDING_MODEL_DEFAULT is documented as a backward-compat env var but is never read anywhere**
>
> **Files:** CLAUDE.md env-var contract; `internal/server/health.go:22-23, 44-47` (`EmbeddingStats` envelope ships) — repo-wide grep finds no code reading the variable and no embeddings endpoint at all.
>
> **Failure scenario:** A deployment swapping the Node binary for this one keeps `EMBEDDING_MODEL_DEFAULT=...` and it is silently ignored — and any LangFlow flow calling `/api/embeddings` 404s.

## Current-source check

**CLAUDE.md env-var contract:** The variable `EMBEDDING_MODEL_DEFAULT` is listed in CLAUDE.md's Constraints section under the backward-compat env var list: `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `POOL_SIZE`, `SESSION_TTL_MS`, `AUTH_TOKEN`, `ALLOWED_IPS`, `DEBUG`, `EMBEDDING_MODEL_DEFAULT`, etc.

**Repo-wide grep for `EMBEDDING_MODEL_DEFAULT` in `internal/` and `cmd/`:**

```
grep -rn "EMBEDDING_MODEL_DEFAULT" internal/   →  ZERO results (production code only)
grep -rn "EMBEDDING_MODEL_DEFAULT" cmd/        →  ZERO results
```

(The test file `internal/config/regression_rel_cfg_03_test.go` added in this plan is the only occurrence in `internal/`.)

**`internal/server/health.go:22-23, 44-47`:** The `EmbeddingStats` struct exists and is included in the `HealthReport` JSON response, but its `ModelsLoaded` field is always zero — nothing populates it because no embedding model loading code exists:

```go
// EmbeddingStats reports loaded embedding model state.
// Populated by Phase 7; zero values correct for Phase 1.
type EmbeddingStats struct {
    ModelsLoaded int `json:"models_loaded"`
}
```

Phase 7 (embeddings) was cut from the milestone scope (see CLAUDE.md and PROJECT.md Out of Scope section) and was never implemented. The struct is a placeholder.

**Config struct:** `config.Load()` (config.go:282) does not call `getEnvStr("EMBEDDING_MODEL_DEFAULT", ...)` or any equivalent. The var is not parsed, not stored, not logged.

## Evidence

This is a Medium finding per D-02 (code-walk + t.Skip'd regression test).

**Go regression test:** `internal/config/regression_rel_cfg_03_test.go`
- Function: `TestRegression_REL_CFG_03_EmbeddingModelDefaultUnimplemented`
- Pre-fix observable: after `t.Setenv("EMBEDDING_MODEL_DEFAULT", "qwen3-embed")` and `config.Load()`, the slog capture buffer contains NO Warn record mentioning `EMBEDDING_MODEL_DEFAULT`
- Post-fix: a Warn record is emitted by `config.Load()` or the boot sequence

**Current-source check confirms:** grep across all non-test Go files in `internal/` and `cmd/` returns zero results for `EMBEDDING_MODEL_DEFAULT`. The variable is never read. This is the most direct form of evidence for C-3.

**`internal/server/health.go` note:** The `EmbeddingStats` struct envelope (`health.go:44-47`) exists only as a placeholder with comment "Populated by Phase 7". Phase 7 was cut from scope. The struct was never wired to any actual embedding model source.

## Verdict

**confirmed** — `EMBEDDING_MODEL_DEFAULT` is documented in CLAUDE.md's backward-compat env var list but a repo-wide grep across all production source files in `internal/` and `cmd/` returns zero occurrences. The var is never parsed, never stored, never logged. An operator who sets this var receives no indication it is ignored. Phase 16 fix: add a `getEnvStr("EMBEDDING_MODEL_DEFAULT", "")` call in `config.Load()` and emit a `Warn("EMBEDDING_MODEL_DEFAULT set but embeddings are not implemented")` when the value is non-empty, so operators are informed without a boot failure (the var is a non-fatal docs/compat issue, not a security or correctness issue).
