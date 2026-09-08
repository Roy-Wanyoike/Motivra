// Command template is the reference wiring for every Motivra backend
// service: config, logger, tracing, metrics, PostgreSQL (optional at boot),
// JetStream publisher, JWT validation, health/readiness and graceful
// shutdown. Copy this directory when creating a new service.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/jackc/pgx/v5/pgxpool"
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

	var pool *pgxpool.Pool
	var publisher *platform.NATSPublisher
	var cleanups []func(context.Context) error

	opts := []platform.ServerOption{platform.WithMount("/metrics", metricsHandler)}

	if cfg.Database.URL != "" {
		pool, err = platform.NewPostgres(ctx, cfg.Database.URL)
		if err != nil {
			logger.Error("database unavailable", "error", err)
			os.Exit(1)
		}
		opts = append(opts, platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}))
		cleanups = append(cleanups, func(context.Context) error { pool.Close(); return nil })
	}
	if cfg.NATS.URL != "" {
		publisher, err = platform.NewNATSPublisher(ctx, cfg.NATS.URL)
		if err != nil {
			logger.Error("nats unavailable", "error", err)
			os.Exit(1)
		}
		cleanups = append(cleanups, func(context.Context) error { publisher.Close(); return nil })
	}
	cleanups = append(cleanups, stopMetrics, stopTracing)

	validator := platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	opts = append(opts, platform.WithMiddleware(platform.AuthMiddleware(validator)))
	srv := platform.NewServer(cfg, logger, opts...)
	srv.Router.Get("/v1/ping", platform.RequireAuthenticated(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := platform.ClaimsFromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(platform.LogJSON(map[string]string{"pong": claims.UserID})))
	}))

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
