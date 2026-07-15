//go:build darwin || windows

package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// remoteWriteConfig is the tray's Grafana Cloud remote-write configuration,
// assembled from GW_METRICS_REMOTE_WRITE_* (+ GW_METRICS_SERIES_PREFIXES) each
// cycle. Token is a secret: it is held only in memory, never written to
// tray.json, and never logged (see scrubToken).
type remoteWriteConfig struct {
	URL        string        // GW_METRICS_REMOTE_WRITE_URL — Grafana /api/prom/push
	User       string        // GW_METRICS_REMOTE_WRITE_USER — instance/username
	Token      string        // GW_METRICS_REMOTE_WRITE_TOKEN — API token (secret)
	Interval   time.Duration // GW_METRICS_REMOTE_WRITE_INTERVAL_SEC (default 30s, min 5s)
	Prefixes   []string      // GW_METRICS_SERIES_PREFIXES allowlist (default gw_,process_)
	EnvEnabled bool          // GW_METRICS_REMOTE_WRITE_ENABLED — the env-level default
}

const (
	defaultRemoteWriteInterval = 30 * time.Second
	minRemoteWriteInterval     = 5 * time.Second
)

// defaultSeriesPrefixes is the built-in allowlist: gateway app + worker metrics
// and the process CPU/mem collectors, but not the go_* runtime internals — keeps
// the Grafana Cloud active-series footprint (and bill) lean.
var defaultSeriesPrefixes = []string{"gw_", "process_"}

// dotenvLookup returns a key→value resolver with the same precedence the rest of
// the tray uses: $GW_HOME/overrides.env, then $GW_HOME/.env, then the process
// environment. Both dotenv files are read once here so the returned closure does
// no further I/O per key.
func dotenvLookup(gwHome string) func(string) string {
	over, _ := readDotenvFile(gwOverridesPath(gwHome))
	env, _ := readDotenvFile(gwEnvPath(gwHome))
	return func(key string) string {
		if v, ok := over[key]; ok {
			return v
		}
		if v, ok := env[key]; ok {
			return v
		}
		return os.Getenv(key)
	}
}

// loadRemoteWriteConfig reads the remote-write settings fresh, so edits to
// overrides.env/.env (endpoint, token, interval, prefixes, env-enable) take
// effect on the next cycle without a tray restart.
func loadRemoteWriteConfig(gwHome string) remoteWriteConfig {
	get := dotenvLookup(gwHome)
	return remoteWriteConfig{
		URL:        strings.TrimSpace(get("GW_METRICS_REMOTE_WRITE_URL")),
		User:       strings.TrimSpace(get("GW_METRICS_REMOTE_WRITE_USER")),
		Token:      strings.TrimSpace(get("GW_METRICS_REMOTE_WRITE_TOKEN")),
		Interval:   parseIntervalSeconds(get("GW_METRICS_REMOTE_WRITE_INTERVAL_SEC")),
		Prefixes:   parseSeriesPrefixes(get("GW_METRICS_SERIES_PREFIXES")),
		EnvEnabled: parseEnvBool(get("GW_METRICS_REMOTE_WRITE_ENABLED")),
	}
}

// ready reports whether enough is configured to attempt a push. A missing
// endpoint/user/token is not an error — it just means the operator has not set
// remote-write up, so the writer no-ops.
func (c remoteWriteConfig) ready() bool {
	return c.URL != "" && c.User != "" && c.Token != ""
}

// parseIntervalSeconds parses a whole-second interval, defaulting to 30s and
// clamping up to a 5s floor so a fat-fingered "1" cannot hammer the gateway or
// Grafana.
func parseIntervalSeconds(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultRemoteWriteInterval
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultRemoteWriteInterval
	}
	d := time.Duration(n) * time.Second
	if d < minRemoteWriteInterval {
		return minRemoteWriteInterval
	}
	return d
}

// parseSeriesPrefixes parses the comma-separated allowlist; empty ⇒ the default.
// An explicit "*" (or "all") disables filtering (send everything).
func parseSeriesPrefixes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSeriesPrefixes
	}
	if raw == "*" || strings.EqualFold(raw, "all") {
		return nil // nil ⇒ no filtering
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return defaultSeriesPrefixes
	}
	return out
}

// parseEnvBool treats 1/true/yes/on (case-insensitive) as true; everything else
// (including empty/unset) is false.
func parseEnvBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// allowedSeriesName reports whether a metric family name passes the prefix
// allowlist. A nil/empty allowlist passes everything.
func allowedSeriesName(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
