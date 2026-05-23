---
allowed-tools: Bash(git add:*), Bash(git status:*), Bash(git commit:*), Bash(git diff:*), Bash(git branch:*), Bash(git log:*)
description: Create a git commit
---

## Context

This is a single-repo workspace (`loop_24`). Top-level layout:

```
loop_24/
├── acp_server/          # Node.js Ollama-compatible HTTP server proxying to Kiro ACP agents
│   ├── acp-ollama-server.js       # WSL/Linux build
│   └── acp-ollama-server-win.js   # Windows native build (keep in parity)
├── flows/               # LangFlow workflows (imported, templates)
├── custom_components/   # LangFlow custom components
├── catalog/             # Catalog assets
├── db/                  # SQLite DB for LangFlow (gitignored contents)
├── schemas/             # Schemas
├── minutes/             # Meeting notes
├── bootstrap.sh         # Linux/macOS/WSL bootstrap
├── bootstrap.ps1        # Windows bootstrap
└── .planning/           # GSD planning artifacts
```

Optional `$ARGUMENTS` provide a hint for the commit `scope`. Examples:
- `acp` / `server` → scope `acp_server`
- `win` / `windows` → scope `acp_server(win)` (Windows server changes)
- `wsl` / `linux` → scope `acp_server(posix)`
- `flows` → scope `flows`
- `bootstrap` → scope `bootstrap`
- `docs` → scope `docs`
- `ci` → scope `ci`

If no argument is provided, infer scope from the staged paths.

## Instructions

1. Run `git status` to see current state.
2. Run `git diff HEAD` to see all changes.
3. Run `git branch --show-current` to see current branch.
4. Run `git log --oneline -10` to see recent commit style.
5. **If changes touch `acp_server/acp-ollama-server.js` OR `acp_server/acp-ollama-server-win.js`**: check whether the change is genuinely platform-specific (one of the four documented deltas in `CLAUDE.md`: `KIRO_CMD`, `spawn()` shell flag, file URI parsing, path ops). If not, warn that the sibling file likely needs the same change before committing.
6. Based on the changes, create a single git commit following the conventions below.
7. After committing, output the standardized summary defined in "Commit Summary Format".

## Git Best Practices

This rule applies to all git operations, especially commits, to ensure consistent and professional version control practices.

## Commit Message Format

### Structure

Follow the Conventional Commits specification:

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Types

- **feat**: A new feature
- **fix**: A bug fix
- **docs**: Documentation only changes
- **style**: Changes that do not affect the meaning of the code (white-space, formatting, missing semi-colons, etc)
- **refactor**: A code change that neither fixes a bug nor adds a feature
- **perf**: A code change that improves performance
- **test**: Adding missing tests or correcting existing tests
- **build**: Changes that affect the build system or external dependencies
- **ci**: Changes to CI configuration files and scripts
- **chore**: Other changes that don't modify src or test files
- **revert**: Reverts a previous commit

### Examples

```
feat(acp_server): add embedding model warmup at startup
fix(acp_server): resolve tool-call coercion for fenced JSON
fix(acp_server(win)): strip file:/// with three slashes on Windows
docs: update LangFlow quickstart in README
refactor(acp_server): simplify ACPSession pending-request tracking
chore(bootstrap): bump kiro-cli minimum version
```

## Commit Message Guidelines

### Subject Line (First Line)

- **Length**: Keep under 50 characters
- **Capitalization**: Use lowercase for type and description
- **Tense**: Use imperative mood ("add" not "added" or "adds")
- **Punctuation**: No period at the end
- **Clarity**: Be specific and descriptive

### Body (Optional)

- **Length**: Wrap at 72 characters
- **Content**: Explain what and why, not how
- **Separation**: Leave a blank line between subject and body

### Footer (Optional)

- **Breaking Changes**: Start with "BREAKING CHANGE:"
- **Issue References**: "Closes #123" or "Fixes #456"
- **Co-authors**: "Co-authored-by: Name <email@example.com>"

## Pre-Commit Checklist

Before making any commit, ensure:

1. **Code Quality**
   - No debugging code, `console.log`, or stray `DEBUG=1` left enabled
   - No commented-out code blocks
   - No secrets, `.env` values, or local DB paths

2. **Staging**
   - Only stage files that are part of the logical change
   - Review staged changes with `git diff --cached`
   - Avoid bundling unrelated changes
   - Never stage `.env`, `db/langflow.db`, or anything under `.venv/`

3. **Commit Scope**
   - Each commit should represent one logical change
   - If fixing multiple unrelated issues, make separate commits
   - For WSL/Windows ACP server parity changes, prefer **one commit covering both files** (so the port is atomic) unless the change is platform-specific

## Branch Naming Conventions

- **Feature branches**: `feat/feature-name` or `corey/feature-name`
- **Bug fixes**: `fix/bug-description`
- **Documentation**: `docs/update-description`
- **Refactoring**: `refactor/component-name`

Current working branch is typically `corey/dev`; PRs target `main`.

## Workflow Best Practices

### Before Committing

1. There are **no automated tests in this repo** — validation is manual:
   - For `acp_server/` changes: start the server (`npm run ollama_dev` or `npm run win_dev`) and hit `/health`, `/api/tags`, and `/api/chat` with `curl`.
   - For `flows/` or LangFlow changes: open `uv run langflow run` and exercise the affected flow.
2. Review changes: `git diff` and `git status`
3. Stage selectively: `git add -p` for partial staging

### Commit Frequency

- Commit early and often
- Each commit should be a working state
- Prefer smaller, focused commits over large ones

### Push Guidelines

- Don't push broken code to shared branches
- Rebase feature branches before merging
- Use `git push --force-with-lease` instead of `--force`

## Tools and Commands

### Useful Git Commands

```bash
git add -p                  # interactive staging
git commit --amend          # amend last commit (only if not pushed)
git rebase -i HEAD~n        # interactive rebase
git log --oneline -n 5      # check recent commit messages
```

## Error Prevention

### Common Mistakes to Avoid

- Committing sensitive information (`.env`, API keys, Kiro credentials)
- Committing the SQLite DB (`db/langflow.db`) or `.venv/`
- Diverging `acp-ollama-server.js` and `acp-ollama-server-win.js` outside the four documented platform deltas
- Vague commit messages like "fix stuff" or "update"
- Mixing formatting changes with logic changes
- Committing commented-out code

### Security Considerations

- Never commit secrets or credentials
- Use `.gitignore` for sensitive files (already covers `.env`, `db/*`, `.claude/`, `.venv/`)
- Review commits before pushing

## Commit Summary Format

After creating the commit, ALWAYS provide a summary in the following standardized format:

```
Commit Details

  - Type: [type] - [Brief description of what this type means]
  - Scope: [scope] - [What part of the codebase this affects]
  - Branch: [branch name]
  - Commit Hash: [short hash]

What Changed

  [2-3 paragraphs describing the changes. Focus on WHAT changed and WHY.
   Be clear and concise. Wrap text at reasonable line length for readability.
   Use imperative mood.]

Commit Message Structure

  The commit follows the Conventional Commits specification:
  - Clear, descriptive subject line (under 50 characters)
  - Detailed body explaining the "what" and "why"
  - Imperative mood ("Add" not "Added")
  - Properly formatted with line wrapping

  The commit is now ready to be pushed to the remote repository when you're ready.
```

### Example

```
Commit Details

  - Type: fix - This is a bug fix addressing tool-call parsing
  - Scope: acp_server - Affects the Ollama-compatible HTTP server
  - Branch: corey/dev
  - Commit Hash: 7569745

What Changed

  Update coerceToolCall() to strip Markdown code fences before attempting JSON
  parsing. LangChain agents using Kiro models occasionally emit tool calls as
  fenced JSON blocks inside the assistant's text content, which previously
  failed to coerce and surfaced as plain text to the caller.

  The fix preserves existing behavior for raw-JSON and true tool-call
  notifications, and only activates when the message body opens with a fenced
  block whose language tag is empty or "json".

Commit Message Structure

  The commit follows the Conventional Commits specification:
  - Clear, descriptive subject line (under 50 characters)
  - Detailed body explaining the "what" and "why"
  - Imperative mood ("Add" not "Added")
  - Properly formatted with line wrapping

  The commit is now ready to be pushed to the remote repository when you're ready.
```

## Integration with Development Workflow

When using tools or agents that make commits:

1. Always review the proposed commit message
2. Ensure it follows these conventions
3. Verify the staged changes are appropriate
4. Add additional context in the body if needed
5. Consider splitting large changes into multiple commits

This rule should be referenced and followed by all automated tools, agents, and manual git operations in this workspace.
