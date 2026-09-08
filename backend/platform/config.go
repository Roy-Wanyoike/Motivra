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
	Secret   string // MOTIVRA_JWT_SECRET (>= 32 bytes in production)
	Issuer   string // MOTIVRA_JWT_ISSUER
	Audience string // MOTIVRA_JWT_AUDIENCE
}

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
}

// Load reads configuration from the process environment and applies
// development-friendly defaults. Use Validate to enforce environment rules.
func Load() Config {
	return Config{
		App: AppConfig{
			Name: envOr("MOTIVRA_APP_NAME", "motivra"),
			Env:  envOr("MOTIVRA_ENV", EnvDevelopment),
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
			Enabled:  envBool("MOTIVRA_OTEL_ENABLED", false),
		},
		ShutdownTimeout: envDuration("MOTIVRA_SHUTDOWN_TIMEOUT", 15*time.Second),
	}
}

// Validate aggregates every configuration problem into a single error so an
// operator sees all missing values at once instead of one boot failure at a
// time. Rules: database URL required except in test; JWT secret required and
// at least 32 bytes in production; port must be numeric in range.
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
	if c.App.Env == EnvProduction && len(c.JWT.Secret) < 32 {
		problems = append(problems, "MOTIVRA_JWT_SECRET must be at least 32 bytes in production")
	}
	if c.OTel.Enabled && c.OTel.Endpoint == "" {
		problems = append(problems, "OTEL_EXPORTER_OTLP_ENDPOINT is required when OTEL tracing is enabled")
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, "MOTIVRA_SHUTDOWN_TIMEOUT must be positive")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
}

// LoadForTest returns a Config valid for unit tests (no database required).
func LoadForTest() Config {
	return Config{
		App:             AppConfig{Name: "motivra-test", Env: EnvTest, Port: "0"},
		JWT:             JWTConfig{Secret: strings.Repeat("test-secret-", 4), Issuer: "motivra-identity", Audience: "motivra"},
		ShutdownTimeout: 5 * time.Second,
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
