// Package auth provides bearer-token + IP-allowlist chi-compatible HTTP
// middlewares for the OTTO Gateway.
//
// The two factory functions (Bearer, IPAllowlist) are constructed against a
// shared Config and emit Ollama-shape JSON errors ({"error": "..."}) on
// rejection — matching the Node reference implementation byte-for-byte so
// existing clients see no behavioural drift after the port.
//
// Per Phase 2 D-14: empty Config.Tokens means "auth disabled" (the Node
// default when AUTH_TOKEN is unset); empty Config.AllowedPrefixes means
// "allow all" (Node default when ALLOWED_IPS is unset).
package auth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
)

// Config carries the shared dependencies for the Bearer and IPAllowlist
// middleware factories. Zero-value fields produce a passthrough (no auth,
// allow-all) middleware so callers can construct a Config from env vars
// without branching on "feature enabled" booleans.
type Config struct {
	// Logger is optional; middlewares do not log on the success path
	// (access logging is owned by the chi accessLog middleware that runs
	// BEFORE this auth sub-router — see Pitfall 2 in RESEARCH.md).
	Logger *slog.Logger

	// Tokens is the set of valid bearer tokens. An empty slice disables
	// authentication entirely, matching the Node reference behaviour when
	// AUTH_TOKEN is unset.
	Tokens []string

	// AllowedPrefixes is the set of CIDR / single-IP prefixes that may
	// reach the gateway. An empty slice allows all clients, matching the
	// Node reference behaviour when ALLOWED_IPS is unset.
	AllowedPrefixes []netip.Prefix

	// TrustXForwardedFor controls whether IPAllowlist consults the X-Forwarded-For header when extracting the client IP.
	// Default false: X-Forwarded-For is NOT trusted (correct for laptop deployments with no proxy in front — a localhost client can set XFF: 127.0.0.1
	// and bypass ALLOWED_IPS otherwise; see Codex review H-7 / T-02-08). Set true ONLY when a known reverse proxy is in front of the gateway.
	TrustXForwardedFor bool
}

// writeOllamaError emits the canonical {"error": "<msg>"} JSON body used by
// the Node reference for both auth and IP-allowlist rejections. It sets the
// Content-Type header BEFORE WriteHeader (the order matters — once the
// status line is flushed, header mutations are silently dropped).
func writeOllamaError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoder errors here mean the client disconnected mid-write; the
	// response headers are already flushed, so there is nothing useful to
	// recover. Discard explicitly to satisfy errcheck.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
