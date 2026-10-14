// Command identity is the Motivra identity service: authentication
// (register/login/refresh/logout), session management, user profiles,
// admin account listing, asymmetric access-token issuance (EdDSA/RS256
// with a JWKS endpoint — ADR-0007) and the Redis-backed auth rate limiter
// over the identity domain's PostgreSQL tables.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

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

	// Identity is the issuer: the opaque-token digest key (refresh and
	// verification token HMACs, ADR-0004) must be present in every
	// environment. Verifier services never receive it (ADR-0007).
	if cfg.JWT.Secret == "" {
		logger.Error("identity service requires MOTIVRA_JWT_SECRET (it keys the opaque-token digests)")
		os.Exit(1)
	}

	// Token signing (ADR-0007, issue #87): asymmetric EdDSA/RS256 by
	// default. The private key loads from MOTIVRA_JWT_PRIVATE_KEY_FILE;
	// development/test generate an ephemeral Ed25519 key when no file is
	// configured (public keys are served over JWKS, so verifier replicas
	// re-discover them). Staging/production refuse ephemeral keys; the
	// legacy HS256 path requires the explicit MOTIVRA_JWT_ALLOW_HS256
	// migration flag.
	var signer *platform.JWTSigner
	if !cfg.JWT.AllowHS256 {
		var err error
		signer, err = platform.NewSignerFromConfig(cfg)
		if err != nil {
			logger.Error("token signer unavailable", "error", err)
			os.Exit(1)
		}
		logger.Info("asymmetric token signing active", "alg", signer.Algorithm(), "kid", signer.KID())
	} else {
		logger.Warn("legacy HS256 token signing enabled via MOTIVRA_JWT_ALLOW_HS256",
			"note", "migration window only: verifiers still hold signing material (ADR-0007)")
	}
	var issuer *identity.Issuer
	if signer != nil {
		issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
			identity.WithVerificationGrace(verificationCfg.Grace), identity.WithSigner(signer))
	} else {
		issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
			identity.WithVerificationGrace(verificationCfg.Grace))
	}
	svc := identity.NewService(store, issuer, publisher,
		identity.WithTrustForwarded(cfg.TrustForwarded),
		identity.WithPasswordBreacher(breacher, breachCfg.FailClosed),
		identity.WithVerification(verificationCfg),
	)
	// Per-IP throttle on the public auth endpoints (0 disables; off in
	// tests). With MOTIVRA_REDIS_URL the limit is the shared,
	// cross-instance sliding window (issue #87); without it the limiter
	// falls back to the in-process fixed window (single-instance
	// deployments). Runtime Redis errors fail OPEN either way.
	var authLimiter platform.AuthLimiter
	var redisClient *redis.Client
	if cfg.AuthRateLimitRPM > 0 {
		if cfg.Redis.URL != "" {
			ropts, err := redis.ParseURL(cfg.Redis.URL)
			if err != nil {
				logger.Error("redis URL invalid", "error", err)
				os.Exit(1)
			}
			redisClient = redis.NewClient(ropts)
			authLimiter = platform.NewRedisRateLimiter(redisClient, cfg.App.Name, cfg.AuthRateLimitRPM,
				platform.WithLimiterMetrics(metrics.Registry()),
				platform.WithLimiterLogger(logger))
		} else {
			authLimiter = platform.NewRateLimiter(cfg.AuthRateLimitRPM)
		}
	}

	cleanups := []func(context.Context) error{
		func(context.Context) error { pool.Close(); return nil },
	}
	if publisher != nil {
		cleanups = append(cleanups, func(context.Context) error { publisher.Close(); return nil })
	}
	if redisClient != nil {
		cleanups = append(cleanups, func(context.Context) error { return redisClient.Close() })
	}
	stopMetricsSrv, err := platform.StartMetricsServer(cfg.MetricsAddr, metrics.Handler)
	if err != nil {
		logger.Error("metrics listener unavailable", "error", err, "addr", cfg.MetricsAddr)
		os.Exit(1)
	}
	cleanups = append(cleanups, stopMetricsSrv, metrics.Stop, stopTracing)

	// Identity also validates tokens on its own protected routes. With a
	// signer it verifies against the signer's PUBLIC key — the private
	// half never enters the validator. Legacy mode keeps the shared
	// secret (migration window only).
	var validator *platform.JWTValidator
	if signer != nil {
		validator = platform.NewValidator(
			platform.WithValidatorIssuerAudience(cfg.JWT.Issuer, cfg.JWT.Audience),
			platform.WithPublicKeyPEM(signer.KID(), signer.PublicKey()),
		)
	} else {
		validator = platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience)
	}
	srvOpts := []platform.ServerOption{
		platform.WithChecker("database", func(ctx context.Context) error {
			return pool.Ping(ctx)
		}),
		platform.WithMiddleware(platform.AuthMiddleware(validator)),
		platform.WithMiddleware(metrics.HTTP.Middleware),
	}
	if redisClient != nil {
		// Rate-limit store health is public posture: a degraded Redis
		// means the limiter is failing open until it returns.
		srvOpts = append(srvOpts, platform.WithChecker("redis", func(ctx context.Context) error {
			return redisClient.Ping(ctx).Err()
		}))
	}
	srv := platform.NewServer(cfg, logger, srvOpts...)
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
