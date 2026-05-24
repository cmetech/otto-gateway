package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// IPAllowlist returns a chi-compatible middleware that gates requests on
// client IP. When cfg.AllowedPrefixes is empty, the returned factory is the
// identity function (matches the Node reference allow-all default when
// ALLOWED_IPS is unset, per D-14).
//
// SECURITY NOTE (Codex H-7 / threat T-02-06): X-Forwarded-For is NOT trusted
// unless cfg.TrustXForwardedFor is true. The Loop24 deployment model is
// laptop-local (Assumption A3) — a process on localhost can set
// `X-Forwarded-For: 127.0.0.1` and bypass an allowlist that honors XFF
// blindly. Operators opt in to XFF trust ONLY when a known reverse proxy
// fronts the gateway.
func IPAllowlist(cfg Config) func(http.Handler) http.Handler {
	if len(cfg.AllowedPrefixes) == 0 {
		// Allow-all fast path: do not even wrap the handler.
		return func(next http.Handler) http.Handler { return next }
	}

	// Capture trust flag at construction time; the per-request closure
	// reads from this captured value (no globals, no per-request cfg lookup).
	trustXFF := cfg.TrustXForwardedFor

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP, ok := extractClientIP(r, trustXFF)
			if !ok {
				writeOllamaError(w, http.StatusForbidden, "could not determine client IP")
				return
			}

			for _, prefix := range cfg.AllowedPrefixes {
				if prefix.Contains(clientIP) {
					next.ServeHTTP(w, r)
					return
				}
			}

			writeOllamaError(w, http.StatusForbidden, "IP "+clientIP.String()+" not in allowlist")
		})
	}
}

// extractClientIP resolves the client IP from the request. When trustXFF is
// true and X-Forwarded-For is present and parseable, the first hop wins.
// Otherwise (trustXFF false, or XFF empty / malformed), the function falls
// back to r.RemoteAddr. In both paths the IPv4-in-IPv6 `::ffff:` mapping
// prefix is stripped before parsing (Go's dual-stack socket emits e.g.
// `[::ffff:127.0.0.1]:12345` for IPv4 connections; threat T-02-07).
func extractClientIP(r *http.Request, trustXFF bool) (netip.Addr, bool) {
	if trustXFF {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
			first = strings.TrimPrefix(first, "::ffff:")
			if addr, err := netip.ParseAddr(first); err == nil {
				return addr, true
			}
			// Malformed XFF: fall through to RemoteAddr (keeps the opt-in
			// path safe against pathological headers).
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	host = strings.TrimPrefix(host, "::ffff:")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}
