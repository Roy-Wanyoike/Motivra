// Command dispatch runs the Motivra Dispatch Scoring service: the
// stateless, explainable multi-factor scoring engine exposed over HTTP
// (issue #19). Boot follows the cmd/template conventions: config, logger,
// tracing, metrics, JWT validation, health/readiness and graceful
// shutdown. The engine itself holds no state; when MOTIVRA_DATABASE_URL
// is set, every successful scoring decision is appended to the platform's
// write-once audit trail (decision records, issue #88) — without a
// database the engine still scores, records nothing, and says so at boot.
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
	metrics, err := platform.SetupMetrics(cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("metrics setup failed", "error", err)
		os.Exit(1)
	}

	stopMetricsSrv, err := platform.StartMetricsServer(cfg.MetricsAddr, metrics.Handler)
	if err != nil {
		logger.Error("metrics listener unavailable", "error", err, "addr", cfg.MetricsAddr)
		os.Exit(1)
	}
	cleanups := []func(context.Context) error{stopMetricsSrv, metrics.Stop, stopTracing}

	// Decision records (issue #88): with a database, every successful
	// scoring decision is appended to the shared write-once audit_log
	// trail — queryable through the admin audit API. A failed record
	// fails the scoring request (ADR-0004: audit signals are never lost
	// silently). Without MOTIVRA_DATABASE_URL the engine scores without
	// persistence; production deployments provision the database.
	var recorder dispatch.DecisionRecorder
	var dbReady platform.Checker
	if cfg.Database.URL != "" {
		pool, poolErr := platform.NewPostgres(ctx, cfg.Database.URL)
		if poolErr != nil {
			logger.Error("database unavailable", "error", poolErr)
			os.Exit(1)
		}
		cleanups = append(cleanups, func(context.Context) error { pool.Close(); return nil })
		recorder = dispatch.NewPostgresDecisionRecorder(pool)
		dbReady = pool.Ping
	} else {
		logger.Warn("dispatch decision records disabled: MOTIVRA_DATABASE_URL is not set (scoring works, nothing is audited)")
	}

	// Token verification (ADR-0007, issue #87): public keys only — JWKS
	// from the identity service by default, or a distributed key file. The
	// legacy HS256 secret verifies tokens only behind the explicit
	// MOTIVRA_JWT_ALLOW_HS256 migration flag.
	validator, err := platform.NewValidatorFromConfig(cfg)
	if err != nil {
		logger.Error("token validator unavailable", "error", err)
		os.Exit(1)
	}
	serverOpts := []platform.ServerOption{
		platform.WithChecker("engine", func(context.Context) error { return nil }),
		// JWKS readiness: a verifier that cannot load the issuer key set
		// refuses every token — surface that on /readyz (ADR-0007).
		platform.WithChecker("jwks", validator.ReadyCheck),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
		platform.WithMiddleware(metrics.HTTP.Middleware),
	}
	// Decision-record readiness: when the audit trail's database is
	// wired, an unreachable database means decisions are failing (the
	// recorder fails the requests) — surface that on /readyz.
	if dbReady != nil {
		serverOpts = append(serverOpts, platform.WithChecker("database", dbReady))
	}
	srv := platform.NewServer(cfg, logger, serverOpts...)
	// The recorder is injected by value below: when a database is wired,
	// a failed decision record fails the scoring request; without one,
	// Routes runs in the no-persistence posture (boot warning above).
	dispatch.Routes(srv.Router, func(h http.HandlerFunc) http.HandlerFunc {
		return platform.RequireRole(dispatch.DispatcherRoles, h)
	}, recorder)

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
