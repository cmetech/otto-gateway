package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Bearer returns a chi-compatible middleware that validates the
// `Authorization: Bearer <token>` header against cfg.Tokens using
// crypto/subtle.ConstantTimeCompare (defends against timing side-channel
// token recovery — see threat T-02-05).
//
// When cfg.Tokens is empty (len == 0), the middleware is a passthrough
// (matches the Node reference behaviour when AUTH_TOKEN is unset, per D-14).
//
// On rejection, the response is HTTP 401 with body
// `{"error": "Invalid or missing API key"}` — the exact shape the Node
// reference emits at acp-ollama-server.js:701.
func Bearer(cfg Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(cfg.Tokens) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
				return
			}

			provided := []byte(authHeader[len(prefix):])
			for _, valid := range cfg.Tokens {
				// Note: this loop is NOT constant-time across the token list
				// (early exit on match leaks "how many tokens are configured",
				// not token bytes). Acceptable per RESEARCH.md Pattern 3.
				if subtle.ConstantTimeCompare(provided, []byte(valid)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			writeOllamaError(w, http.StatusUnauthorized, "Invalid or missing API key")
		})
	}
}
