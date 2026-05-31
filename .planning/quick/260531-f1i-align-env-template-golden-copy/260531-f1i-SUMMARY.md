---
name: 260531-f1i-align-env-template-golden-copy
status: complete
quick_id: 260531-f1i
date: 2026-05-31
commit: 645a79a
---

# Align .env.otto-gw.example with OOB install defaults

## What changed
`scripts/.env.otto-gw.example` is now the **golden-copy** config reference: its
active-vs-commented key set matches byte-for-byte what `otto-gw init
--non-interactive` writes. Seven deltas applied:

- `AUTH_TOKEN` — active → **commented** (auth disabled OOB), `replace-me` placeholder kept.
- `KIRO_CMD` — added active empty `KIRO_CMD=` (matches OOB).
- `HTTP_ADDR` — uncommented to `127.0.0.1:18080`.
- `ENABLED_HOOKS` — uncommented to `RequestIDHook,AuthHook,PIIRedactionHook,LoggingHook`.
- `PII_REDACTION_ENABLED` — uncommented to `true`.
- `PII_REDACTION_MODE` — uncommented and `replace` → `hash`.
- `PII_HASH_KEY` — uncommented (placeholder; real installs mint a unique key).

Unchanged (already correct, commented): `PII_ENABLED_ENTITIES`, `CHAT_TRACE`,
`CHAT_TRACE_FILE`, `CHAT_TRACE_MAX_AGE_DAYS`, `DEBUG`, `ALLOWED_IPS`,
`AUTH_TRUST_XFF`, `POOL_SIZE`, `KIRO_ARGS`, `KIRO_CWD`.

## Verification
Generated a real OOB env via `init --non-interactive` to a temp dest and diffed
active keys against the template: **IDENTICAL**. `AUTH_TOKEN` and `CHAT_TRACE`
confirmed commented in both.

## Notes / follow-up
- Two sources of env shape now exist: the `init_cmd` heredoc (scripts/otto-gw)
  and this template. They are aligned today but can drift; a future refactor
  could make `init` render from the template to remove the duplication.
- This is the foundation for the **env-merge-on-upgrade** feature (preserve
  user customizations, add new keys, sweep removed keys with backup) — a
  separate follow-up task.
