// Package testutil provides shared test helpers for internal packages.
// Zero external dependencies — only stdlib + testing.
package testutil

import (
	"bytes"
	"log/slog"
	"testing"
)

type testWriter struct{ t *testing.T }

func (tw testWriter) Write(p []byte) (int, error) {
	tw.t.Log(string(bytes.TrimRight(p, "\n")))
	return len(p), nil
}

// Logger returns a *slog.Logger that routes JSON output to t.Log.
// Output is test-scoped: visible only when the test fails or -v is set.
// Use in every internal package test to avoid global slog state (D-15).
func Logger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewJSONHandler(testWriter{t}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
}
