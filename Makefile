# OTTO Gateway — Makefile
# Targets are intentionally simple. Per docs/briefs/go_port_brief.md §3.9,
# cross-compilation is a first-class concern; per §3.12, linting is gated.

BINARY      := otto-gateway
PKG         := ./cmd/$(BINARY)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS     := -s -w -X otto-gateway/internal/version.Version=$(VERSION)
BUILD_DIR   := bin

.PHONY: all build run test test-race lint fmt tidy clean cross ci arch-lint start stop status e2e e2e-sdk-setup help

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
	rm -rf $(BUILD_DIR)

# Cross-compilation — the headline reason Go was chosen (see brief §2 / §3.9).
# These work from any host with the Go toolchain installed; no extra tools needed
# as long as cgo stays disabled.
cross: cross-linux-amd64 cross-windows-amd64 ## Cross-compile for Linux + Windows (x86_64)

cross-linux-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 $(PKG)

cross-windows-amd64:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe $(PKG)

arch-lint: ## Check architecture boundaries (requires go-arch-lint@v1.15.0)
	$(shell go env GOPATH)/bin/go-arch-lint check --project-path .

ci: lint test-race arch-lint ## Full CI gate (lint + race-tests + govulncheck + arch-lint)
	$(shell go env GOPATH)/bin/govulncheck ./...

start: ## Start gateway in background (wrapper script)
	@./scripts/otto start

stop: ## Stop background gateway (wrapper script)
	@./scripts/otto stop

status: ## Show gateway status (wrapper script)
	@./scripts/otto status

# E2E suite: boots the real binary against real kiro-cli and ALWAYS renders a
# markdown report regardless of pass/fail. Deliberately NOT wired into `all` or
# `ci` (it needs a refreshed kiro-cli + network). The test exit code is captured
# to a tmpfile and re-raised after the report renders — no bash PIPESTATUS, so
# this stays POSIX-portable (the project Makefile sets no SHELL override).
e2e: build ## Run E2E suite (real binary + kiro) and write a markdown report
	@mkdir -p tests/e2e/reports
	@TS=$$(date +%Y%m%d-%H%M%S); \
		( OTTO_E2E=1 go test -tags e2e -json -v ./tests/e2e/ > tests/e2e/reports/raw.jsonl; echo $$? > tests/e2e/reports/rc ); \
		go run ./tests/e2e/cmd/report < tests/e2e/reports/raw.jsonl > tests/e2e/reports/REPORT-$$TS.md; \
		cp tests/e2e/reports/REPORT-$$TS.md tests/e2e/reports/LATEST.md; \
		echo "E2E report: tests/e2e/reports/REPORT-$$TS.md"; \
		exit $$(cat tests/e2e/reports/rc)

e2e-sdk-setup: ## Install the opt-in Node SDK harness (enables E2E steps 4-5)
	cd tests/e2e/sdk && (pnpm install || npm install)

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
