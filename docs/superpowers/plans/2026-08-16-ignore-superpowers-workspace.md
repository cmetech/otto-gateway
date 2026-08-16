# Ignore Local Superpowers Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the repository-local `.superpowers/` workspace out of Git status and prevent its runtime and review artifacts from being committed accidentally.

**Architecture:** Add one root-level directory rule to the existing local tool-state section of `.gitignore`. Verify the rule covers both brainstorm and SDD artifacts while leaving tracked `docs/superpowers/` project documentation unaffected.

**Tech Stack:** Git ignore rules and shell verification commands.

## Global Constraints

- Add the exact root-directory ignore rule `.superpowers/`.
- Do not delete or modify any existing `.superpowers/` files.
- Do not add any `.superpowers/` files to version control.
- Do not change other ignore rules.
- Keep `docs/superpowers/` tracked.

---

### Task 1: Ignore the local Superpowers workspace

**Files:**
- Modify: `.gitignore`
- Test: Git status and ignore-source assertions in this task

**Interfaces:**
- Consumes: Git's repository-root `.gitignore` rules.
- Produces: A root-only `.superpowers/` ignore rule that does not match `docs/superpowers/`.

- [ ] **Step 1: Prove the brainstorm workspace is currently unignored**

```bash
test -n "$(git status --short --untracked-files=all -- .superpowers)"
! git check-ignore -q .superpowers/brainstorm/.last-token
```

Expected: both commands exit 0. Git status reports `.superpowers/` content,
and the representative brainstorm token is not ignored by any current rule.

- [ ] **Step 2: Add the minimal root ignore rule**

In the root `.gitignore`, immediately after the existing `.otto/` rule, add:

```gitignore
# Local Superpowers brainstorming and subagent workspace state.
.superpowers/
```

Do not edit any other ignore rule or delete workspace files.

- [ ] **Step 3: Verify both workspace types use the root rule**

```bash
brainstorm_rule=$(git check-ignore -v .superpowers/brainstorm/.last-token)
sdd_rule=$(git check-ignore -v .superpowers/sdd/progress.md)
printf '%s\n%s\n' "$brainstorm_rule" "$sdd_rule"
printf '%s\n' "$brainstorm_rule" | rg '^\.gitignore:[0-9]+:\.superpowers/'
printf '%s\n' "$sdd_rule" | rg '^\.gitignore:[0-9]+:\.superpowers/'
```

Expected: both assertions exit 0 and identify the root `.gitignore` rule.

- [ ] **Step 4: Verify Git status is clean for the workspace and docs stay tracked**

```bash
test -z "$(git status --short --untracked-files=all -- .superpowers)"
! git check-ignore -q --no-index \
  docs/superpowers/specs/2026-08-16-ignore-superpowers-workspace-design.md
git ls-files --error-unmatch \
  docs/superpowers/specs/2026-08-16-ignore-superpowers-workspace-design.md
```

Expected: all commands exit 0. No `.superpowers/` file appears in status,
and the design document remains unignored and tracked.

- [ ] **Step 5: Inspect and validate the exact diff**

```bash
git diff --check
git diff -- .gitignore
```

Expected: no whitespace errors; the diff contains only the comment and exact
`.superpowers/` rule after `.otto/`.

- [ ] **Step 6: Commit the ignore rule**

```bash
git add .gitignore
git commit -m "chore: ignore Superpowers workspace state"
```

Expected: one commit containing only `.gitignore`.

