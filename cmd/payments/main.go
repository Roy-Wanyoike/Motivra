// Command payments runs the Motivra Payments service (issue #62, Wave 5
// A17): the integer-KES ledger over payment intents, transactions and
// refunds, the provider abstraction with the deterministic sandbox adapter
// and the M-Pesa Daraja adapter, the signature-verified idempotent webhook
// receiver and the reconciliation fallback. Boot follows the cmd/template
// conventions: config, logger, tracing, metrics, PostgreSQL with the
// domain's own migration chain, JWT validation on the money routes, a
// reconciliation ticker and graceful shutdown.
//
// Provider selection (see backend/payments/README.md for the full table):
//
//	MOTIVRA_PAYMENTS_PROVIDER   sandbox (default) | daraja
//	MOTIVRA_PAYMENTS_WEBHOOK_SECRET   sandbox webhook HMAC (required
//	                                  outside development/test; Daraja
//	                                  carries its own *_DARAJA_* vars)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	paymentsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/payments"
	"github.com/Roy-Wanyoike/Motivra/backend/payments"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Service-level env-var contract (adapter credentials live in the
// payments package — daraja.go).
const (
	envProviderSelection    = "MOTIVRA_PAYMENTS_PROVIDER"
	envSandboxWebhookSecret = "MOTIVRA_PAYMENTS_WEBHOOK_SECRET"
	envReconcileInterval    = "MOTIVRA_PAYMENTS_RECONCILE_INTERVAL"

	// defaultSandboxWebhookSecret is the documented development/test HMAC
	// secret for the sandbox webhook receiver. It is refused outside
	// development and test: staging/production must configure a real
	// secret (the sandbox is a local-dev adapter, not a payment rail).
	defaultSandboxWebhookSecret = "motivra-sandbox-webhook-secret-dev-only"
	defaultReconcileInterval    = time.Minute
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := platform.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	// Payments owns the authoritative money ledger; it cannot boot without
	// its database.
	if cfg.Database.URL == "" {
		slog.Error("configuration invalid: MOTIVRA_DATABASE_URL is required for the payments service")
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
	if err := platform.MigrateUp(ctx, pool, paymentsmigrations.FS, "schema_migrations_payments"); err != nil {
		logger.Error("migrations failed", "error", err)
		os.Exit(1)
	}

	// Domain instruments register on the same registry the platform RED
	// metrics live on, so /metrics scrapes them together (ADR-0005).
	domainMetrics := payments.NewMetrics(metrics.Registry())

	// Events publish on JetStream when MOTIVRA_NATS_URL is set; without it
	// the service runs with a nil publisher and the domain skips event
	// publishing — local and test deployments stay broker-free.
	var publisher *platform.NATSPublisher
	if cfg.NATS.URL != "" {
		publisher, err = platform.NewNATSPublisher(ctx, cfg.NATS.URL)
		if err != nil {
			logger.Error("nats unavailable", "error", err)
			os.Exit(1)
		}
	}

	// --- Provider wiring ---------------------------------------------------
	// The sandbox adapter is ALWAYS registered: it is the deterministic
	// mock for tests and local development and the fallback while the
	// owner has not provisioned Daraja credentials. The Daraja adapter is
	// registered only when its STK-push environment subset is complete.
	providers := map[string]payments.Provider{}
	sandboxSecret := os.Getenv(envSandboxWebhookSecret)
	if sandboxSecret == "" {
		if cfg.App.Env == platform.EnvStaging || cfg.App.Env == platform.EnvProduction {
			logger.Error("configuration invalid: " + envSandboxWebhookSecret + " is required outside development/test (sandbox webhook receiver refuses unsigned deliveries)")
			os.Exit(1)
		}
		sandboxSecret = defaultSandboxWebhookSecret
	}
	sandbox := payments.NewSandboxProvider([]byte(sandboxSecret))
	providers[sandbox.Name()] = sandbox

	darajaCfg, darajaComplete := payments.DarajaConfigFromEnvironment()
	if darajaComplete {
		daraja := payments.NewDarajaProvider(darajaCfg)
		providers[daraja.Name()] = daraja
		logger.Info("daraja_adapter_configured", "base_url", darajaCfg.BaseURL)
	}

	store := payments.NewPostgresStore(pool)
	svc := payments.NewService(store, providers, publisher, domainMetrics)
	reconciler := payments.NewReconciler(store, providers, publisher, domainMetrics, 0)

	// Default adapter selection: initiations without an explicit provider
	// use this adapter. "daraja" requires the credentials above.
	switch sel := envOr(envProviderSelection, "sandbox"); sel {
	case "sandbox":
	case "daraja":
		if !darajaComplete {
			logger.Error("configuration invalid: " + envProviderSelection + "=daraja but the Daraja STK credentials are incomplete (MOTIVRA_PAYMENTS_DARAJA_CONSUMER_KEY, _CONSUMER_SECRET, _SHORTCODE, _PASSKEY, _CALLBACK_URL)")
			os.Exit(1)
		}
		svc.DefaultProvider = payments.ProviderDaraja
	default:
		logger.Error("configuration invalid: " + envProviderSelection + " must be \"sandbox\" or \"daraja\"")
		os.Exit(1)
	}

	// --- HTTP wiring -------------------------------------------------------
	// The money routes sit behind the JWT middleware; the webhook receiver
	// does NOT (the provider HMAC signature is its trust boundary — the
	// Routes wiring leaves it unwrapped).
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
		platform.WithMiddleware(metrics.HTTP.Middleware),
	)
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			platform.AuthMiddleware(validator)(platform.RequireAuthenticated(h)).ServeHTTP(w, r)
		}
	}
	payments.Routes(srv.Router, svc, reconciler, authed)

	httpSrv := &http.Server{
		Addr:              ":" + cfg.App.Port,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("service_listening", "port", cfg.App.Port, "service", cfg.App.Name)

	// --- Reconciliation ticker ----------------------------------------------
	// The bounded-batch fallback that repairs missed webhooks (the operator
	// route POST /v1/payments/reconcile runs the same Reconciler on demand).
	interval := defaultReconcileInterval
	if raw := os.Getenv(envReconcileInterval); raw != "" {
		secs, parseErr := strconv.Atoi(raw)
		if parseErr != nil || secs <= 0 {
			logger.Error("configuration invalid: " + envReconcileInterval + " must be a positive number of seconds")
			os.Exit(1)
		}
		interval = time.Duration(secs) * time.Second
	}
	stopReconciler := startReconciler(logger, reconciler, interval)

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
	cleanups = append(cleanups, stopMetricsSrv, metrics.Stop, stopTracing, stopReconciler)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, cleanups...); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}

// startReconciler runs one bounded reconciliation batch per interval until
// the returned stop function cancels the loop (graceful shutdown). Runs
// continue past individual provider failures (the next tick queries
// again); the drift gauge and run counter track them.
func startReconciler(logger *slog.Logger, reconciler *payments.Reconciler, interval time.Duration) func(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, err := reconciler.Run(ctx)
				if err != nil {
					logger.Error("payments_reconciliation_run_failed", "error", err)
					continue
				}
				if result.Scanned > 0 {
					logger.Info("payments_reconciliation_run",
						"scanned", result.Scanned, "repaired", result.Repaired,
						"still_pending", result.StillPending, "errors", result.Errors)
				}
			}
		}
	}()
	return func(context.Context) error {
		cancel()
		<-done
		return nil
	}
}
