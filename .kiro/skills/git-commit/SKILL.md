---
name: git-commit
description: Create a git commit following Conventional Commits with repo-specific scope inference and dual-platform parity checks. Invoke via /git-commit or /git-commit "<scope hint>".
---

# Git Commit

Create a well-structured git commit for this repository.

## Context

This is a single-repo workspace (`loop_24`). Key layout:

```
loop_24/
├── acp_server/          # Node.js Ollama-compatible HTTP server proxying to Kiro ACP agents
│   ├── acp-ollama-server.js       # WSL/Linux build
│   └── acp-ollama-server-win.js   # Windows native build (keep in parity)
├── flows/               # LangFlow workflows
├── custom_components/   # LangFlow custom components
├── catalog/             # Component catalog
├── db/                  # SQLite DB (gitignored)
├── bootstrap.sh         # Linux/macOS/WSL bootstrap
├── bootstrap.ps1        # Windows bootstrap
└── .planning/           # Planning artifacts
```

Optional `$ARGUMENTS` provide a scope hint:
- `acp` / `server` → scope `acp_server`
- `win` / `windows` → scope `acp_server(win)`
- `wsl` / `linux` → scope `acp_server(posix)`
- `flows` → scope `flows`
- `bootstrap` → scope `bootstrap`
- `docs` → scope `docs`
- `ci` → scope `ci`

If no argument is provided, infer scope from the staged/changed paths.

## Instructions

1. Run `git status` to see current state.
2. Run `git diff HEAD` to see all changes.
3. Run `git branch --show-current` to get the current branch.
4. Run `git log --oneline -5` to see recent commit style.
5. **Parity check**: If changes touch `acp_server/acp-ollama-server.js` OR `acp_server/acp-ollama-server-win.js`, check whether the change is genuinely platform-specific (one of the four documented deltas: `spawn()` shell flag, file URI parsing, path ops, EPIPE handling). If not, warn that the sibling file likely needs the same change before committing.
6. Stage appropriate files (selective — never stage `.env`, `db/langflow.db`, or `.venv/`).
7. Create a single git commit following the format below.
8. Output the commit summary.

## Commit Message Format

Follow Conventional Commits:

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Types

- **feat**: New feature
- **fix**: Bug fix
- **docs**: Documentation only
- **style**: Formatting (no logic change)
- **refactor**: Code change (no fix, no feature)
- **perf**: Performance improvement
- **test**: Tests
- **build**: Build system / dependencies
- **ci**: CI configuration
- **chore**: Other (no src/test change)
- **revert**: Reverts a previous commit

### Subject Line Rules

- Under 50 characters
- Lowercase
- Imperative mood ("add" not "added")
- No trailing period

### Body Rules

- Wrap at 72 characters
- Explain what and why, not how
- Blank line between subject and body

### Scope Examples

```
feat(acp_server): add embedding model warmup at startup
fix(acp_server(win)): strip file:/// with three slashes on Windows
docs: update LangFlow quickstart in README
chore(bootstrap): bump kiro-cli minimum version
```

## Pre-Commit Checks

Before committing, verify:
- No debugging code or stray `console.log`
- No secrets, `.env` values, or local paths
- No commented-out code blocks
- Only files related to the logical change are staged
- For ACP server changes: both platform files updated (unless platform-specific)

## Commit Summary Format

After committing, output:

```
Commit Details

  - Type: [type] - [brief meaning]
  - Scope: [scope] - [what it affects]
  - Branch: [branch]
  - Hash: [short hash]

What Changed

  [2-3 sentences: what changed and why, imperative mood]
```
