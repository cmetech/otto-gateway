package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// loggerKey is the unexported context key for the per-request logger.
// Using a private struct type prevents key collisions with other packages.
type loggerKey struct{}

// accessLog returns a chi middleware that emits one structured JSON log line
// per request with request_id, method, path, status, and duration_ms.
//
// IMPORTANT: middleware.RequestID MUST be registered before this middleware
// in the chi router chain. accessLog reads the request ID from the context
// key set by RequestID; registering accessLog first results in empty request_id.
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := middleware.GetReqID(r.Context())
			reqLogger := logger.With("request_id", reqID)

			// Stash per-request logger in context so handlers can retrieve it.
			ctx := context.WithValue(r.Context(), loggerKey{}, reqLogger)

			start := time.Now()
			// WrapResponseWriter captures the status code written by downstream handlers.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r.WithContext(ctx))

			reqLogger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// LoggerFromCtx retrieves the per-request logger stored by accessLog.
// Falls back to fallback if no logger is present in the context.
func LoggerFromCtx(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return fallback
}
