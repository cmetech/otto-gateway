# Ignore Local Superpowers Workspace Design

## Goal

Prevent local Superpowers runtime and review artifacts from appearing as
untracked repository files or being committed accidentally.

## Design

Add the directory rule `/.superpowers/` to the repository root `.gitignore`
under the existing local tool-state section. The directory is entirely
workspace state: brainstorm server metadata and mockups, session tokens,
subagent task briefs, progress ledgers, reports, and review diff packages.

The rule applies to the complete directory rather than enumerating current
subdirectories. This keeps future Superpowers-generated workspace types local
without requiring repeated `.gitignore` updates.

## Safety and Scope

- Do not delete or modify any existing `.superpowers/` files.
- Do not add any `.superpowers/` files to version control.
- Do not change other ignore rules.
- Keep project documentation under `docs/superpowers/` tracked; the root-only
  `/.superpowers/` rule does not match that directory.

## Verification

After adding the rule:

1. `git check-ignore -v` identifies the root `.gitignore` rule `/.superpowers/` for a
   representative brainstorm file and a representative SDD file.
2. `git status --short --untracked-files=all -- .superpowers` produces no
   output.
3. `git check-ignore docs/superpowers/specs` fails, confirming tracked project
   documentation is not ignored.
4. `git diff --check` reports no whitespace errors.
