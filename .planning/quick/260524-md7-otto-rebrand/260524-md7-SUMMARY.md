---
phase: quick-260524-md7
plan: 01
subsystem: project-wide rebrand
tags: [rebrand, branding, otto-gateway, mechanical-refactor]
tier: 2
requires: []
provides:
  - "Go module path otto-gateway"
  - "Binary name otto-gateway"
  - "Wrapper command scripts/otto + scripts/otto.ps1"
  - "OTTO Gateway brand across code, build/lint config, scripts, docs"
affects:
  - go.mod
  - cmd/otto-gateway/
  - Makefile
  - scripts/
  - internal/
  - docs/
tech-stack:
  added: []
  patterns: ["BSD-safe literal seds (no \\b word boundaries)"]
key-files:
  created:
    - cmd/otto-gateway/main.go (renamed from cmd/loop24-gateway/)
    - cmd/otto-gateway/main_test.go (renamed)
    - scripts/otto (renamed from scripts/loop24)
    - scripts/otto.ps1 (renamed from scripts/loop24.ps1)
    - docs/architecture/otto_architecture_infographic_prompt.md (renamed)
    - docs/architecture/otto_activity_flow_v3.png (renamed)
  modified:
    - go.mod
    - Makefile
    - internal/config/config.go
    - internal/**/*.go (48 files, import-path rewrite + brand prose)
    - .golangci.yml, .pre-commit-config.yaml, .gitignore, .gitattributes
    - README.md, DEVELOPERS.md, CLAUDE.md, docs/README.md, docs/operating.md
    - docs/reference/acp_wire_shapes.md
decisions:
  - "Repo working directory loop24-gateway/ NOT renamed — deferred to Tier 3"
  - "Preserved external loop24-client / Loop24-client / @loop24/client product references verbatim"
  - "Preserved external loop_24/acp_server upstream reference verbatim"
  - "Node-parity functional env var names kept byte-identical"
  - "ClientInfo name + root response JSON name treated as brand strings (rebranded to otto-gateway)"
metrics:
  duration: ~5m
  completed: 2026-05-24T20:24:03Z
  tasks: 5
  commits: 5
---

# Quick Task 260524-md7: OTTO Gateway Rebrand Summary

Mechanical Tier-2 rebrand of the project from `loop24-gateway` to `otto-gateway`
across Go module path, binary, wrapper command, build/lint config, source prose,
test-harness env vars, and docs — with all trust gates green and both external
assets (`loop_24/acp_server` upstream, `loop24-client` product) preserved verbatim.

## Tier Scope

This is **Tier 2** (module/binary/wrapper/brand strings). The repo working directory
`loop24-gateway/` is intentionally **NOT renamed** — that is **deferred to Tier 3**.

## Tasks Completed

| Task | Name | Commit |
|------|------|--------|
| 1 | Go module path rename (loop24-gateway -> otto-gateway, 87 import refs / 48 files, cmd dir git mv) | `4061e02` |
| 2 | Build + lint config rebrand (Makefile BINARY/LDFLAGS/scripts, config.go FlagSet, .golangci.yml/.pre-commit/.gitignore/.gitattributes) | `290b336` |
| 3 | Wrapper + setup scripts rebrand (scripts/loop24{,.ps1} -> scripts/otto{,.ps1}, LOOP24_* -> OTTO_*, ADDR value 11435 preserved) | `75937a4` |
| 4 | Go source brand prose + test-harness env vars (Loop24 Gateway/gateway -> OTTO Gateway, LOOP24_KIRO_BIN/LOOP24_INTEGRATION -> OTTO_*, pool_test fixture) | `cdc9c76` |
| 5 | Docs rebrand + architecture asset renames + final acceptance gate | `e89cbf3` |

## Acceptance Gate Results (all PASS)

| Gate | Result |
|------|--------|
| `go build ./...` | PASS (clean) |
| `go vet ./...` | PASS (clean) |
| `go test ./... -race -count=1` | PASS (10 packages ok, 0 failures) |
| `golangci-lint run ./...` | PASS (0 issues) |
| `make arch-lint` | PASS (OK - No warnings found, exit 0) |
| `make clean && make build` | PASS (produces `bin/otto-gateway`) |
| `./bin/otto-gateway --version` | PASS (prints `cdc9c76-dirty`, exit 0) |
| `./bin/otto-gateway --help` header | PASS (reads `Usage of otto-gateway:`) |
| `./scripts/otto status` | PASS (`otto-gateway: stopped`, exit 1 = stopped, runs cleanly) |
| `grep -rn 'loop24' --include='*.go' . \| grep -iv 'loop24-client'` | PASS (ZERO) |
| Node-parity env vars in config.go | PASS (31 occurrences across the 14 names, all intact) |

## Residual loop24 References (all allowed exceptions — reported exactly)

Repo-wide grep `grep -rni 'loop24' . --exclude-dir=.git --exclude-dir=.planning --exclude-dir=bin | grep -iv 'loop24-client'`
yields ONLY these allowed residuals:

1. **External upstream `loop_24/acp_server`** (UNDERSCORE — not our brand):
   - `CLAUDE.md:11` — `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`
   - `docs/reference/acp_wire_shapes.md:8` — same upstream path
2. **`.claude` / `.kiro` tooling skill files** (git-commit workspace name `loop_24`, tooling not brand):
   - `.claude/commands/git-commit.md:8,11`
   - `.kiro/skills/git-commit/SKILL.md:12,15`
3. **External product `@loop24/client`** (npm scope of the loop24-client product):
   - `docs/briefs/go_port_brief.md:211` — `@loop24/client v1.0.1`

   Note: `@loop24/client` is the npm package name of the SAME external `loop24-client`
   product. The plan's `grep -iv 'loop24-client'` exclusion does not literally match
   `loop24/client` (slash, not hyphen), so it surfaces in the guardrail grep, but it is
   an external-product identifier and was correctly **preserved verbatim** (not rebranded).

ZERO non-underscore `loop24` brand strings remain outside the external loop24-client product.

## Preserved Assets (verified verbatim)

- **`loop24-client` / `Loop24-client`** (external Pi-SDK / GSD Pi product, both casings):
  - Lowercase in `internal/server/server.go:179`, `internal/adapter/anthropic/render.go:186`,
    `render_test.go:136`, `wire.go:293`, `README.md:151,155`, `go_port_brief.md:210,295,296,675`
  - Capital-L `Loop24-client` in `internal/adapter/anthropic/wire.go:98`
- **`loop_24/acp_server`** external upstream — untouched in CLAUDE.md + acp_wire_shapes.md
- **Node-parity functional env vars** — byte-identical: KIRO_CMD, KIRO_ARGS, KIRO_CWD,
  DEBUG, HTTP_ADDR, PING_INTERVAL, AUTH_TOKEN, ALLOWED_IPS, AUTH_TRUST_XFF, POOL_SIZE,
  OLLAMA_PATH_PREFIX, OPENAI_PATH_PREFIX, ANTHROPIC_PATH_PREFIX, ENABLED_SURFACES,
  EMBEDDING_MODEL_DEFAULT
- **Repo working directory** `loop24-gateway/` — NOT renamed (Tier 3 deferred)
- **`.planning/` history** — NOT bulk-rewritten

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `.golangci.yml` brand comment missed by lowercase survey**
- **Found during:** Task 2
- **Issue:** Initial `grep -n loop24 .golangci.yml` (case-sensitive lowercase) returned nothing,
  but the file's line 1 header comment was `# Loop24 Gateway — golangci-lint config` (capital-L).
  The Task 2 verify (`grep -rni`, case-insensitive) caught the residual.
- **Fix:** Added `sed 's/# Loop24 Gateway — golangci-lint config/# OTTO Gateway — golangci-lint config/'`.
- **Commit:** `290b336`

**2. [Rule 3 - Blocking] Pre-existing uncommitted user work in README.md + a new untracked PNG**
- **Found during:** Task 1 (discovered when staging)
- **Issue:** The repo had pre-existing UNCOMMITTED working-tree changes — a substantially
  rewritten `README.md` (new OTTER/OSCAR/Langflow narrative) and a new untracked
  `docs/architecture/loop24_activity_flow_v3.png` that the README references. These were
  NOT produced by this task's seds (which only touched `*.go` in Task 1). The session-start
  git-status snapshot ("clean") was stale. Per project memory ("don't discard uncommitted
  changes"), this in-progress user content was preserved.
- **Fix:** Unstaged README.md + the PNG from the Task 1 commit so each task commit stayed
  scoped to its own change set. The user's README narrative was preserved intact; in Task 5
  (which owns README + the PNG rename by plan scope) only the brand tokens within the
  narrative were rebranded (`Loop24 Gateway` -> `OTTO Gateway`, `loop24 stack/product family`
  -> `OTTO`, script/bin paths, `LOOP24_*` -> `OTTO_*`, png filename) while every sentence of
  the OTTER/OSCAR story was kept. The untracked PNG was renamed via plain `mv` (git mv failed
  because it was not yet tracked) to `otto_activity_flow_v3.png` and added under the new name;
  the README image reference was updated to match.
- **Files modified:** README.md, docs/architecture/otto_activity_flow_v3.png
- **Commit:** `e89cbf3`

**3. [Rule 1 - Bug] Bold-markdown broke a literal sed on README.md:5**
- **Found during:** Task 5
- **Issue:** The literal sed `s/loop24 product family/.../` did not match `**loop24** product family`
  (markdown bold asterisks split the token).
- **Fix:** Explicit Edit replacing `**loop24** product family` -> `**OTTO** product family`.
- **Commit:** `e89cbf3`

### Notes
- ClientInfo `Name` (`internal/acp/client.go:523`) and the root response JSON
  `{"name":"loop24-gateway"}` (`internal/server/server.go:213`) were rebranded to
  `otto-gateway`. These are brand identifiers (not Node-parity functional env vars), so
  rebranding them is correct per the plan's scope.
- All seds used fixed literal multi-word contexts — NO `\b` word-boundary metacharacters
  anywhere (BSD/darwin-safe), guaranteeing `Loop24-client`/`loop24-client` was never matched.

## Known Stubs

None.

## Self-Check: PASSED

- cmd/otto-gateway/main.go — FOUND
- scripts/otto — FOUND
- scripts/otto.ps1 — FOUND
- docs/architecture/otto_activity_flow_v3.png — FOUND
- docs/architecture/otto_architecture_infographic_prompt.md — FOUND
- go.mod `module otto-gateway` — FOUND
- bin/otto-gateway (make build artifact) — FOUND
- Commits 4061e02, 290b336, 75937a4, cdc9c76, e89cbf3 — all FOUND in git log
