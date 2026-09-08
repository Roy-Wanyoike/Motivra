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
		{"otel enabled without endpoint", func(c *Config) { c.OTel = OTelConfig{Enabled: true} }, "OTEL_EXPORTER_OTLP_ENDPOINT"},
		{"bad shutdown timeout", func(c *Config) { c.ShutdownTimeout = -1 }, "MOTIVRA_SHUTDOWN_TIMEOUT"},
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production config with 44-byte secret should validate, got %v", err)
	}
}
