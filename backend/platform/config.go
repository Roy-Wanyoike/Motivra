// Package platform provides the shared foundation for all Motivra backend
// services: configuration, structured logging, API error modelling,
// PostgreSQL access with migrations, JWT validation, domain event
// publishing over NATS JetStream, OpenTelemetry wiring, HTTP server
// conventions and graceful shutdown.
//
// The package contains no business logic and imports no domain packages
// (ADR-0001). New services copy cmd/template and build on these primitives.
package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names understood by the platform.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// AppConfig identifies the service process.
type AppConfig struct {
	Name string // MOTIVRA_APP_NAME
	Env  string // MOTIVRA_ENV: development | test | staging | production
	Port string // MOTIVRA_PORT
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	URL string // MOTIVRA_DATABASE_URL
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	URL string // MOTIVRA_REDIS_URL
}

// NATSConfig holds NATS connection settings.
type NATSConfig struct {
	URL string // MOTIVRA_NATS_URL
}

// TemporalConfig holds Temporal workflow-engine settings. Workflows are
// owned by their domains only (ADR-0001); the platform only dials clients.
type TemporalConfig struct {
	HostPort  string // MOTIVRA_TEMPORAL_ADDRESS
	Namespace string // MOTIVRA_TEMPORAL_NAMESPACE
}

// JWTConfig holds access-token validation settings. Services validate but
// never issue tokens; issuance belongs exclusively to the identity service
// (ADR-0004).
type JWTConfig struct {
	Secret   string // MOTIVRA_JWT_SECRET (>= 32 bytes outside development/test)
	Issuer   string // MOTIVRA_JWT_ISSUER
	Audience string // MOTIVRA_JWT_AUDIENCE
}

// legacyDevJWTSecret is the historical development placeholder that used to
// be baked into compose files and .env.example. It is PUBLIC (committed to
// the repository at one point), so any deployment booting with it would sign
// forgeable tokens. Validate refuses it in every environment except test.
const legacyDevJWTSecret = "dev-only-insecure-jwt-secret-change-me-32b"

// DefaultAbsoluteSessionTTL bounds the total lifetime of a refresh session:
// rotation extends the idle expiry but never the absolute one (hardening
// issue #81), so an actively-used session still ends after 90 days.
const DefaultAbsoluteSessionTTL = 90 * 24 * time.Hour

// OTelConfig controls OpenTelemetry export.
type OTelConfig struct {
	Endpoint string // OTEL_EXPORTER_OTLP_ENDPOINT
	Enabled  bool   // MOTIVRA_OTEL_ENABLED
}

// Config is the service-wide configuration.
type Config struct {
	App             AppConfig
	Database        DatabaseConfig
	Redis           RedisConfig
	NATS            NATSConfig
	Temporal        TemporalConfig
	JWT             JWTConfig
	OTel            OTelConfig
	ShutdownTimeout time.Duration // MOTIVRA_SHUTDOWN_TIMEOUT

	// MetricsAddr is the listener for the internal Prometheus exposition
	// (MOTIVRA_METRICS_ADDR, default ":9090"). Metrics are never served on
	// the public API listener.
	MetricsAddr string
	// TrustForwarded honours X-Forwarded-For for client-IP resolution
	// (MOTIVRA_TRUST_FORWARDED, default false). Enable ONLY when a trusted
	// proxy (load balancer, ingress) is the only thing in front of the
	// service; otherwise clients can spoof their IP.
	TrustForwarded bool
	// AuthRateLimitRPM caps authentication requests per minute per client
	// IP (MOTIVRA_AUTH_RATE_LIMIT_RPM, default 60; 0 disables). Applied by
	// the identity service to register/login/refresh/logout.
	AuthRateLimitRPM int
}

// Load reads configuration from the process environment and applies
// development-friendly defaults. Use Validate to enforce environment rules.
// OTel export defaults to ON in production: a production deployment must not
// silently run without telemetry (ADR-0005).
func Load() Config {
	env := envOr("MOTIVRA_ENV", EnvDevelopment)
	return Config{
		App: AppConfig{
			Name: envOr("MOTIVRA_APP_NAME", "motivra"),
			Env:  env,
			Port: envOr("MOTIVRA_PORT", "8080"),
		},
		Database: DatabaseConfig{URL: os.Getenv("MOTIVRA_DATABASE_URL")},
		Redis:    RedisConfig{URL: os.Getenv("MOTIVRA_REDIS_URL")},
		NATS:     NATSConfig{URL: os.Getenv("MOTIVRA_NATS_URL")},
		Temporal: TemporalConfig{
			HostPort:  os.Getenv("MOTIVRA_TEMPORAL_ADDRESS"),
			Namespace: envOr("MOTIVRA_TEMPORAL_NAMESPACE", "default"),
		},
		JWT: JWTConfig{
			Secret:   os.Getenv("MOTIVRA_JWT_SECRET"),
			Issuer:   envOr("MOTIVRA_JWT_ISSUER", "motivra-identity"),
			Audience: envOr("MOTIVRA_JWT_AUDIENCE", "motivra"),
		},
		OTel: OTelConfig{
			Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			Enabled:  envBool("MOTIVRA_OTEL_ENABLED", env == EnvProduction),
		},
		ShutdownTimeout: envDuration("MOTIVRA_SHUTDOWN_TIMEOUT", 15*time.Second),

		MetricsAddr:      envOr("MOTIVRA_METRICS_ADDR", ":9090"),
		TrustForwarded:   envBool("MOTIVRA_TRUST_FORWARDED", false),
		AuthRateLimitRPM: envInt("MOTIVRA_AUTH_RATE_LIMIT_RPM", 60),
	}
}

// Validate aggregates every configuration problem into a single error so an
// operator sees all missing values at once instead of one boot failure at a
// time. Rules: database URL required except in test; JWT secret required and
// at least 32 bytes in staging and production and refused when it is the
// historical public development placeholder in every non-test environment;
// port must be numeric in range.
func (c Config) Validate() error {
	var problems []string

	if c.Database.URL == "" && c.App.Env != EnvTest {
		problems = append(problems, "MOTIVRA_DATABASE_URL is required")
	}
	switch c.App.Env {
	case EnvDevelopment, EnvTest, EnvStaging, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf("MOTIVRA_ENV %q is not a known environment", c.App.Env))
	}
	if port, err := strconv.Atoi(c.App.Port); err != nil || port < 1 || port > 65535 {
		problems = append(problems, fmt.Sprintf("MOTIVRA_PORT %q is not a valid port", c.App.Port))
	}
	switch c.App.Env {
	case EnvTest:
		// Tests use fixed short secrets; no secret rules.
	case EnvDevelopment:
		// Development still refuses the PUBLIC placeholder: regenerating a
		// local secret costs one openssl call, a forgeable-token deployment
		// costs far more.
		if c.JWT.Secret == legacyDevJWTSecret {
			problems = append(problems, "MOTIVRA_JWT_SECRET must not be the historical development placeholder (generate one: openssl rand -base64 48)")
		}
	default: // staging, production
		if len(c.JWT.Secret) < 32 {
			problems = append(problems, fmt.Sprintf("MOTIVRA_JWT_SECRET must be at least 32 bytes in %s", c.App.Env))
		}
		if c.JWT.Secret == legacyDevJWTSecret {
			problems = append(problems, "MOTIVRA_JWT_SECRET must not be the historical development placeholder")
		}
	}
	if c.OTel.Enabled && c.OTel.Endpoint == "" {
		problems = append(problems, "OTEL_EXPORTER_OTLP_ENDPOINT is required when OTEL tracing is enabled")
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "MOTIVRA_SHUTDOWN_TIMEOUT must be positive")
	}
	if c.AuthRateLimitRPM < 0 {
		problems = append(problems, "MOTIVRA_AUTH_RATE_LIMIT_RPM must not be negative")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
}

// LoadForTest returns a Config valid for unit tests (no database required).
func LoadForTest() Config {
	return Config{
		App:              AppConfig{Name: "motivra-test", Env: EnvTest, Port: "8080"},
		JWT:              JWTConfig{Secret: strings.Repeat("test-secret-", 4), Issuer: "motivra-identity", Audience: "motivra"},
		ShutdownTimeout:  5 * time.Second,
		MetricsAddr:      "",
		AuthRateLimitRPM: 0, // tests exercise handlers directly; limiter off
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
