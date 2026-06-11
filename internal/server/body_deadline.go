package server

import (
	"net/http"
	"time"
)

// chatBodyDeadlinePaths is the static set of paths that receive a
// per-request body-read deadline (REL-HTTP-04 / D-04a). Path matching is
// exact — chi sub-router prefixes have already been resolved by the
// time the middleware runs (req.URL.Path is the full request path).
//
// Admin POSTs (/admin/config/…), catalog endpoints (/api/tags,
// /api/show, /api/ps, /api/pull, /api/push, /api/create, /api/copy,
// /api/delete), and the OpenAI GET /v1/models endpoint are
// intentionally absent — their bodies are small/empty and the stall
// failure mode does not appear in the v1.9 reliability review.
var chatBodyDeadlinePaths = map[string]struct{}{
	"/v1/chat/completions": {},
	"/v1/messages":         {},
	"/v1/completions":      {},
	"/v1/embeddings":       {},
	"/api/chat":            {},
	"/api/generate":        {},
	"/api/embed":           {},
	"/api/embeddings":      {},
}

// withBodyReadDeadline returns a chi middleware that arms a per-request
// time.AfterFunc timer for chat-body POSTs in chatBodyDeadlinePaths.
// When the timer fires it calls r.Body.Close() — the next Read on
// r.Body returns an error, unblocking io.ReadAll / json.Decode on the
// handler goroutine (REL-HTTP-04 / D-04b).
//
// Critical design property: the timer ONLY closes the request body. It
// does NOT cancel the request context and does NOT touch the
// http.ResponseWriter. This is what keeps long SSE response writes
// unbounded — the deadline scopes to the body-read phase only.
//
// The timer is Stop()'d when the handler returns (whether the body was
// fully read, the deadline fired, or the handler short-circuited). The
// time.AfterFunc goroutine is reclaimed by Stop() (or by Close itself
// — Close is idempotent on Go's *Request.Body wrappers).
//
// Zero or negative timeout disables the wrapper (no timer armed). Plan
// 16-05's config.Load() already rejects <= 0 at boot, but the explicit
// guard here keeps the middleware safe when the test path constructs a
// server.Config with a zero BodyReadTimeout.
//
// Method scope: GET / HEAD / OPTIONS requests skip the wrapper because
// they carry no body. POST / PUT / PATCH / DELETE on a deadline path
// arm the timer.
func withBodyReadDeadline(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if timeout <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			// Path scope: only chat-body POSTs.
			if _, ok := chatBodyDeadlinePaths[r.URL.Path]; !ok {
				next.ServeHTTP(w, r)
				return
			}
			// Method scope: bodies only travel on write methods.
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				// fall through to arm the timer
			default:
				next.ServeHTTP(w, r)
				return
			}
			// Arm the body-read deadline. AfterFunc returns a *Timer
			// whose Stop method reclaims the underlying goroutine if
			// the timer has not yet fired.
			timer := time.AfterFunc(timeout, func() {
				// Close the body to unblock any parked io.Read on
				// the handler goroutine. Idempotent — *http.Request
				// body wrappers swallow double Close.
				_ = r.Body.Close()
			})
			defer timer.Stop()
			next.ServeHTTP(w, r)
		})
	}
}
