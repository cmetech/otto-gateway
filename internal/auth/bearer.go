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
// delegated to extractToken (declared below) so the contract has a
// single source of truth that both the middleware and the package's
// whitebox tests exercise.
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

			provided := extractToken(r)
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

// extractToken implements the D-15 dual-header precedence contract:
// try `Authorization: Bearer <token>` first, fall back to `x-api-key:
// <token>` only when Authorization is absent or non-Bearer. Returns
// the empty string when neither path yields a credential.
//
// Precedence is deliberately Authorization-wins: when BOTH headers are
// present the Bearer value is used unconditionally. This blocks a
// downgrade attack where the attacker supplies a bad Bearer alongside
// a stolen x-api-key — the bad Bearer is evaluated first and the
// request is rejected. The fallback path is consulted ONLY when
// Authorization adds no credential (absent or non-Bearer scheme).
//
// The package-private auth_internal_test.go pins this contract with
// table-driven cases; TestBearer_DualHeader in auth_test.go validates
// the same contract end-to-end through the middleware.
func extractToken(r *http.Request) string {
	const bearerPrefix = "Bearer "
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, bearerPrefix) {
		return authHeader[len(bearerPrefix):]
	}
	return r.Header.Get("x-api-key")
}
