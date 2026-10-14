// Command identity is the Motivra identity service: authentication
// (register/login/refresh/logout), user profiles, admin account listing and
// HS256 access-token issuance over the identity domain's PostgreSQL tables.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/identity"
	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
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
	metrics, err := platform.SetupMetrics(cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("metrics setup failed", "error", err)
		os.Exit(1)
	}

	// Identity cannot serve authentication without its store; require the
	// database in every environment.
	if cfg.Database.URL == "" {
		logger.Error("identity service requires MOTIVRA_DATABASE_URL")
		os.Exit(1)
	}
	pool, err := platform.NewPostgres(ctx, cfg.Database.URL)
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	if err := platform.MigrateUp(ctx, pool, identitymigrations.FS, "schema_migrations_identity"); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	// Events publish on JetStream when MOTIVRA_NATS_URL is set (issue #28,
	// deferral 2); without it the service runs with a nil publisher and
	// identity.NewService skips event publishing — local and test
	// deployments stay broker-free.
	var publisher *platform.NATSPublisher
	if cfg.NATS.URL != "" {
		publisher, err = platform.NewNATSPublisher(ctx, cfg.NATS.URL)
		if err != nil {
			logger.Error("nats unavailable", "error", err)
			os.Exit(1)
		}
	}

	store := identity.NewPostgresStore(pool)

	// Breached-password check (ADR-0004, issue #86): the local denylist
	// always applies; HIBP k-anonymity joins the chain when
	// MOTIVRA_HIBP_ENABLED is set. An unreadable denylist file refuses
	// boot — an operator-provided corpus must not silently vanish.
	breachCfg := identity.LoadBreachConfig()
	breacher, err := identity.NewBreacher(breachCfg)
	if err != nil {
		logger.Error("breach denylist unavailable", "error", err)
		os.Exit(1)
	}

	// Email/phone verification flow (issue #86): grace window, token TTL,
	// resend limits and the dev-only token echo, validated before boot.
	verificationCfg := identity.LoadVerificationConfig()
	if err := verificationCfg.Validate(cfg.App.Env); err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(1)
	}

	issuer := identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
		identity.WithVerificationGrace(verificationCfg.Grace))
	svc := identity.NewService(store, issuer, publisher,
		identity.WithTrustForwarded(cfg.TrustForwarded),
		identity.WithPasswordBreacher(breacher, breachCfg.FailClosed),
		identity.WithVerification(verificationCfg),
	)
	// Per-IP throttle on the public auth endpoints (0 disables; off in tests).
	var authLimiter *platform.RateLimiter
	if cfg.AuthRateLimitRPM > 0 {
		authLimiter = platform.NewRateLimiter(cfg.AuthRateLimitRPM)
	}

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

	validator := platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	srv := platform.NewServer(cfg, logger,
		platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
		platform.WithMiddleware(metrics.HTTP.Middleware),
	)
	identity.Routes(srv.Router, svc, authLimiter)

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
