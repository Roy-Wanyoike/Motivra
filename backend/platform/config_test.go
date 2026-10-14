package platform

import (
        "strings"
        "testing"
        "time"
)

func TestLoadDefaults(t *testing.T) {
        t.Setenv("MOTIVRA_APP_NAME", "")
        cfg := Load()
        if cfg.App.Name != "motivra" || cfg.App.Port != "8080" {
                t.Fatalf("unexpected defaults: %+v", cfg.App)
        }
        if cfg.ShutdownTimeout != 15*time.Second {
                t.Fatalf("default shutdown timeout = %v", cfg.ShutdownTimeout)
        }
}

// PORT is the PaaS/container convention (platforms inject the listen port
// as PORT). MOTIVRA_PORT must still win; PORT applies only when
// MOTIVRA_PORT is unset; 8080 is the compose default.
func TestLoadPortFallback(t *testing.T) {
        t.Setenv("MOTIVRA_PORT", "")
        t.Setenv("PORT", "3000")
        if cfg := Load(); cfg.App.Port != "3000" {
                t.Fatalf("PORT fallback not applied: port = %q", cfg.App.Port)
        }

        t.Setenv("MOTIVRA_PORT", "9090")
        if cfg := Load(); cfg.App.Port != "9090" {
                t.Fatalf("MOTIVRA_PORT must win over PORT: port = %q", cfg.App.Port)
        }

        t.Setenv("MOTIVRA_PORT", "")
        t.Setenv("PORT", "")
        if cfg := Load(); cfg.App.Port != "8080" {
                t.Fatalf("default port not applied: port = %q", cfg.App.Port)
        }
}

func TestLoadOverrides(t *testing.T) {
        t.Setenv("MOTIVRA_APP_NAME", "identity")
        t.Setenv("MOTIVRA_ENV", "production")
        t.Setenv("MOTIVRA_PORT", "9090")
        t.Setenv("MOTIVRA_SHUTDOWN_TIMEOUT", "30s")
        t.Setenv("MOTIVRA_OTEL_ENABLED", "true")
        t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")

        cfg := Load()
        if cfg.App.Name != "identity" || cfg.App.Env != "production" || cfg.App.Port != "9090" {
                t.Fatalf("override not applied: %+v", cfg.App)
        }
        if cfg.ShutdownTimeout != 30*time.Second || !cfg.OTel.Enabled {
                t.Fatalf("override not applied: timeout=%v otel=%v", cfg.ShutdownTimeout, cfg.OTel.Enabled)
        }
}

func TestValidate(t *testing.T) {
        tests := []struct {
                name    string
                mutate  func(*Config)
                wantErr string
        }{
                {"valid test config", func(c *Config) {}, ""},
                {"missing database outside test", func(c *Config) { c.App.Env = EnvDevelopment; c.Database.URL = "" }, "MOTIVRA_DATABASE_URL"},
                {"bad port", func(c *Config) { c.App.Port = "99999" }, "MOTIVRA_PORT"},
                {"unknown env", func(c *Config) { c.App.Env = "stagingx" }, "MOTIVRA_ENV"},
                {"short secret in production", func(c *Config) { c.App.Env = EnvProduction; c.JWT.Secret = "short" }, "MOTIVRA_JWT_SECRET"},
                {"no verification keys in staging (ADR-0007)", func(c *Config) { c.App.Env = EnvStaging; c.JWT.Secret = "" }, "token verification keys"},
                {"allow_hs256 without secret", func(c *Config) { c.App.Env = EnvStaging; c.JWT.Secret = ""; c.JWT.AllowHS256 = true }, "MOTIVRA_JWT_ALLOW_HS256 requires MOTIVRA_JWT_SECRET"},
                {"legacy dev placeholder in production", func(c *Config) {
                        c.App.Env = EnvProduction
                        c.JWT.Secret = legacyDevJWTSecret
                }, "historical development placeholder"},
                {"legacy dev placeholder in development", func(c *Config) {
                        c.App.Env = EnvDevelopment
                        c.JWT.Secret = legacyDevJWTSecret
                        c.Database.URL = "postgres://localhost/motivra"
                }, "historical development placeholder"},
                {"otel enabled without endpoint", func(c *Config) { c.OTel = OTelConfig{Enabled: true} }, "OTEL_EXPORTER_OTLP_ENDPOINT"},
                {"bad shutdown timeout", func(c *Config) { c.ShutdownTimeout = -1 }, "MOTIVRA_SHUTDOWN_TIMEOUT"},
                {"negative rate limit", func(c *Config) { c.AuthRateLimitRPM = -5 }, "MOTIVRA_AUTH_RATE_LIMIT_RPM"},
        }
        for _, tc := range tests {
                t.Run(tc.name, func(t *testing.T) {
                        cfg := LoadForTest()
                        tc.mutate(&cfg)
                        err := cfg.Validate()
                        if tc.wantErr == "" {
                                if err != nil {
                                        t.Fatalf("expected valid, got %v", err)
                                }
                                return
                        }
                        if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
                                t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
                        }
                })
        }
}

func TestProductionSecretLengthAccepted(t *testing.T) {
        cfg := LoadForTest()
        cfg.App.Env = EnvProduction
        cfg.Database.URL = "postgres://localhost/motivra"
        // ADR-0007 posture: a production verifier holds public keys (JWKS by
        // default via Load()/NewValidatorFromConfig; explicit here because this
        // is a hand-built config).
        cfg.JWT.JWKSURL = DefaultJWKSURL
        if err := cfg.Validate(); err != nil {
                t.Fatalf("production config with 44-byte secret should validate, got %v", err)
        }
}

// TestValidateMigrationEscapeHatch pins the explicit HS256 opt-in: a
// staging/production deployment may keep the shared secret as its only
// verification source when the migration flag is set (never by default).
func TestValidateMigrationEscapeHatch(t *testing.T) {
        cfg := LoadForTest()
        cfg.App.Env = EnvProduction
        cfg.Database.URL = "postgres://localhost/motivra"
        cfg.JWT.AllowHS256 = true
        if err := cfg.Validate(); err != nil {
                t.Fatalf("HS256 migration posture should validate, got %v", err)
        }
}

// TestOTelDefaultsOnInProduction pins the ADR-0005 posture: a production
// deployment must not silently run without telemetry. Load() defaults the
// exporter on in production and off elsewhere unless the env var says so.
func TestOTelDefaultsOnInProduction(t *testing.T) {
        t.Setenv("MOTIVRA_ENV", "production")
        t.Setenv("MOTIVRA_OTEL_ENABLED", "")
        t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector:4317")
        if cfg := Load(); !cfg.OTel.Enabled {
                t.Fatal("OTel must default to enabled in production")
        }

        t.Setenv("MOTIVRA_ENV", "development")
        if cfg := Load(); cfg.OTel.Enabled {
                t.Fatal("OTel must default to disabled outside production")
        }
}
