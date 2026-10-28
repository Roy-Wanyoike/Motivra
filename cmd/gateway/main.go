// Command gateway runs the Motivra API gateway (issue #202, ADR-0009): ONE
// process that serves every domain's HTTP surface behind the single /v1
// namespace the container topology routes to it. The gateway imports each
// domain's router constructor and mounts it verbatim — identity (auth,
// users, admin, JWKS), vehicles, jobs, garages (capacity; it rides the jobs
// service's pool and publisher, cmd/jobs precedent), dispatch, payments and
// inspections. Domain routers keep their own paths, their own middleware
// posture and their own RBAC gates; the gateway composes, it never
// re-implements routing.
//
// Boot is DEGRADED, never fatal (issue #122, ADR-0008), exactly like every
// reworked service: the listener binds first, /healthz answers 200
// immediately, and configuration/dependency failures become /readyz 503
// blockers (retried with backoff) instead of os.Exit(1). Domain routes
// answer 503 problem+json until the wiring loop completes. Unknown /v1
// prefixes answer 404 problem+json once the gate is open.
//
// Dependency model (ADR-0009): the gateway holds ONE PostgreSQL pool
// shared by every domain (the cmd/jobs jobs+garages precedent), ONE NATS
// publisher, ONE token validator. The database is a hard dependency —
// identity, vehicles, jobs, garages, payments and inspections all own
// authoritative state — so a missing or unreachable database blocks
// readiness for the whole process. The fail-fast payments/inspections
// mains keep their posture inside this contract: their dependencies remain
// hard dependencies, expressed as readyz blockers here because the process
// itself must keep serving /healthz (ADR-0008). Dispatch scores
// statelessly; when the database is wired every scoring decision is
// appended to the audit trail (cmd/dispatch posture).
//
// Observability (ADR-0005): one RED instrument per process plus the
// payments/inspections domain metrics on the same internal registry;
// /metrics is never on the public listener.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Roy-Wanyoike/Motivra/backend/dispatch"
	"github.com/Roy-Wanyoike/Motivra/backend/garages"
	"github.com/Roy-Wanyoike/Motivra/backend/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/inspections"
	"github.com/Roy-Wanyoike/Motivra/backend/jobs"
	garagesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/garages"
	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	inspectionsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/inspections"
	jobsnextmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/jobs"
	paymentsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/payments"
	vehiclesmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/vehicles"
	"github.com/Roy-Wanyoike/Motivra/backend/payments"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/Roy-Wanyoike/Motivra/backend/vehicles"
)

// Service-level env-var contract of the payments domain (issue #62): the
// same names cmd/payments reads. The strings are re-declared here because
// the gateway is the process that wires payments in the container
// topology (ADR-0009); backend/payments/README.md documents the contract.
const (
	envPaymentsProvider          = "MOTIVRA_PAYMENTS_PROVIDER"
	envPaymentsWebhookSecret     = "MOTIVRA_PAYMENTS_WEBHOOK_SECRET"
	envPaymentsReconcileInterval = "MOTIVRA_PAYMENTS_RECONCILE_INTERVAL"

	// defaultSandboxWebhookSecret is the documented development/test HMAC
	// secret for the sandbox webhook receiver (cmd/payments). It is used
	// only outside staging/production; there the variable is required.
	defaultSandboxWebhookSecret = "motivra-sandbox-webhook-secret-dev-only"

	defaultReconcileInterval = time.Minute
)

// domainRoutes binds one domain's router constructor — the exact
// backend/<domain>.Routes call cmd/<domain>/main.go makes — to the gateway
// router. The gateway's routes boot step fills it with the production
// constructors (productionMounts); the tests drive buildDomainRouter with
// the same shape so per-prefix routing is exercised against the real
// constructors, not a re-implementation.
type domainRoutes struct {
	Identity    func(chi.Router)
	Vehicles    func(chi.Router)
	Jobs        func(chi.Router)
	Dispatch    func(chi.Router)
	Payments    func(chi.Router)
	Inspections func(chi.Router)
	Garages     func(chi.Router)
}

// mounts returns the non-nil constructors in registration order. No two
// domains claim the same /v1 prefix, so order carries no routing weight.
func (d domainRoutes) mounts() []func(chi.Router) {
	all := []func(chi.Router){
		d.Identity, d.Vehicles, d.Jobs, d.Dispatch,
		d.Payments, d.Inspections, d.Garages,
	}
	out := make([]func(chi.Router), 0, len(all))
	for _, m := range all {
		if m != nil {
			out = append(out, m)
		}
	}
	return out
}

// buildDomainRouter composes the gateway's domain API router: each domain
// constructor registers its own absolute /v1 paths on one chi router (the
// domains own their routing; the gateway only composes). The gateway adds
// exactly two handlers of its own — problem+json 404 for unknown /v1
// prefixes and 405 for wrong methods — because once the ADR-0008 wiring
// gate opens, the platform Server hands every non-health path to this
// router (serveDomainGate), and a bare chi 404 text body would break the
// API's problem+json contract (issue #202 requirement 2).
func buildDomainRouter(routes domainRoutes) chi.Router {
	domain := chi.NewRouter()
	domain.NotFound(writeDomainNotFound)
	domain.MethodNotAllowed(writeDomainMethodNotAllowed)
	for _, mount := range routes.mounts() {
		mount(domain)
	}
	return domain
}

// writeDomainNotFound renders the API's 404 contract for unknown /v1
// prefixes (and any other unknown path behind the gate) through the
// platform error model, so clients get the same problem+json shape the
// domains use.
func writeDomainNotFound(w http.ResponseWriter, r *http.Request) {
	platform.WriteError(w, platform.ErrNotFound("no API route for "+r.URL.Path))
}

// writeDomainMethodNotAllowed renders the API's 405 contract for a known
// path hit with an unsupported method — the same code/title shape the
// platform Server's own method gate (ADR-0008) writes before readiness.
func writeDomainMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	platform.WriteError(w, &platform.Error{
		Status: http.StatusMethodNotAllowed,
		Code:   "method_not_allowed",
		Title:  "Method not allowed",
	})
}

// gatewayWiring is the shared wiring state of the gateway process. Every
// boot step guards its own one-time work so a retry pass resumes where the
// previous pass failed (the cmd/<domain> closure-captured vars, gathered
// into one struct because seven domains share this process).
type gatewayWiring struct {
	cfg     platform.Config
	logger  *slog.Logger
	boot    *platform.Boot
	srv     *platform.Server
	metrics *platform.Metrics

	pool        *pgxpool.Pool
	publisher   platform.Publisher      // service view: a nil *NATSPublisher must never masquerade as a live publisher
	natsPub     *platform.NATSPublisher // typed view: Close() lives on the concrete type
	signer      *platform.JWTSigner
	breacher    identity.PasswordBreacher
	redisClient *redis.Client
	validator   atomic.Pointer[platform.JWTValidator]

	breachCfg       identity.BreachConfig
	verificationCfg identity.VerificationConfig

	// Payments provider wiring (the cmd/payments contract).
	sandboxWebhookSecret string
	darajaCfg            payments.DarajaConfig
	darajaComplete       bool
	paymentsSelection    string
	reconcileInterval    time.Duration
	reconciler           *payments.Reconciler
}

// newGatewayWiring reads the process environment once (payments provider
// contract, breach denylist, verification flow) into the shared wiring
// state. Configuration VALIDATION happens in the config boot step so
// problems surface as /readyz blockers (ADR-0008), never as exits.
func newGatewayWiring(cfg platform.Config, logger *slog.Logger, metrics *platform.Metrics) *gatewayWiring {
	sandboxSecret := os.Getenv(envPaymentsWebhookSecret)
	if sandboxSecret == "" && cfg.App.Env != platform.EnvStaging && cfg.App.Env != platform.EnvProduction {
		sandboxSecret = defaultSandboxWebhookSecret
	}
	darajaCfg, darajaComplete := payments.DarajaConfigFromEnvironment()
	return &gatewayWiring{
		cfg:                  cfg,
		logger:               logger,
		metrics:              metrics,
		breachCfg:            identity.LoadBreachConfig(),
		verificationCfg:      identity.LoadVerificationConfig(),
		sandboxWebhookSecret: sandboxSecret,
		darajaCfg:            darajaCfg,
		darajaComplete:       darajaComplete,
		paymentsSelection:    envOr(envPaymentsProvider, payments.ProviderSandbox),
		reconcileInterval:    defaultReconcileInterval,
	}
}

// newGatewayServer builds the platform server for the gateway process: the
// ADR-0008 boot gate plus the shared token-verification middleware,
// resolved per request from the wiring loop's validator (until it lands,
// tokens are refused — never bypassed). Shared by main and the tests so
// the boot contract is exercised against the production constructor chain.
func newGatewayServer(cfg platform.Config, logger *slog.Logger, boot *platform.Boot, validator *atomic.Pointer[platform.JWTValidator], metrics *platform.Metrics) *platform.Server {
	opts := []platform.ServerOption{
		platform.WithBoot(boot),
		platform.WithMiddleware(platform.AuthMiddlewareFunc(validator.Load)),
	}
	if metrics != nil {
		opts = append(opts, platform.WithMiddleware(metrics.HTTP.Middleware))
	}
	return platform.NewServer(cfg, logger, opts...)
}

// gatewayBootSteps is the wiring sequence of the gateway process — the
// union of the mounted domains' steps, ordered so dependencies precede
// consumers and routes build LAST (every input green by the time the
// router is swapped in atomically). Shared by main and the integration
// tests so tests drive the exact production boot.
func gatewayBootSteps(w *gatewayWiring) []platform.BootStep {
	cfg := w.cfg
	return []platform.BootStep{
		{
			// Configuration is fixed for the process lifetime: a problem here
			// is recorded once as a permanent blocker (no retry spam). The
			// gateway aggregates the config rules of every mounted domain:
			// the platform baseline, the identity issuer requirements (the
			// gateway issues tokens, cmd/identity rules) and the payments
			// provider contract (cmd/payments rules).
			Name:      "config",
			Permanent: true,
			Run: func(context.Context) error {
				problems := cfg.ValidateProblems()
				// Identity is the issuer: the opaque-token digest key
				// (refresh and verification token HMACs, ADR-0004) is
				// required in every environment (cmd/identity rule).
				if cfg.JWT.Secret == "" {
					problems = append(problems, "identity requires MOTIVRA_JWT_SECRET (it keys the opaque-token digests, ADR-0004)")
				}
				if err := w.verificationCfg.Validate(cfg.App.Env); err != nil {
					problems = append(problems, err.Error())
				}
				switch w.paymentsSelection {
				case payments.ProviderSandbox:
				case payments.ProviderDaraja:
					if !w.darajaComplete {
						problems = append(problems, envPaymentsProvider+"=daraja but the Daraja STK credentials are incomplete (MOTIVRA_PAYMENTS_DARAJA_CONSUMER_KEY, _CONSUMER_SECRET, _SHORTCODE, _PASSKEY, _CALLBACK_URL)")
					}
				default:
					problems = append(problems, envPaymentsProvider+" must be \"sandbox\" or \"daraja\"")
				}
				if w.sandboxWebhookSecret == "" {
					problems = append(problems, envPaymentsWebhookSecret+" is required outside development/test (sandbox webhook receiver refuses unsigned deliveries)")
				}
				if raw := os.Getenv(envPaymentsReconcileInterval); raw != "" {
					secs, err := strconv.Atoi(raw)
					if err != nil || secs <= 0 {
						problems = append(problems, envPaymentsReconcileInterval+" must be a positive number of seconds")
					} else {
						w.reconcileInterval = time.Duration(secs) * time.Second
					}
				}
				return aggregateProblems(problems)
			},
		},
		{
			// The gateway serves six stateful domains over ONE pool (the
			// cmd/jobs jobs+garages precedent): the database is a hard
			// dependency of the whole process.
			Name: "database",
			Run: func(ctx context.Context) error {
				if cfg.Database.URL == "" {
					return errors.New("MOTIVRA_DATABASE_URL is required for the gateway (it serves the identity, vehicles, jobs, garages, payments and inspections domains)")
				}
				if w.pool == nil {
					p, err := platform.NewPostgres(ctx, cfg.Database.URL)
					if err != nil {
						return err
					}
					w.pool = p
					w.boot.OnShutdown(func(context.Context) error { w.pool.Close(); return nil })
					w.srv.AddChecker("database", func(ctx context.Context) error { return w.pool.Ping(ctx) })
				}
				return w.pool.Ping(ctx)
			},
		},
		{
			// Six migration chains, one per owning domain (ADR-0003:
			// per-domain chains, separate schema_migrations tables).
			Name: "migrations-identity",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, identitymigrations.FS, "schema_migrations_identity")
			},
		},
		{
			Name: "migrations-vehicles",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, vehiclesmigrations.FS, "schema_migrations_vehicles")
			},
		},
		{
			Name: "migrations-jobs",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, jobsnextmigrations.FS, "schema_migrations_jobs")
			},
		},
		{
			Name: "migrations-garages",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, garagesmigrations.FS, "schema_migrations_garages")
			},
		},
		{
			Name: "migrations-payments",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, paymentsmigrations.FS, "schema_migrations_payments")
			},
		},
		{
			Name: "migrations-inspections",
			Run: func(ctx context.Context) error {
				if w.pool == nil {
					return errors.New("database not connected yet")
				}
				return platform.MigrateUp(ctx, w.pool, inspectionsmigrations.FS, "schema_migrations_inspections")
			},
		},
		{
			// Events publish on JetStream when MOTIVRA_NATS_URL is set (issue
			// #28, deferral 2); unset stays broker-free with a nil publisher.
			Name: "nats",
			Run: func(ctx context.Context) error {
				if cfg.NATS.URL == "" || w.natsPub != nil {
					return nil
				}
				p, err := platform.NewNATSPublisher(ctx, cfg.NATS.URL)
				if err != nil {
					return err
				}
				w.natsPub = p
				w.publisher = p
				w.boot.OnShutdown(func(context.Context) error { w.natsPub.Close(); return nil })
				return nil
			},
		},
		{
			// Token signing (ADR-0007, issue #87): the gateway hosts identity,
			// the issuer, so it holds MOTIVRA_JWT_PRIVATE_KEY_FILE exactly
			// like cmd/identity. Development/test generate an ephemeral
			// Ed25519 key when no file is configured; staging/production
			// refuse ephemeral keys. The legacy HS256 path requires the
			// explicit MOTIVRA_JWT_ALLOW_HS256 migration flag.
			Name: "signer",
			Run: func(context.Context) error {
				if w.signer != nil || cfg.JWT.AllowHS256 {
					return nil
				}
				s, err := platform.NewSignerFromConfig(cfg)
				if err != nil {
					return err
				}
				w.signer = s
				w.logger.Info("asymmetric token signing active", "alg", s.Algorithm(), "kid", s.KID())
				return nil
			},
		},
		{
			// Breached-password check (ADR-0004, issue #86): the local
			// denylist always applies; HIBP k-anonymity joins the chain when
			// MOTIVRA_HIBP_ENABLED is set (cmd/identity rule).
			Name: "breach_denylist",
			Run: func(context.Context) error {
				if w.breacher != nil {
					return nil
				}
				b, err := identity.NewBreacher(w.breachCfg)
				if err != nil {
					return err
				}
				w.breacher = b
				return nil
			},
		},
		{
			// Per-IP throttle on the public auth endpoints (cmd/identity
			// rule): shared Redis window when MOTIVRA_REDIS_URL is set,
			// in-process fixed window otherwise, off when the RPM is 0.
			Name: "redis",
			Run: func(context.Context) error {
				if cfg.AuthRateLimitRPM <= 0 || cfg.Redis.URL == "" || w.redisClient != nil {
					return nil
				}
				ropts, err := redis.ParseURL(cfg.Redis.URL)
				if err != nil {
					return err
				}
				w.redisClient = redis.NewClient(ropts)
				w.boot.OnShutdown(func(context.Context) error { return w.redisClient.Close() })
				// Rate-limit store health is public posture: a degraded Redis
				// means the limiter is failing open until it returns.
				w.srv.AddChecker("redis", func(ctx context.Context) error { return w.redisClient.Ping(ctx).Err() })
				return nil
			},
		},
		{
			// Token verification for every mounted domain. With a signer the
			// validator verifies against the signer's PUBLIC key — the
			// private half never enters the validator (cmd/identity rule).
			// Legacy mode keeps the shared secret (migration window only).
			Name: "validator",
			Run: func(context.Context) error {
				if w.validator.Load() != nil {
					return nil
				}
				if w.signer != nil {
					w.validator.Store(platform.NewValidator(
						platform.WithValidatorIssuerAudience(cfg.JWT.Issuer, cfg.JWT.Audience),
						platform.WithPublicKeyPEM(w.signer.KID(), w.signer.PublicKey()),
					))
					return nil
				}
				if !cfg.JWT.AllowHS256 {
					return errors.New("no token verification source: signer unavailable and MOTIVRA_JWT_ALLOW_HS256 unset")
				}
				w.logger.Warn("legacy HS256 token signing enabled via MOTIVRA_JWT_ALLOW_HS256",
					"note", "migration window only: verifiers still hold signing material (ADR-0007)")
				w.validator.Store(platform.NewJWTValidator(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience))
				return nil
			},
		},
		{
			// JWKS readiness: a verifier that cannot load the issuer key set
			// refuses every token — surface that on /readyz (ADR-0007). The
			// live checker added here keeps reporting it after readiness.
			Name: "jwks",
			Run: func(ctx context.Context) error {
				v := w.validator.Load()
				if v == nil {
					return errors.New("token validator not wired yet")
				}
				w.srv.AddChecker("jwks", v.ReadyCheck)
				return v.ReadyCheck(ctx)
			},
		},
		{
			// Routes build LAST: every input (pool, publisher, signer,
			// breacher, limiter, validator) is green by the time this step
			// runs, and the finished router is swapped in atomically — the
			// first request a domain route ever serves is a fully-wired one.
			Name: "routes",
			Run: func(context.Context) error {
				if w.boot.DomainHandler() != nil {
					return nil
				}
				w.boot.SetDomainHandler(buildDomainRouter(productionMounts(w)))
				// Dispatch scoring is in-process and stateless.
				w.srv.AddChecker("engine", func(context.Context) error { return nil })
				// Payments reconciliation ticker (cmd/payments): the
				// bounded-batch fallback that repairs missed webhooks;
				// POST /v1/payments/reconcile runs the same reconciler on
				// demand. Started only here — the routes step never runs
				// without a pool, because the database step gates it.
				stop := startReconciler(w.logger, w.reconciler, w.reconcileInterval)
				w.boot.OnShutdown(stop)
				return nil
			},
		},
	}
}

// productionMounts builds every domain service from the wired dependencies
// and returns the domain router constructors. It is the ONE place that
// composes the domain packages, shared by the routes boot step and the
// integration tests, so tests drive the exact production wiring: the same
// service constructors, the same Routes calls, the same RBAC gates as the
// cmd/<domain> mains.
func productionMounts(w *gatewayWiring) domainRoutes {
	cfg := w.cfg

	// Identity (issuer): issuer + service, same options as cmd/identity.
	var issuer *identity.Issuer
	if w.signer != nil {
		issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
			identity.WithVerificationGrace(w.verificationCfg.Grace), identity.WithSigner(w.signer))
	} else {
		issuer = identity.NewIssuer(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.Audience,
			identity.WithVerificationGrace(w.verificationCfg.Grace))
	}
	var authLimiter platform.AuthLimiter
	if cfg.AuthRateLimitRPM > 0 {
		if w.redisClient != nil {
			limiterOpts := []platform.LimiterOption{platform.WithLimiterLogger(w.logger)}
			if w.metrics != nil {
				limiterOpts = append(limiterOpts, platform.WithLimiterMetrics(w.metrics.Registry()))
			}
			authLimiter = platform.NewRedisRateLimiter(w.redisClient, cfg.App.Name, cfg.AuthRateLimitRPM, limiterOpts...)
		} else {
			authLimiter = platform.NewRateLimiter(cfg.AuthRateLimitRPM)
		}
	}
	identitySvc := identity.NewService(identity.NewPostgresStore(w.pool), issuer, w.publisher,
		identity.WithTrustForwarded(cfg.TrustForwarded),
		identity.WithPasswordBreacher(w.breacher, w.breachCfg.FailClosed),
		identity.WithVerification(w.verificationCfg),
	)

	// Vehicles, jobs and garages: the stateful registries over the shared
	// pool (garages rides this process exactly as in cmd/jobs).
	vehiclesSvc := vehicles.NewService(vehicles.NewPostgresStore(w.pool), w.publisher)
	jobsSvc := jobs.NewService(jobs.NewPostgresStore(w.pool), w.publisher)
	garageSvc := garages.NewService(garages.NewPostgresStore(w.pool), w.publisher)

	// Dispatch: decision records on the shared pool when it exists (the
	// documented no-persistence posture applies only when it does not).
	var recorder dispatch.DecisionRecorder
	if w.pool != nil {
		recorder = dispatch.NewPostgresDecisionRecorder(w.pool)
	}

	// Payments: the sandbox adapter is ALWAYS registered (deterministic
	// mock and fallback); Daraja joins when its credentials are complete.
	// The default selection was validated in the config step.
	providers := map[string]payments.Provider{}
	sandbox := payments.NewSandboxProvider([]byte(w.sandboxWebhookSecret))
	providers[sandbox.Name()] = sandbox
	if w.darajaComplete {
		daraja := payments.NewDarajaProvider(w.darajaCfg)
		providers[daraja.Name()] = daraja
	}
	var paymentsMetrics *payments.Metrics
	if w.metrics != nil {
		paymentsMetrics = payments.NewMetrics(w.metrics.Registry())
	}
	paymentsStore := payments.NewPostgresStore(w.pool)
	paymentsSvc := payments.NewService(paymentsStore, providers, w.publisher, paymentsMetrics)
	paymentsRec := payments.NewReconciler(paymentsStore, providers, w.publisher, paymentsMetrics, 0)
	if w.paymentsSelection == payments.ProviderDaraja {
		paymentsSvc.DefaultProvider = payments.ProviderDaraja
	}
	w.reconciler = paymentsRec

	// Inspections: template registry + session state machine.
	var inspectionsMetrics *inspections.Metrics
	if w.metrics != nil {
		inspectionsMetrics = inspections.NewMetrics(w.metrics.Registry())
	}
	inspectionsSvc := inspections.NewService(inspections.NewPostgresStore(w.pool), w.publisher, inspectionsMetrics)

	return domainRoutes{
		Identity: func(r chi.Router) { identity.Routes(r, identitySvc, authLimiter) },
		Vehicles: func(r chi.Router) { vehicles.Routes(r, vehiclesSvc, platform.RequireAuthenticated) },
		Jobs:     func(r chi.Router) { jobs.Routes(r, jobsSvc, platform.RequireAuthenticated) },
		Dispatch: func(r chi.Router) {
			dispatch.Routes(r, func(h http.HandlerFunc) http.HandlerFunc {
				return platform.RequireRole(dispatch.DispatcherRoles, h)
			}, recorder)
		},
		Payments: func(r chi.Router) {
			payments.Routes(r, paymentsSvc, paymentsRec, platform.RequireAuthenticated)
		},
		Inspections: func(r chi.Router) {
			inspections.Routes(r, inspectionsSvc, platform.RequireAuthenticated)
		},
		Garages: func(r chi.Router) { garages.Routes(r, garageSvc, platform.RequireAuthenticated) },
	}
}

// startReconciler runs one bounded payments reconciliation batch per
// interval until the returned stop function cancels the loop (graceful
// shutdown). Runs continue past individual provider failures (the next
// tick queries again); the drift gauge and run counter track them — the
// cmd/payments loop, unchanged.
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

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// aggregateProblems joins configuration problems into one blocker reason.
func aggregateProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

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

	w := newGatewayWiring(cfg, logger, metrics)
	w.boot = boot
	// The server exists BEFORE any dependency work: /healthz and /readyz are
	// live from the first millisecond; domain paths 503 until wiring lands.
	w.srv = newGatewayServer(cfg, logger, boot, &w.validator, metrics)

	// Degraded boot, step 1 (issue #122): bind the listener BEFORE any
	// dependency work that can fail, so /healthz is answerable from the
	// first millisecond. A bind failure is the one boot error that still
	// exits — without a socket there is nothing to serve.
	httpSrv := &http.Server{
		Handler:           w.srv,
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
	boot.Wire(ctx, gatewayBootSteps(w)...)

	if err := platform.Graceful(httpSrv, logger, cfg.ShutdownTimeout, boot.RunShutdownHooks); err != nil {
		logger.Error("shutdown completed with errors", "error", err)
		os.Exit(1)
	}
}
