// Command dispatch runs the Motivra Dispatch Scoring service: the
// stateless, explainable multi-factor scoring engine exposed over HTTP
// (issue #19).
//
// Boot is DEGRADED, never fatal (issue #122, ADR-0008): the listener binds
// first and /healthz answers 200 immediately; configuration and dependency
// failures become /readyz 503 blockers (retried with backoff) instead of
// os.Exit(1). Domain routes answer 503 problem+json until the wiring loop
// completes. The engine holds no state; when MOTIVRA_DATABASE_URL is set,
// every successful scoring decision is appended to the platform's
// write-once audit trail (decision records, issue #88) — the database then
// gates readiness, because a failed record fails the request (ADR-0004:
// audit signals are never lost silently). Without a database the engine
// still scores, records nothing, and says so at boot.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Roy-Wanyoike/Motivra/backend/dispatch"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

func main() {
	cfg := platform.Load()
	logger := platform.NewLogger(cfg.App)
	ctx := context.Background()
	boot := platform.NewBoot(cfg, logger)

	// Observability degrades, never exits (issue #122, ADR-0008).
	stopTracing, err := platform.SetupTracing(ctx, cfg.OTel, cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("tracing setup failed — serving degraded without trace export", "error", err)
		boot.Degraded("tracing", "tracing setup failed: "+err.Error())
	}
	metrics, err := platform.SetupMetrics(cfg.App.Name, cfg.App.Env)
	if err != nil {
		logger.Error("metrics setup failed — serving degraded without metrics", "error", err)
		metrics = nil
		boot.Degraded("metrics", "metrics setup failed: "+err.Error())
	}

	var (
		pool      *pgxpool.Pool
		validator atomic.Pointer[platform.JWTValidator]
	)
	// Decision records (issue #88): with a database wired, the recorder is
	// injected by value below and a failed record fails the scoring request.
	// Without one, Routes runs in the no-persistence posture (documented
	// degradation — the boot warning below).
	var recorder dispatch.DecisionRecorder

	srvOpts := []platform.ServerOption{
		platform.WithBoot(boot),
		platform.WithMiddleware(platform.AuthMiddlewareFunc(validator.Load)),
	}
	if metrics != nil {
		srvOpts = append(srvOpts, platform.WithMiddleware(metrics.HTTP.Middleware))
	}
	srv := platform.NewServer(cfg, logger, srvOpts...)

	steps := []platform.BootStep{
		{
			Name:      "config",
			Permanent: true,
			Run: func(context.Context) error {
				return aggregateProblems(cfg.ValidateProblems())
			},
		},
		{
			// Token verification (ADR-0007, issue #87): public keys only —
			// JWKS from the identity service by default, or a distributed key
			// file. The legacy HS256 secret verifies tokens only behind the
			// explicit MOTIVRA_JWT_ALLOW_HS256 migration flag.
			Name: "validator",
			Run: func(context.Context) error {
				if validator.Load() != nil {
					return nil
				}
				v, err := platform.NewValidatorFromConfig(cfg)
				if err != nil {
					return err
				}
				validator.Store(v)
				return nil
			},
		},
		{
			// JWKS readiness: a verifier that cannot load the issuer key set
			// refuses every token — surface that on /readyz (ADR-0007). The
			// live checker added here keeps reporting it after readiness.
			Name: "jwks",
			Run: func(ctx context.Context) error {
				v := validator.Load()
				if v == nil {
					return errors.New("token validator not wired yet")
				}
				srv.AddChecker("jwks", v.ReadyCheck)
				return v.ReadyCheck(ctx)
			},
		},
		{
			// Decision-record database: OPTIONAL at boot (the engine scores
			// without persistence — the documented no-database posture), but
			// when MOTIVRA_DATABASE_URL is set the audit trail is a hard
			// dependency: an unreachable database blocks readiness instead of
			// letting scoring run unrecorded.
			Name: "database",
			Run: func(ctx context.Context) error {
				if cfg.Database.URL == "" {
					return nil
				}
				if pool == nil {
					p, err := platform.NewPostgres(ctx, cfg.Database.URL)
					if err != nil {
						return err
					}
					pool = p
					boot.OnShutdown(func(context.Context) error { pool.Close(); return nil })
					srv.AddChecker("database", func(ctx context.Context) error { return pool.Ping(ctx) })
				}
				return pool.Ping(ctx)
			},
		},
		{
			Name: "routes",
			Run: func(context.Context) error {
				if boot.DomainHandler() != nil {
					return nil
				}
				if pool != nil {
					recorder = dispatch.NewPostgresDecisionRecorder(pool)
				} else {
					logger.Warn("dispatch decision records disabled: MOTIVRA_DATABASE_URL is not set (scoring works, nothing is audited)")
				}
				domain := chi.NewRouter()
				dispatch.Routes(domain, func(h http.HandlerFunc) http.HandlerFunc {
					return platform.RequireRole(dispatch.DispatcherRoles, h)
				}, recorder)
				// The scoring engine itself is in-process and stateless.
				srv.AddChecker("engine", func(context.Context) error { return nil })
				boot.SetDomainHandler(domain)
				return nil
			},
		},
	}

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ln, err := net.Listen("tcp", ":"+cfg.App.Port)
	if err != nil {
		logger.Error("http listener unavailable", "error", err, "port", cfg.App.Port)
		os.Exit(1)
	}
	go func() {
		if serveErr := httpSrv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("http_server_failed", "error", serveErr)
		}
	}()
	logger.Info("service_listening", "port", cfg.App.Port, "service", cfg.App.Name)

	if metrics != nil {
		stopMetricsSrv, err := platform.StartMetricsServer(cfg.MetricsAddr, metrics.Handler)
		if err != nil {
			logger.Error("metrics listener unavailable — serving degraded without Prometheus exposition", "error", err, "addr", cfg.MetricsAddr)
			boot.Degraded("metrics", "metrics listener unavailable: "+err.Error())
		} else {
			boot.OnShutdown(stopMetricsSrv)
		}
		boot.OnShutdown(metrics.Stop)
	}
	boot.OnShutdown(stopTracing)

	boot.Wire(ctx, steps...)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, boot.RunShutdownHooks); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}

// aggregateProblems joins configuration problems into one blocker reason.
func aggregateProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}
