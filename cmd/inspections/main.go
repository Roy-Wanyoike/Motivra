// Command inspections runs the Motivra Inspections service: the template
// registry per vehicle class, template-driven inspection sessions, the
// GREEN/AMBER/RED findings model, evidence provenance (content hashes) and
// append-only verifiable reports. Boot follows the cmd/template
// conventions: config, logger, tracing, metrics, PostgreSQL with the
// domain's own migration chain, JWT validation and graceful shutdown.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/inspections"
	inspectionsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/inspections"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

func main() {
	cfg := platform.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	// Inspections owns the authoritative report assembly and evidence
	// provenance; like the jobs service it cannot boot without its
	// database.
	if cfg.Database.URL == "" {
		slog.Error("configuration invalid: MOTIVRA_DATABASE_URL is required for the inspections service")
		os.Exit(1)
	}
	logger := platform.NewLogger(cfg.App)
	ctx := context.Background()

	stopTracing, err := platform.SetupTracing(ctx, cfg.OTel, cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("tracing setup failed", "error", err)
		os.Exit(1)
	}
	metrics, err := platform.SetupMetrics(cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("metrics setup failed", "error", err)
		os.Exit(1)
	}

	pool, err := platform.NewPostgres(ctx, cfg.Database.URL)
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	if err := platform.MigrateUp(ctx, pool, inspectionsmigrations.FS, "schema_migrations_inspections"); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	// Domain instruments register on the same registry the platform RED
	// metrics live on, so /metrics scrapes them together (ADR-0005).
	domainMetrics := inspections.NewMetrics(metrics.Registry())

	// Events publish on JetStream when MOTIVRA_NATS_URL is set (issue #28,
	// deferral 2); without it the service runs with a nil publisher and
	// inspections.NewService skips event publishing — local and test
	// deployments stay broker-free.
	var publisher *platform.NATSPublisher
	if cfg.NATS.URL != "" {
		publisher, err = platform.NewNATSPublisher(ctx, cfg.NATS.URL)
		if err != nil {
			logger.Error("nats unavailable", "error", err)
			os.Exit(1)
		}
	}
	svc := inspections.NewService(inspections.NewPostgresStore(pool), publisher, domainMetrics)

	// Token verification (ADR-0007, issue #87): public keys only — JWKS
	// from the identity service by default, or a distributed key file. The
	// legacy HS256 secret verifies tokens only behind the explicit
	// MOTIVRA_JWT_ALLOW_HS256 migration flag.
	validator, err := platform.NewValidatorFromConfig(cfg)
	if err != nil {
		logger.Error("token validator unavailable", "error", err)
		os.Exit(1)
	}
	srv := platform.NewServer(cfg, logger,
		platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		// JWKS readiness: a verifier that cannot load the issuer key set
		// refuses every token — surface that on /readyz (ADR-0007).
		platform.WithChecker("jwks", validator.ReadyCheck),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
		platform.WithMiddleware(metrics.HTTP.Middleware),
	)
	inspections.Routes(srv.Router, svc, platform.RequireAuthenticated)

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
	stopMetricsSrv, err := platform.StartMetricsServer(cfg.MetricsAddr, metrics.Handler)
	if err != nil {
		logger.Error("metrics listener unavailable", "error", err, "addr", cfg.MetricsAddr)
		os.Exit(1)
	}
	cleanups = append(cleanups, stopMetricsSrv, metrics.Stop, stopTracing)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, cleanups...); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
