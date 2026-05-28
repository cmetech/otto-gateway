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

.PHONY: all build run test test-race lint fmt tidy clean cross ci arch-lint start stop status e2e e2e-list e2e-sdk-setup help \
        cross-darwin-arm64 cross-darwin-amd64 cross-linux-amd64 cross-windows-amd64 \
        package package-all package-darwin-arm64 package-darwin-amd64 package-linux-amd64 package-windows-amd64

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

package-all: package-darwin-arm64 package-darwin-amd64 package-linux-amd64 package-windows-amd64 ## Build distribution archives for every supported OS/arch

# stage_unix($1=goos, $2=goarch, $3=binary-suffix-on-disk):
# Wipes the staging dir, copies bin/wrappers/template/README/.gitkeep,
# preserves the otto-gw executable bit, and leaves an empty logs/ dir.
define stage_unix
	rm -rf $(DIST_DIR)/otto_gateway
	mkdir -p $(DIST_DIR)/otto_gateway/bin
	mkdir -p $(DIST_DIR)/otto_gateway/scripts
	mkdir -p $(DIST_DIR)/otto_gateway/logs
	cp $(BUILD_DIR)/$(BINARY)-$(1)-$(2)$(3) $(DIST_DIR)/otto_gateway/bin/$(BINARY)$(3)
	cp scripts/otto-gw scripts/otto-gw.ps1 scripts/.env.otto-gw.example $(DIST_DIR)/otto_gateway/scripts/
	chmod 755 $(DIST_DIR)/otto_gateway/scripts/otto-gw
	cp $(PKG_README) $(DIST_DIR)/otto_gateway/README.md
	: > $(DIST_DIR)/otto_gateway/logs/.gitkeep
endef

package-darwin-arm64: cross-darwin-arm64 $(PKG_README) ## Build otto_gateway-darwin-arm64-<version>.tar.gz
	@$(call stage_unix,darwin,arm64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-darwin-arm64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-darwin-arm64-$(VERSION).tar.gz"

package-darwin-amd64: cross-darwin-amd64 $(PKG_README)
	@$(call stage_unix,darwin,amd64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-darwin-amd64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-darwin-amd64-$(VERSION).tar.gz"

package-linux-amd64: cross-linux-amd64 $(PKG_README)
	@$(call stage_unix,linux,amd64,)
	cd $(DIST_DIR) && tar -czf otto_gateway-linux-amd64-$(VERSION).tar.gz otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-linux-amd64-$(VERSION).tar.gz"

package-windows-amd64: cross-windows-amd64 $(PKG_README)
	@$(call stage_unix,windows,amd64,.exe)
	@command -v zip >/dev/null 2>&1 || { echo "ERROR: zip not installed (required for Windows package)" >&2; exit 1; }
	cd $(DIST_DIR) && zip -r -q otto_gateway-windows-amd64-$(VERSION).zip otto_gateway
	@echo "→ $(DIST_DIR)/otto_gateway-windows-amd64-$(VERSION).zip"

arch-lint: ## Check architecture boundaries (requires go-arch-lint@v1.15.0)
	$(shell go env GOPATH)/bin/go-arch-lint check --project-path .

ci: lint test-race arch-lint ## Full CI gate (lint + race-tests + govulncheck + arch-lint)
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
