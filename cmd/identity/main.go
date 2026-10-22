// Command identity is the Motivra identity service: authentication
// (register/login/refresh/logout), session management, user profiles,
// admin account listing, asymmetric access-token issuance (EdDSA/RS256
// with a JWKS endpoint — ADR-0007) and the Redis-backed auth rate limiter
// over the identity domain's PostgreSQL tables.
//
// Boot is DEGRADED, never fatal (issue #122, ADR-0008): the listener binds
// first and /healthz answers 200 immediately; configuration and dependency
// failures become /readyz 503 blockers (retried with backoff) instead of
// os.Exit(1). Domain routes answer 503 problem+json until the wiring loop
// completes, so identity never serves authentication without its store.
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
	"github.com/redis/go-redis/v9"

	"github.com/Roy-Wanyoike/Motivra/backend/identity"
	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

func main() {
	cfg := platform.Load()
	logger := platform.NewLogger(cfg.App)
	ctx := context.Background()
	boot := platform.NewBoot(cfg, logger)

	// Observability degrades, never exits (issue #122, ADR-0008): a broken
	// collector must not take authentication down. Failures are recorded as
	// non-blocking degradations (surfaced on /readyz 200 bodies and logs).
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

	// Wiring state. Every step guards its own one-time work so a retry pass
	// can resume where the previous pass failed.
	var (
		pool        *pgxpool.Pool
		publisher   platform.Publisher      // service view: a nil *NATSPublisher must never masquerade as a live publisher
		natsPub     *platform.NATSPublisher // typed view: Close() lives on the concrete type
		redisClient *redis.Client
		signer      *platform.JWTSigner
		breacher    identity.PasswordBreacher
		validator   atomic.Pointer[platform.JWTValidator]
	)

	breachCfg := identity.LoadBreachConfig()
	verificationCfg := identity.LoadVerificationConfig()

	// The server exists BEFORE any dependency work: /healthz and /readyz are
	// live from the first millisecond; domain paths 503 until wiring lands.
	srvOpts := []platform.ServerOption{
		platform.WithBoot(boot),
		// Token verification resolves per request: until the wiring loop
		// stores a validator, tokens are refused (401), never bypassed.
		platform.WithMiddleware(platform.AuthMiddlewareFunc(validator.Load)),
	}
	if metrics != nil {
		srvOpts = append(srvOpts, platform.WithMiddleware(metrics.HTTP.Middleware))
	}
	srv := platform.NewServer(cfg, logger, srvOpts...)

	steps := []platform.BootStep{
		{
			// Configuration is fixed for the process lifetime: a problem here
			// is recorded once as a permanent blocker (no retry spam) and
			// only an owner-side env change + redeploy clears it.
			Name:      "config",
			Permanent: true,
			Run: func(context.Context) error {
				problems := cfg.ValidateProblems()
				// Identity is the issuer: the opaque-token digest key
				// (refresh and verification token HMACs, ADR-0004) is
				// required in every environment. Verifier services never
				// receive it (ADR-0007).
				if cfg.JWT.Secret == "" {
					problems = append(problems, "identity requires MOTIVRA_JWT_SECRET (it keys the opaque-token digests, ADR-0004)")
				}
				// Email/phone verification flow (issue #86): grace window,
				// token TTL, resend limits and the dev-only token echo.
				if err := verificationCfg.Validate(cfg.App.Env); err != nil {
					problems = append(problems, err.Error())
				}
				if len(problems) > 0 {
					return errors.New(strings.Join(problems, "; "))
				}
				return nil
			},
		},
		{
			Name: "database",
			Run: func(ctx context.Context) error {
				if cfg.Database.URL == "" {
					return errors.New("identity service requires MOTIVRA_DATABASE_URL")
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
			Name: "migrations",
			Run: func(ctx context.Context) error {
				if pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, pool, identitymigrations.FS, "schema_migrations_identity")
			},
		},
		{
			// Events publish on JetStream when MOTIVRA_NATS_URL is set (issue
			// #28, deferral 2); unset stays broker-free with a nil publisher —
			// local and test deployments publish nothing. Set-but-unreachable
			// blocks readiness until it connects (nats.go reconnects forever
			// afterwards; publish errors surface in requests, as before).
			Name: "nats",
			Run: func(ctx context.Context) error {
				if cfg.NATS.URL == "" || natsPub != nil {
					return nil
				}
				p, err := platform.NewNATSPublisher(ctx, cfg.NATS.URL)
				if err != nil {
					return err
				}
				natsPub = p
				publisher = p
				boot.OnShutdown(func(context.Context) error { natsPub.Close(); return nil })
				return nil
			},
		},
		{
			// Token signing (ADR-0007, issue #87): asymmetric EdDSA/RS256 by
			// default from MOTIVRA_JWT_PRIVATE_KEY_FILE; development/test
			// generate an ephemeral Ed25519 key when no file is configured.
			// Staging/production refuse ephemeral keys. The legacy HS256 path
			// requires the explicit MOTIVRA_JWT_ALLOW_HS256 migration flag.
			Name: "signer",
			Run: func(context.Context) error {
				if signer != nil || cfg.JWT.AllowHS256 {
					return nil
				}
				s, err := platform.NewSignerFromConfig(cfg)
				if err != nil {
					return err
				}
				signer = s
				logger.Info("asymmetric token signing active", "alg", signer.Algorithm(), "kid", signer.KID())
				return nil
			},
		},
		{
			// Breached-password check (ADR-0004, issue #86): the local
			// denylist always applies; HIBP k-anonymity joins the chain when
			// MOTIVRA_HIBP_ENABLED is set. An unreadable operator-provided
			// denylist must not silently vanish: it blocks readiness.
			Name: "breach_denylist",
			Run: func(context.Context) error {
				if breacher != nil {
					return nil
				}
				b, err := identity.NewBreacher(breachCfg)
				if err != nil {
					return err
				}
				breacher = b
				return nil
			},
		},
		{
			// Per-IP throttle on the public auth endpoints (0 disables; off in
			// tests). With MOTIVRA_REDIS_URL the limit is the shared,
			// cross-instance sliding window (issue #87); without it the
			// limiter falls back to the in-process fixed window. Runtime
			// Redis errors fail OPEN either way; a configured-but-down Redis
			// stays visible via the ping checker added here.
			Name: "redis",
			Run: func(context.Context) error {
				if cfg.AuthRateLimitRPM <= 0 || cfg.Redis.URL == "" || redisClient != nil {
					return nil
				}
				ropts, err := redis.ParseURL(cfg.Redis.URL)
				if err != nil {
					return err
				}
				redisClient = redis.NewClient(ropts)
				boot.OnShutdown(func(context.Context) error { return redisClient.Close() })
				// Rate-limit store health is public posture: a degraded Redis
				// means the limiter is failing open until it returns.
				srv.AddChecker("redis", func(ctx context.Context) error { return redisClient.Ping(ctx).Err() })
				return nil
			},
		},
		{
			// Identity also validates tokens on its own protected routes.
			// With a signer it verifies against the signer's PUBLIC key — the
			// private half never enters the validator. Legacy mode keeps the
			// shared secret (migration window only).
			Name: "validator",
			Run: func(context.Context) error {
				if validator.Load() != nil {
					return nil
				}
				if signer != nil {
					validator.Store(platform.NewValidator(
						platform.WithValidatorIssuerAudience(cfg.JWT.Issuer, cfg.JWT.Audience),
						platform.WithPublicKeyPEM(signer.KID(), signer.PublicKey()),
					))
					return nil
				}
				if !cfg.JWT.AllowHS256 {
					return errors.New("no token verification source: signer unavailable and MOTIVRA_JWT_ALLOW_HS256 unset")
				}
				logger.Warn("legacy HS256 token signing enabled via MOTIVRA_JWT_ALLOW_HS256",
					"note", "migration window only: verifiers still hold signing material (ADR-0007)")
				validator.Store(platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience))
				return nil
			},
		},
		{
			// Routes build LAST: every input (store, issuer, publisher,
			// breacher, limiter) is green by the time this step runs, and the
			// finished router is swapped in atomically — the first request a
			// domain route ever serves is a fully-wired one.
			Name: "routes",
			Run: func(context.Context) error {
				if boot.DomainHandler() != nil {
					return nil
				}
				var authLimiter platform.AuthLimiter
				if cfg.AuthRateLimitRPM > 0 {
					if redisClient != nil {
						limiterOpts := []platform.LimiterOption{platform.WithLimiterLogger(logger)}
						if metrics != nil {
							limiterOpts = append(limiterOpts, platform.WithLimiterMetrics(metrics.Registry()))
						}
						authLimiter = platform.NewRedisRateLimiter(redisClient, cfg.App.Name, cfg.AuthRateLimitRPM, limiterOpts...)
					} else {
						authLimiter = platform.NewRateLimiter(cfg.AuthRateLimitRPM)
					}
				}
				var issuer *identity.Issuer
				if signer != nil {
					issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
						identity.WithVerificationGrace(verificationCfg.Grace), identity.WithSigner(signer))
				} else {
					issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
						identity.WithVerificationGrace(verificationCfg.Grace))
				}
				svc := identity.NewService(identity.NewPostgresStore(pool), issuer, publisher,
					identity.WithTrustForwarded(cfg.TrustForwarded),
					identity.WithPasswordBreacher(breacher, breachCfg.FailClosed),
					identity.WithVerification(verificationCfg),
				)
				domain := chi.NewRouter()
				identity.Routes(domain, svc, authLimiter)
				boot.SetDomainHandler(domain)
				return nil
			},
		},
	}

	// Degraded boot, step 1 (issue #122): bind the listener BEFORE any
	// dependency work that can fail, so /healthz is answerable from the
	// first millisecond. A bind failure is the one boot error that still
	// exits — without a socket there is nothing to serve.
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

	// Metrics live on the internal listener only (issue #81/#84) — never
	// mounted on the public API router. An unavailable listener degrades.
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

	// Degraded boot, step 2: dependency wiring runs with retry/backoff. A
	// failure records a /readyz blocker; it NEVER exits the process (#122).
	boot.Wire(ctx, steps...)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, boot.RunShutdownHooks); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
