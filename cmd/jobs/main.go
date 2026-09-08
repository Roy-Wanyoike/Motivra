// Command jobs runs the Motivra Jobs service: service requests, the
// 18-status job state machine and technician assignments. Boot follows the
// cmd/template conventions: config, logger, tracing, metrics, PostgreSQL
// with the domain's own migration chain, JWT validation and graceful
// shutdown.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/jobs"
	jobsnextmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/jobs"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

func main() {
	cfg := platform.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	// Jobs owns the authoritative job state machine; like the vehicles
	// service it cannot boot without its database.
	if cfg.Database.URL == "" {
		slog.Error("configuration invalid: MOTIVRA_DATABASE_URL is required for the jobs service")
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

	pool, err := platform.NewPostgres(ctx, cfg.Database.URL)
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	if err := platform.MigrateUp(ctx, pool, jobsnextmigrations.FS, "schema_migrations_jobs"); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	// The JetStream publisher wiring lands with the notifications wave: the
	// service deliberately starts with a nil publisher, and jobs.NewService
	// skips event publishing until then. Swap in a platform.NATSPublisher
	// (and add its cleanup) when NATS_URL is provisioned for this service.
	svc := jobs.NewService(jobs.NewPostgresStore(pool), nil)

	validator := platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	srv := platform.NewServer(cfg, logger,
		platform.WithMount("/metrics", metricsHandler),
		platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
	)
	jobs.Routes(srv.Router, svc, platform.RequireAuthenticated)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("service_listening", "port", cfg.App.Port, "service", cfg.App.Name)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout,
		func(context.Context) error { pool.Close(); return nil },
		stopMetrics,
		stopTracing,
	); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
