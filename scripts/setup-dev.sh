#!/usr/bin/env bash
# Loop24 Gateway — macOS dev environment bootstrap.
# Idempotent: safe to re-run. Skips already-installed tools.
# See DEVELOPERS.md for the manual equivalent and gotchas.

set -euo pipefail

echo "Loop24 Gateway — macOS dev environment bootstrap"
echo "(idempotent — safe to re-run)"
echo

# --- Pre-flight: OS check -----------------------------------------------------

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "ERROR: this script targets macOS only." >&2
  echo "For Windows, use scripts/setup-dev.ps1." >&2
  exit 1
fi

# --- Pre-flight: Homebrew -----------------------------------------------------

if ! command -v brew >/dev/null 2>&1; then
  echo "ERROR: Homebrew is not installed." >&2
  echo "Install it from https://brew.sh (we don't auto-install — it needs sudo)." >&2
  exit 1
fi

# --- Tool install loop --------------------------------------------------------
# Order matters only insofar as we want Go available early. The rest are
# independent. Each block: probe, then either [skip] or [install].

if command -v go >/dev/null 2>&1; then
  echo "[skip] go already installed: $(go version)"
else
  echo "[install] go"
  brew install go
fi

if command -v golangci-lint >/dev/null 2>&1; then
  echo "[skip] golangci-lint already installed: $(golangci-lint --version)"
else
  echo "[install] golangci-lint"
  brew install golangci-lint
fi

if command -v pre-commit >/dev/null 2>&1; then
  echo "[skip] pre-commit already installed: $(pre-commit --version)"
else
  echo "[install] pre-commit"
  brew install pre-commit
fi

if command -v gosec >/dev/null 2>&1; then
  # gosec prints version on stderr on some versions; collapse both streams.
  echo "[skip] gosec already installed: $(gosec --version 2>&1 | head -1)"
else
  echo "[install] gosec"
  brew install gosec
fi

if command -v gofumpt >/dev/null 2>&1; then
  echo "[skip] gofumpt already installed: $(gofumpt --version)"
else
  echo "[install] gofumpt"
  brew install gofumpt
fi

if command -v gitleaks >/dev/null 2>&1; then
  echo "[skip] gitleaks already installed: $(gitleaks version)"
else
  echo "[install] gitleaks"
  brew install gitleaks
fi

# --- Versions summary ---------------------------------------------------------

echo
echo "==== Installed versions ===="
go version
golangci-lint --version
pre-commit --version
gosec --version 2>&1 | head -1
gofumpt --version
gitleaks version

# --- Next steps ---------------------------------------------------------------

echo
echo "==== Next steps ===="
echo "Run these yourself — this script does NOT auto-run them:"
echo
echo "  pre-commit install"
echo "    wires the git pre-commit hook so lint/secret-scan/format"
echo "    checks run on every commit."
echo
echo "  make help"
echo "    list available make targets."
echo
echo "  make build && make test && make lint"
echo "    verify the toolchain end-to-end."
echo
echo "Heads-up: on a fresh clone, the 'go mod tidy' pre-commit hook will"
echo "fail until the first dependency is added. See DEVELOPERS.md →"
echo "Known issues for the workaround."
