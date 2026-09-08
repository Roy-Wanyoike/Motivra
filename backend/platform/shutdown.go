package platform

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"
)

// Graceful blocks until SIGTERM/SIGINT, then shuts the HTTP server down
// within timeout and runs cleanup functions in reverse registration order
// (metrics and tracing flush last). It returns the first cleanup error, if
// any, after running every cleanup.
func Graceful(srv *http.Server, logger *slog.Logger, timeout time.Duration, cleanups ...func(context.Context) error) error {
	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	<-stopCtx.Done()
	logger.Info("shutdown_signal_received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var serverErr error
	if srv != nil {
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr = err
			logger.Error("server_shutdown_failed", "error", err)
		}
	}

	var firstErr error
	for i := len(cleanups) - 1; i >= 0; i-- {
		if err := cleanups[i](shutdownCtx); err != nil {
			logger.Error("cleanup_failed", "step", i, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if serverErr != nil {
		return serverErr
	}
	logger.Info("shutdown_complete")
	return firstErr
}
