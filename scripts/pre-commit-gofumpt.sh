#!/usr/bin/env bash
# pre-commit-gofumpt.sh — pre-commit framework hook for gofumpt.
#
# Invoked by .pre-commit-config.yaml's `gofumpt` hook. Reports any formatting
# violations among staged Go files and exits non-zero to block the commit.
#
# Phase 11 (CI-01) — see docs/operating.md "Pre-commit gate" section.

set -euo pipefail

if ! command -v gofumpt >/dev/null 2>&1; then
    echo "gofumpt not installed — run: go install mvdan.cc/gofumpt@latest" >&2
    exit 1
fi

# pre-commit passes the staged file paths as positional args.
if [ "$#" -eq 0 ]; then
    exit 0
fi

out=$(gofumpt -l "$@")
if [ -n "$out" ]; then
    echo "gofumpt formatting violations:" >&2
    echo "$out" >&2
    echo "fix with: gofumpt -w <file>" >&2
    exit 1
fi
