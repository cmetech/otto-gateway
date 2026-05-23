// Package main is the entry point for the Loop24 Gateway binary.
// It loads config, builds the structured logger, conditionally wires the ACP client
// (only when KIRO_CMD is set), starts the HTTP server, and runs until signal.
//
// D-22: the binary stays foreground-only. start/stop/status are owned by scripts/loop24 (POSIX)
// and scripts/loop24.ps1 (PowerShell). Never add lifecycle subcommands to the binary.
// REVIEW FIX (Codex MEDIUM): ACP client is only started when KIRO_CMD env var is set.
// This prevents /health startup from failing when kiro-cli is not installed.
package main

import (
	"context"
	"log/slog"
	"os"

	"loop24-gateway/internal/acp"
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

	// REVIEW FIX (Codex MEDIUM — conditional ACP init):
	// Only initialize the ACP client when KIRO_CMD is set. This lets /health start
	// successfully on machines where kiro-cli is not installed (e.g., pure HTTP routing).
	// main.go only calls acp.New when KiroCmd != "".
	if cfg.KiroCmd != "" {
		acpCfg := acp.Config{
			Logger:       logger,
			Command:      cfg.KiroCmd,
			Args:         cfg.KiroArgs,
			Cwd:          cfg.KiroCWD,
			PingInterval: cfg.PingInterval,
		}
		acpClient, err := acp.New(acpCfg)
		if err != nil {
			logger.Error("acp: init failed", "err", err)
			os.Exit(1)
		}
		defer func() {
			if err := acpClient.Close(); err != nil {
				logger.Error("acp: close error", "err", err)
			}
		}()
		_ = acpClient // passed to server in Phase 2 when handlers need it
	}

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
