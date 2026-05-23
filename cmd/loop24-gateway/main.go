// Package main is the entry point for the Loop24 Gateway binary.
// It loads config, builds the structured logger, wires the HTTP server, and runs until signal.
//
// D-22: the binary stays foreground-only. start/stop/status are owned by scripts/loop24 (POSIX)
// and scripts/loop24.ps1 (PowerShell). Never add lifecycle subcommands to the binary.
package main

import (
	"context"
	"log/slog"
	"os"

	"loop24-gateway/internal/config"
	"loop24-gateway/internal/server"
	"loop24-gateway/internal/version"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Use a minimal stderr logger for startup errors (D-15: no slog.SetDefault).
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "err", err)
		os.Exit(1)
	}

	logger := buildLogger(cfg)

	// Phase 1 note: internal/acp does not exist yet (Plan 02).
	// main.go wires config → logger → server only; ACP client is added in Plan 02.

	srv := server.New(cfg, logger, version.Version)
	if err := srv.RunUntilSignal(context.Background()); err != nil {
		logger.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}

// buildLogger constructs the root *slog.Logger from the loaded config.
// D-15: never call slog.SetDefault. Logger is constructed once here and injected everywhere.
func buildLogger(cfg config.Config) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel(),
	}))
}
