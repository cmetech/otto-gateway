# Loop24 Gateway — Makefile
# Targets are intentionally simple. Per docs/briefs/go_port_brief.md §3.9,
# cross-compilation is a first-class concern; per §3.12, linting is gated.

BINARY      := loop24-gateway
PKG         := ./cmd/$(BINARY)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS     := -s -w -X loop24-gateway/internal/version.Version=$(VERSION)
BUILD_DIR   := bin

.PHONY: all build run test test-race lint fmt tidy clean cross ci start stop status help

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

ci: lint test-race ## Full CI gate (lint + race-tests + vuln scan)
	$(shell go env GOPATH)/bin/govulncheck ./...

start: ## Start gateway in background (wrapper script)
	@./scripts/loop24 start

stop: ## Stop background gateway (wrapper script)
	@./scripts/loop24 stop

status: ## Show gateway status (wrapper script)
	@./scripts/loop24 status

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
