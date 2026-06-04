# OTTO Gateway — Makefile
# Targets are intentionally simple. Per docs/briefs/go_port_brief.md §3.9,
# cross-compilation is a first-class concern; per §3.12, linting is gated.

BINARY      := otto-gateway
PKG         := ./cmd/$(BINARY)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS     := -s -w -X otto-gateway/internal/version.Version=$(VERSION)
BUILD_DIR   := bin
DIST_DIR    := dist
# Files that ship inside the laptop distribution package, in the
# otto_gateway/ root the user sees after extracting the archive. The
# operator-facing README is docs/operator-quickstart.md (the repo's
# top-level README.md is developer-facing); the packager renames it.
PKG_SCRIPTS := scripts/otto-gw scripts/otto-gw.ps1 scripts/.env.otto-gw.example
PKG_README  := docs/operator-quickstart.md
PKG_INSTALL := docs/INSTALL.md

.PHONY: all build build-fake-kiro run test test-race lint fmt fmt-check vet examples tidy clean cross ci arch-lint start stop status e2e e2e-list e2e-sdk-setup help \
        cross-darwin-arm64 cross-darwin-amd64 cross-linux-amd64 cross-windows-amd64 \
        package package-all package-checksums package-darwin-arm64 package-darwin-amd64 package-linux-amd64 package-windows-amd64

all: lint test build ## Lint, test, and build for the host platform

build: ## Build for the host platform
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(PKG)

run: ## Run the gateway on the host platform
	go run $(PKG)

test: ## Run unit + integration tests
	go test ./...

test-race: ## Run tests with the race detector (CI default)
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format Go sources (gofumpt preferred; falls back to gofmt)
	@if command -v gofumpt >/dev/null 2>&1; then gofumpt -w .; else gofmt -w .; fi

# Brief §3.12 trust-gate step 1: read-only formatting verification. Unlike `fmt`
# (write-mode), `fmt-check` is CI-safe — it produces no side effects and exits
# non-zero on any diff so misformatted code blocks the gate.
fmt-check: ## Verify gofumpt formatting (brief §3.12 step 1 — fails on diff)
	@if command -v gofumpt >/dev/null 2>&1; then \
		diff="$$(gofumpt -d . 2>&1)"; \
		tool="gofumpt"; \
	else \
		diff="$$(gofmt -d . 2>&1)"; \
		tool="gofmt"; \
	fi; \
	if [ -n "$$diff" ]; then \
		echo "FAIL: $$tool reports formatting diffs (brief §3.12):"; \
		echo "$$diff"; \
		exit 1; \
	fi

# Brief §3.12 trust-gate step 2: go vet. golangci-lint's govet linter already
# covers this, but the brief calls for an explicit step so the gate sequence is
# legible in both Makefile + CI logs (Phase 08.1 D-16).
vet: ## Run go vet (brief §3.12 step 2 — explicit even though govet linter covers it)
	go vet ./...

# Brief §3.12 trust-gate step 7: Example tests. Go's `Example_*` functions are
# runnable, output-validated, and surface in godoc; the brief gates them
# separately from the regular test suite to make the convention visible.
examples: ## Run go Example tests (brief §3.12 step 7)
	go test -run Example ./...

tidy: ## Tidy go.mod / go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR) $(DIST_DIR)

# Cross-compilation — the headline reason Go was chosen (see brief §2 / §3.9).
# These work from any host with the Go toolchain installed; no extra tools needed
# as long as cgo stays disabled.
cross: cross-darwin-arm64 cross-darwin-amd64 cross-linux-amd64 cross-windows-amd64 ## Cross-compile for darwin (arm64+amd64) + linux + windows

cross-darwin-arm64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 $(PKG)

cross-darwin-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 $(PKG)

cross-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(PKG)

cross-windows-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe $(PKG)

# Host-platform build of the deterministic fake kiro-cli worker used by the
# PII smoke-test scripts (Phase 08.3.2, TEST-01). NOT a dependency of `ci`,
# `all`, `build`, `package`, or any release-bundling target — per the phase's
# T4 mitigation the fake binary must never be auto-included in release
# artifacts. Operators run this explicitly when they want to point KIRO_CMD
# at a deterministic worker for `scripts/test-pii.*` round-trip verification.
# The $$(go env GOEXE) expansion yields ".exe" on Windows hosts and empty
# string on POSIX hosts, so one recipe works everywhere.
build-fake-kiro: ## Build deterministic fake-kiro-cli into bin/ for scripts/test-pii.* fake mode
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -o $(BUILD_DIR)/fake-kiro-cli$$(go env GOEXE) ./tests/e2e/cmd/fake-kiro-cli

# Distribution packaging — produces a self-contained otto_gateway/ folder
# the user extracts on their laptop:
#
#   otto_gateway/
#     bin/otto-gateway           (or otto-gateway.exe on Windows)
#     scripts/otto-gw            (POSIX wrapper — start|stop|run|env|logs|...)
#     scripts/otto-gw.ps1        (Windows wrapper)
#     scripts/.env.otto-gw.example
#     logs/.gitkeep              (empty; gateway writes rotated logs here)
#     README.md                  (operator quickstart)
#
# Unix archives are .tar.gz; Windows is .zip. Output to $(DIST_DIR)/.
# `make package` builds the host-OS archive only. `make package-all`
# builds for every supported OS/arch.
package: ## Build a distribution archive for the host OS/arch
	@os=$$(go env GOOS); arch=$$(go env GOARCH); \
		case "$$os-$$arch" in \
			darwin-arm64)   $(MAKE) package-darwin-arm64 ;; \
			darwin-amd64)   $(MAKE) package-darwin-amd64 ;; \
			linux-amd64)    $(MAKE) package-linux-amd64 ;; \
			windows-amd64)  $(MAKE) package-windows-amd64 ;; \
			*) echo "package: unsupported host $$os/$$arch — use cross targets" >&2; exit 1 ;; \
		esac

package-all: package-darwin-arm64 package-darwin-amd64 package-linux-amd64 package-windows-amd64 package-checksums ## Build distribution archives for every supported OS/arch

# package-checksums: produces SHA256SUMS-<version>.txt across every dist/
# archive. Operators verify with `shasum -a 256 -c SHA256SUMS-<version>.txt`
# (POSIX) or `Get-FileHash` (PowerShell). Prefer `shasum`; fall back to
# `sha256sum` on Linux hosts without BSD shasum.
package-checksums: ## Generate SHA256SUMS-<version>.txt for all archives in dist/
	@cd $(DIST_DIR) && ( \
		if command -v shasum >/dev/null 2>&1; then \
			shasum -a 256 otto_gateway-*-$(VERSION).tar.gz otto_gateway-*-$(VERSION).zip 2>/dev/null; \
		else \
			sha256sum otto_gateway-*-$(VERSION).tar.gz otto_gateway-*-$(VERSION).zip 2>/dev/null; \
		fi \
	) > SHA256SUMS-$(VERSION).txt
	@echo "→ $(DIST_DIR)/SHA256SUMS-$(VERSION).txt"
	@cat $(DIST_DIR)/SHA256SUMS-$(VERSION).txt

# stage_unix($1=goos, $2=goarch, $3=binary-suffix-on-disk):
# Wipes the staging dir, copies bin/wrappers/template/README/.gitkeep,
# preserves the otto-gw executable bit, and leaves an empty logs/ dir.
define stage_unix
	rm -rf $(DIST_DIR)/otto_gateway
	mkdir -p $(DIST_DIR)/otto_gateway/bin
	mkdir -p $(DIST_DIR)/otto_gateway/scripts
	mkdir -p $(DIST_DIR)/otto_gateway/logs
	cp $(BUILD_DIR)/$(BINARY)-$(1)-$(2)$(3) $(DIST_DIR)/otto_gateway/bin/$(BINARY)$(3)
	cp scripts/otto-gw scripts/otto-gw.ps1 scripts/otto-gw.bat scripts/setup.bat scripts/start.bat scripts/stop.bat scripts/status.bat scripts/.env.otto-gw.example scripts/test-pii.sh scripts/test-pii.ps1 $(DIST_DIR)/otto_gateway/scripts/
	chmod 755 $(DIST_DIR)/otto_gateway/scripts/otto-gw
	chmod 755 $(DIST_DIR)/otto_gateway/scripts/test-pii.sh
	cp $(PKG_README) $(DIST_DIR)/otto_gateway/README.md
	cp $(PKG_INSTALL) $(DIST_DIR)/otto_gateway/INSTALL.md
	: > $(DIST_DIR)/otto_gateway/logs/.gitkeep
endef

# codesign_adhoc($1=path): ad-hoc sign with empty identity. Removes the
# `cannot be opened because Apple cannot check it for malicious software`
# message on macOS Catalina+ for binaries that have NOT been downloaded
# (no com.apple.quarantine xattr — the README documents `xattr -d` for
# the downloaded-tarball case). Does NOT satisfy notarization — full
# Developer ID + notarytool is needed for that and requires a paid Apple
# Developer ID, which we deliberately keep out of scope for v1 laptop
# distribution. Skips silently on Linux/Windows hosts (no codesign).
define codesign_adhoc
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --sign - --force --options runtime --timestamp=none "$(1)" \
			&& echo "  ad-hoc signed: $(1)"; \
	else \
		echo "  (codesign not available on this host — skipping ad-hoc sign of $(1))"; \
	fi
endef

package-darwin-arm64: cross-darwin-arm64 $(PKG_README) $(PKG_INSTALL) ## Build otto_gateway-darwin-arm64-<version>.tar.gz
	$(call codesign_adhoc,$(BUILD_DIR)/$(BINARY)-darwin-arm64)
	@$(call stage_unix,darwin,arm64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-darwin-arm64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-darwin-arm64-$(VERSION).tar.gz"

package-darwin-amd64: cross-darwin-amd64 $(PKG_README) $(PKG_INSTALL)
	$(call codesign_adhoc,$(BUILD_DIR)/$(BINARY)-darwin-amd64)
	@$(call stage_unix,darwin,amd64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-darwin-amd64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-darwin-amd64-$(VERSION).tar.gz"

package-linux-amd64: cross-linux-amd64 $(PKG_README) $(PKG_INSTALL)
	@$(call stage_unix,linux,amd64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-linux-amd64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-linux-amd64-$(VERSION).tar.gz"

package-windows-amd64: cross-windows-amd64 $(PKG_README) $(PKG_INSTALL)
	@$(call stage_unix,windows,amd64,.exe)
	@command -v zip >/dev/null 2>&1 || { echo "ERROR: zip not installed (required for Windows package)" >&2; exit 1; }
	cd $(DIST_DIR) && zip -r -q otto_gateway-windows-amd64-$(VERSION).zip otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-windows-amd64-$(VERSION).zip"

arch-lint: ## Check architecture boundaries (requires go-arch-lint@v1.15.0)
	$(shell go env GOPATH)/bin/go-arch-lint check --project-path .

# Brief §3.12 canonical trust-gate sequence (Phase 08.1 D-16): fmt-check → vet
# → build → lint → test-race → arch-lint → examples → govulncheck → cross. Each
# target gates the next via Make's dependency ordering; govulncheck stays as a
# recipe step because it has no separate target. `cross` is intentionally NOT
# a dependency — CI runs it in a parallel job (see .github/workflows/ci.yml).
ci: fmt-check vet build lint test-race arch-lint examples ## Full CI gate (brief §3.12 canonical sequence)
	$(shell go env GOPATH)/bin/govulncheck ./...

start: ## Start gateway in background (wrapper script)
	@./scripts/otto-gw start

stop: ## Stop background gateway (wrapper script)
	@./scripts/otto-gw stop

status: ## Show gateway status (wrapper script)
	@./scripts/otto-gw status

# E2E suite: boots the real binary against real kiro-cli and ALWAYS renders a
# markdown report regardless of pass/fail. Deliberately NOT wired into `all` or
# `ci` (it needs a refreshed kiro-cli + network). The test exit code is captured
# to a tmpfile and re-raised after the report renders — no bash PIPESTATUS, so
# this stays POSIX-portable (the project Makefile sets no SHELL override).
#
# Select a subset with RUN=<regex> (passed to `go test -run`). Empty (default)
# runs everything. Examples:
#   make e2e RUN=TestE2E_Ollama            # only the Ollama group
#   make e2e RUN=TestE2E_Ollama/Tags       # one subtest
#   make e2e RUN='TestE2E_(Ollama|SharedGateway)'  # two groups
# Discover group names with `make e2e-list`.
RUN ?=
e2e: build ## Run E2E suite (real binary + kiro); RUN=<regex> selects a subset
	@mkdir -p tests/e2e/reports
	@TS=$$(date +%Y%m%d-%H%M%S); \
		( OTTO_E2E=1 go test -tags e2e -json -v -run "$(RUN)" ./tests/e2e/ > tests/e2e/reports/raw.jsonl; echo $$? > tests/e2e/reports/rc ); \
		go run ./tests/e2e/cmd/report < tests/e2e/reports/raw.jsonl > tests/e2e/reports/REPORT-$$TS.md; \
		cp tests/e2e/reports/REPORT-$$TS.md tests/e2e/reports/LATEST.md; \
		echo "E2E report: tests/e2e/reports/REPORT-$$TS.md"; \
		exit $$(cat tests/e2e/reports/rc)

e2e-list: ## List E2E test groups (names usable with RUN=)
	@go test -tags e2e -list '.*' ./tests/e2e/ | grep -E '^TestE2E' | sort

e2e-sdk-setup: ## Install the opt-in Node SDK harness (enables E2E steps 4-5)
	@command -v pnpm >/dev/null 2>&1 || { echo "ERROR: pnpm not found. We standardize on pnpm — install it (https://pnpm.io/installation), then re-run 'make e2e-sdk-setup'." >&2; exit 1; }
	cd tests/e2e/sdk && pnpm install

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
