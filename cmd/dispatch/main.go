// Command dispatch runs the Motivra Dispatch Scoring service: the
// stateless, explainable multi-factor scoring engine exposed over HTTP
// (issue #19). Boot follows the cmd/template conventions: config, logger,
// tracing, metrics, JWT validation, health/readiness and graceful
// shutdown. No database or broker is required: the engine is a pure
// function and holds no state, so the "engine" readiness checker always
// reports ready.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/dispatch"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

func main() {
	cfg := platform.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	logger := platform.NewLogger(cfg.App)
	ctx := context.Background()

	stopTracing, err := platform.SetupTracing(ctx, cfg.OTel, cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("tracing setup failed", "error", err)
		os.Exit(1)
	}
	metricsHandler, stopMetrics, err := platform.SetupMetrics(cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("metrics setup failed", "error", err)
		os.Exit(1)
	}

	cleanups := []func(context.Context) error{stopMetrics, stopTracing}

	validator := platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	srv := platform.NewServer(cfg, logger,
		platform.WithMount("/metrics", metricsHandler),
		platform.WithChecker("engine", func(context.Context) error { return nil }),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
	)
	dispatch.Routes(srv.Router, func(h http.HandlerFunc) http.HandlerFunc {
		return platform.RequireRole(dispatch.DispatcherRoles, h)
	})

	httpSrv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("service_listening", "port", cfg.App.Port, "service", cfg.App.Name)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, cleanups...); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
