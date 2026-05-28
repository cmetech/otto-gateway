package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Bearer returns a chi-compatible middleware that validates the inbound
// bearer credential against cfg.Tokens using
// crypto/subtle.ConstantTimeCompare (defends against timing side-channel
// token recovery — see threat T-02-05 + T-3.1-AUTH).
//
// Header precedence (Phase 3.1 D-15): the middleware tries
// `Authorization: Bearer <token>` FIRST and falls back to `x-api-key:
// <token>` ONLY when the Authorization header is absent or non-Bearer.
// Authorization wins when both are present — a request with a bad
// Bearer alongside a stolen x-api-key is rejected. The extraction is
// delegated to ExtractToken (declared below) so the contract has a
// single source of truth that the middleware, the package's whitebox
// tests, AND the per-surface adapter handlers (Phase 8 Plan 08-02
// Task 4) exercise. The helper was renamed from `extractToken` to
// `ExtractToken` in slice 08-02 so the three adapter packages can call
// it directly when stamping the bearer credential onto ctx for the
// canonical-layer AuthHook.
//
// When cfg.Tokens is empty (len == 0), the middleware is a passthrough
// (matches the Node reference behaviour when AUTH_TOKEN is unset, per D-14).
//
// On rejection, the response is HTTP 401 with body
// `{"error": "Invalid or missing API key"}` — the exact shape the Node
// reference emits at acp-ollama-server.js:701. The envelope is surface-
// agnostic; the Anthropic adapter mounts the SAME middleware (D-15
// "one middleware, one mental model") and accepts the Ollama-shape
// 401 body per RESEARCH.md Pattern 3 option 2.
func Bearer(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(cfg.Tokens) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			provided := ExtractToken(r)
			if provided == "" {
				writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
				return
			}

			providedBytes := []byte(provided)
			for _, valid := range cfg.Tokens {
				// Note: this loop is NOT constant-time across the token list
				// (early exit on match leaks "how many tokens are configured",
				// not token bytes). Acceptable per RESEARCH.md Pattern 3.
				if subtle.ConstantTimeCompare(providedBytes, []byte(valid)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
		})
	}
}

// ExtractToken implements the D-15 dual-header precedence contract:
// try `Authorization: Bearer <token>` first, fall back to `x-api-key:
// <token>` only when Authorization is absent or non-Bearer. Returns
// the empty string when neither path yields a credential. The scheme
// token is matched case-insensitively per RFC 7235 §2.1 / RFC 6750
// (so "Bearer", "bearer", "BEARER", "BeArEr" are all accepted).
//
// Precedence is deliberately Authorization-wins: when BOTH headers are
// present the Bearer value is used unconditionally. This blocks a
// downgrade attack where the attacker supplies a bad Bearer alongside
// a stolen x-api-key — the bad Bearer is evaluated first and the
// request is rejected. The fallback path is consulted ONLY when
// Authorization adds no credential (absent or non-Bearer scheme).
// Crucially, the case-insensitive scheme match ensures the precedence
// holds for ALL RFC-valid spellings — a downgrade attacker cannot
// bypass the guard by lowercasing the scheme.
//
// EXPORTED in Phase 8 Plan 08-02 Task 4 so the per-surface adapter
// handlers (ollama, openai) can extract the bearer credential and
// stamp it onto ctx via canonical.WithBearerToken for the
// canonical-layer AuthHook to consume. The Anthropic adapter handles
// its own dual-header precedence (x-api-key first per the surface's
// SDK convention) and calls ExtractToken only for the Bearer fallback.
//
// The package-private auth_internal_test.go pins this contract with
// table-driven cases; TestBearer_DualHeader in auth_test.go validates
// the same contract end-to-end through the middleware.
func ExtractToken(r *http.Request) string {
	const bearerPrefix = "Bearer "
	authHeader := r.Header.Get("Authorization")
	// RFC 7235 §2.1 + RFC 6750: the auth-scheme token is case-insensitive.
	// The len(authHeader) >= len(bearerPrefix) guard prevents an
	// out-of-bounds slice when the header is shorter than the prefix
	// (including the empty / absent case — r.Header.Get returns "").
	if len(authHeader) >= len(bearerPrefix) &&
		strings.EqualFold(authHeader[:len(bearerPrefix)], bearerPrefix) {
		return authHeader[len(bearerPrefix):]
	}
	return r.Header.Get("x-api-key")
}
