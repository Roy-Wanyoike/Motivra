package identity

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Identity-service environment contract (issue #86). These variables are
// identity-specific, so they live here rather than in the shared
// platform.Config: every other service validates tokens but never performs
// breach checks or verification issuance. They are documented for operators
// in docs/RUNBOOK.md §6.

// Verification defaults (issue #86). The grace window and token TTL mirror
// each other: a token issued at registration stays valid for as long as
// the unverified account may still use sessions.
const (
	DefaultVerificationTokenTTL   = 24 * time.Hour
	DefaultResendCooldown         = time.Minute
	DefaultResendLimit            = 5
	verificationResendLimitWindow = 24 * time.Hour
)

// VerificationConfig holds the email/phone verification flow settings.
type VerificationConfig struct {
	// Grace is the post-registration window during which an unverified
	// account may still mint refresh sessions (MOTIVRA_VERIFICATION_GRACE,
	// default 24h; 0 disables the gate).
	Grace time.Duration
	// TokenTTL is the validity of a verification token
	// (MOTIVRA_VERIFICATION_TOKEN_TTL, default 24h).
	TokenTTL time.Duration
	// ResendCooldown is the minimum spacing between verification tokens for
	// the same user and channel (MOTIVRA_VERIFICATION_RESEND_COOLDOWN,
	// default 1m).
	ResendCooldown time.Duration
	// ResendLimit is the maximum verification tokens per user and channel
	// within a rolling 24h window (MOTIVRA_VERIFICATION_RESEND_LIMIT,
	// default 5).
	ResendLimit int
	// DevEcho echoes raw verification tokens in register/resend responses so
	// local flows work without an email/SMS provider
	// (MOTIVRA_VERIFICATION_DEV_ECHO, default false; refused at boot in
	// staging and production — tokens must be delivered out-of-band there).
	DevEcho bool
}

// DefaultVerificationConfig returns the development-friendly defaults.
func DefaultVerificationConfig() VerificationConfig {
	return VerificationConfig{
		Grace:          DefaultVerificationGrace,
		TokenTTL:       DefaultVerificationTokenTTL,
		ResendCooldown: DefaultResendCooldown,
		ResendLimit:    DefaultResendLimit,
		DevEcho:        false,
	}
}

// LoadVerificationConfig reads the verification flow configuration from the
// process environment, falling back to the defaults for unset values.
func LoadVerificationConfig() VerificationConfig {
	cfg := DefaultVerificationConfig()
	if v := strings.TrimSpace(os.Getenv("MOTIVRA_VERIFICATION_GRACE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.Grace = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOTIVRA_VERIFICATION_TOKEN_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.TokenTTL = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOTIVRA_VERIFICATION_RESEND_COOLDOWN")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.ResendCooldown = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MOTIVRA_VERIFICATION_RESEND_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ResendLimit = n
		}
	}
	cfg.DevEcho = envBool("MOTIVRA_VERIFICATION_DEV_ECHO", false)
	return cfg
}

// Validate aggregates the configuration problems that must refuse boot.
// devEnv reports the configured MOTIVRA_ENV so the dev-only token echo can
// be refused anywhere it would leak verification capability.
func (c VerificationConfig) Validate(devEnv string) error {
	var problems []string
	if c.Grace < 0 {
		problems = append(problems, "MOTIVRA_VERIFICATION_GRACE must not be negative (0 disables the gate)")
	}
	if c.TokenTTL <= 0 {
		problems = append(problems, "MOTIVRA_VERIFICATION_TOKEN_TTL must be positive")
	}
	if c.ResendCooldown < 0 {
		problems = append(problems, "MOTIVRA_VERIFICATION_RESEND_COOLDOWN must not be negative")
	}
	if c.ResendLimit <= 0 {
		problems = append(problems, "MOTIVRA_VERIFICATION_RESEND_LIMIT must be positive")
	}
	if c.DevEcho && (devEnv == platform.EnvStaging || devEnv == platform.EnvProduction) {
		problems = append(problems,
			"MOTIVRA_VERIFICATION_DEV_ECHO must not be enabled in staging or production: verification tokens must be delivered out-of-band there")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid verification configuration: %s", strings.Join(problems, "; "))
}

// BreachConfig holds the breached-password check settings (ADR-0004 breach
// policy, issue #86).
type BreachConfig struct {
	// HIBPEnabled turns on the HIBP k-anonymity lookup in addition to the
	// always-on local denylist (MOTIVRA_HIBP_ENABLED, default false).
	HIBPEnabled bool
	// DenylistFile is an optional path of extra denied passwords, one per
	// line (MOTIVRA_BREACH_DENYLIST_FILE). An unreadable file fails boot.
	DenylistFile string
	// FailClosed selects the availability policy when the checker errors:
	// false (default) fails open and writes an audit note; true refuses the
	// registration/password change while the check is unavailable
	// (MOTIVRA_BREACH_FAIL_CLOSED).
	FailClosed bool
}

// LoadBreachConfig reads the breach-check configuration from the process
// environment.
func LoadBreachConfig() BreachConfig {
	return BreachConfig{
		HIBPEnabled:  envBool("MOTIVRA_HIBP_ENABLED", false),
		DenylistFile: strings.TrimSpace(os.Getenv("MOTIVRA_BREACH_DENYLIST_FILE")),
		FailClosed:   envBool("MOTIVRA_BREACH_FAIL_CLOSED", false),
	}
}

// envBool parses a boolean environment variable with a fallback; it mirrors
// the platform helpers, which are deliberately not exported (the identity
// env contract stays identity-local).
func envBool(key string, fallback bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
