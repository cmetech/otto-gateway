// Package config provides typed gateway configuration loaded from environment variables.
// All env var names match the Node reference implementation for drop-in binary replacement.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all gateway configuration loaded from environment variables.
// Phase 1 reads a subset; later phases add fields without changing Load()'s signature.
type Config struct {
	// HTTPAddr is the address the HTTP server listens on (default ":11434").
	HTTPAddr string
	// KiroCmd is the kiro-cli binary name or path (default "kiro-cli").
	KiroCmd string
	// KiroArgs is the list of arguments passed to kiro-cli (default ["acp"]).
	KiroArgs []string
	// KiroCWD is the working directory for the kiro-cli subprocess (default "").
	KiroCWD string
	// Debug enables debug-level logging (default false).
	Debug bool
	// PingInterval is the heartbeat interval for kiro-cli (default 60s).
	PingInterval time.Duration
}

// LogLevel returns the slog.Level implied by the Debug flag.
func (c Config) LogLevel() slog.Level {
	if c.Debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// Load reads environment variables and returns a validated Config.
// Returns a non-nil error if any env var is present but has an unparseable value.
// Missing env vars (empty string) use the documented default — only set-but-invalid values are errors.
func Load() (Config, error) {
	var errs []error

	httpAddr := getEnvStr("HTTP_ADDR", ":11434")
	kiroCmd := getEnvStr("KIRO_CMD", "kiro-cli")
	kiroArgs := getEnvStrSlice("KIRO_ARGS", []string{"acp"})
	kiroCWD := getEnvStr("KIRO_CWD", "")

	debug, err := getEnvBool("DEBUG", false)
	if err != nil {
		errs = append(errs, err)
	}

	pingInterval, err := getEnvDuration("PING_INTERVAL", 60*time.Second)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("config: invalid env vars: %w", errors.Join(errs...))
	}

	return Config{
		HTTPAddr:     httpAddr,
		KiroCmd:      kiroCmd,
		KiroArgs:     kiroArgs,
		KiroCWD:      kiroCWD,
		Debug:        debug,
		PingInterval: pingInterval,
	}, nil
}

// getEnvStr reads an env var, trims whitespace, and returns the default if empty.
func getEnvStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// getEnvStrSlice reads an env var and splits on whitespace.
// Returns def if the env var is empty.
func getEnvStrSlice(key string, def []string) []string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return strings.Fields(v)
}

// getEnvBool reads a boolean env var. Returns an error if the var is set but not parseable.
// Accepts "1", "true", "0", "false" (case-insensitive). TrimSpace handles Windows trailing-space bug.
func getEnvBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s: cannot parse %q as bool", key, v)
	}
}

// getEnvDuration reads a duration env var. Returns an error if the var is set but not parseable.
// Accepts both millisecond integers (Node compat: "60000" → 60s) and Go duration strings ("60s").
func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	// Accept milliseconds for Node compat (PING_INTERVAL=60000 means 60s).
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Duration(ms) * time.Millisecond, nil
	}
	// Accept Go duration strings (e.g., "60s", "1m").
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	return 0, fmt.Errorf("%s: cannot parse %q as duration (expected integer ms or Go duration string)", key, v)
}
