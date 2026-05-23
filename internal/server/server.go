// Package server provides the HTTP server for the Loop24 gateway.
// It wires the chi router, middleware chain, and request handlers.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"loop24-gateway/internal/config"
)

// Server wraps the chi router and HTTP server with structured logging.
type Server struct {
	cfg     config.Config
	logger  *slog.Logger
	router  chi.Router
	version string
	commit  string
	start   time.Time
}

// New constructs a Server, registers middleware and routes, and returns it ready to serve.
// Middleware is registered in order: RequestID → Recoverer → accessLog (D-13).
// RequestID MUST be first — accessLog reads the request ID it sets (Pitfall 5).
func New(cfg config.Config, logger *slog.Logger, version string) *Server {
	return NewWithCommit(cfg, logger, version, "unknown")
}

// NewWithCommit is like New but also accepts a commit hash for the version endpoint.
func NewWithCommit(cfg config.Config, logger *slog.Logger, version, commit string) *Server {
	s := &Server{
		cfg:     cfg,
		logger:  logger,
		version: version,
		commit:  commit,
		start:   time.Now(),
	}
	s.router = chi.NewRouter()

	// Middleware order is non-negotiable: RequestID first, then Recoverer, then accessLog.
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.Recoverer)
	s.router.Use(accessLog(logger))

	s.router.Get("/health", s.healthHandler)
	s.router.Get("/api/version", s.versionHandler)

	return s
}

// ServeHTTP implements http.Handler, delegating to the chi router.
// This enables direct handler testing with httptest.NewRecorder without starting a listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Run starts the HTTP server and blocks until ctx is cancelled.
// When ctx is done, it calls http.Server.Shutdown with a 30-second deadline (D-16).
// This is the testable lifecycle method — tests cancel the context to verify shutdown.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.HTTPAddr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second, // mitigates Slowloris (gosec G112)
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", s.cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server: listen: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("context cancelled; shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	return nil
}

// RunUntilSignal starts the HTTP server and blocks until SIGINT, SIGTERM, or ctx cancellation.
// It is a thin wrapper around Run that wires OS signal handling.
// D-22: the binary stays foreground-only — no start/stop subcommands.
func (s *Server) RunUntilSignal(ctx context.Context) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	derivedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-sigCh:
			s.logger.Info("shutdown signal received")
			cancel()
		case <-derivedCtx.Done():
		}
	}()

	return s.Run(derivedCtx)
}
