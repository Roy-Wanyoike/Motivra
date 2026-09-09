// Command vehicles runs the Motivra Vehicle Identity service: the vehicle
// registry, the append-only service history and the read-only Vehicle
// Passport API. Boot follows the cmd/template conventions: config, logger,
// tracing, metrics, PostgreSQL with the domain's own migration chain, JWT
// validation and graceful shutdown.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	vehiclesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/vehicles"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/Roy-Wanyoike/Motivra/backend/vehicles"
)

func main() {
	cfg := platform.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	// The registry is the system of record for vehicle identity; unlike the
	// template it cannot boot without its database.
	if cfg.Database.URL == "" {
		slog.Error("configuration invalid: MOTIVRA_DATABASE_URL is required for the vehicles service")
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
	if err := platform.MigrateUp(ctx, pool, vehiclesmigrations.FS, "schema_migrations_vehicles"); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	// Events publish on JetStream when MOTIVRA_NATS_URL is set (issue #28,
	// deferral 2); without it the service runs with a nil publisher and
	// vehicles.NewService skips event publishing — local and test
	// deployments stay broker-free.
	var publisher *platform.NATSPublisher
	if cfg.NATS.URL != "" {
		publisher, err = platform.NewNATSPublisher(ctx, cfg.NATS.URL)
		if err != nil {
			logger.Error("nats unavailable", "error", err)
			os.Exit(1)
		}
	}
	svc := vehicles.NewService(vehicles.NewPostgresStore(pool), publisher)

	validator := platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	srv := platform.NewServer(cfg, logger,
		platform.WithMount("/metrics", metricsHandler),
		platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
	)
	vehicles.Routes(srv.Router, svc, platform.RequireAuthenticated)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("service_listening", "port", cfg.App.Port, "service", cfg.App.Name)

	cleanups := []func(context.Context) error{
		func(context.Context) error { pool.Close(); return nil },
	}
	if publisher != nil {
		cleanups = append(cleanups, func(context.Context) error { publisher.Close(); return nil })
	}
	cleanups = append(cleanups, stopMetrics, stopTracing)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, cleanups...); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
