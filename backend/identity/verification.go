package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Verification flow (issue #86). Registration issues one single-use token
// per present contact channel; the raw token is delivered out-of-band
// (email/SMS provider — delivery integration lands with the notifications
// wave, so local/dev flows use MOTIVRA_VERIFICATION_DEV_ECHO) and only its
// keyed HMAC digest is stored. POST /v1/auth/verify consumes a token and
// stamps the channel; POST /v1/auth/verification/resend re-issues one
// within cooldown + limit. Until every present channel is verified,
// session minting is gated to the post-registration grace window by the
// Issuer (see tokens.go).

// VerificationEcho is the dev-only echo of an issued verification token.
// It is populated in API responses exclusively when
// MOTIVRA_VERIFICATION_DEV_ECHO is enabled (never in staging/production —
// echoing a token in a response would let a caller verify a contact
// address without receiving its out-of-band message).
type VerificationEcho struct {
	Channel   VerificationChannel `json:"channel"`
	Token     string              `json:"token"`
	ExpiresAt time.Time           `json:"expires_at"`
}

// errInvalidVerificationToken is the single rejection for unknown, expired,
// consumed and raced verification tokens: token state is never disclosed.
func errInvalidVerificationToken() *platform.Error {
	return platform.ErrUnauthorized("verification token is invalid or expired")
}

// errResendLimited renders the resend cooldown/limit refusals as 429s,
// matching the rate limiter's problem code.
func errResendLimited(detail string) *platform.Error {
	return &platform.Error{
		Status: http.StatusTooManyRequests,
		Code:   "rate_limited",
		Title:  "Too many requests",
		Detail: detail,
	}
}

// issueVerificationToken creates one single-use token row for the user's
// channel, audits the issuance (action depends on via: registration or
// resend) and returns the raw client token with its expiry. Audit failures
// fail the caller, per the audit-fails-request rule.
func (s *Service) issueVerificationToken(ctx context.Context, user User, channel VerificationChannel, via string) (string, time.Time, error) {
	raw, err := newOpaqueToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expiresAt := now.Add(s.verification.TokenTTL)
	token := VerificationToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		Channel:   channel,
		TokenHash: s.issuer.TokenHash(raw),
		ExpiresAt: expiresAt,
	}
	if err := s.store.CreateVerificationToken(ctx, token); err != nil {
		return "", time.Time{}, fmt.Errorf("create verification token: %w", err)
	}
	action := ActionVerificationIssued
	if via == "resend" {
		action = ActionVerificationResent
	}
	if err := s.audit(ctx, user.ID, action, "user", user.ID.String(), map[string]any{
		"channel":    string(channel),
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	}); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

// Verify consumes a single-use verification token and marks the owning
// user's channel verified. Unknown, expired, already-consumed and
// concurrently-consumed tokens return the identical unauthorized error.
// The consumption and the user flag update land in one store transaction.
func (s *Service) Verify(ctx context.Context, rawToken string) (VerificationChannel, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return "", platform.ErrValidation("token is required",
			platform.FieldError{Field: "token", Issue: "required"})
	}
	token, err := s.store.GetVerificationTokenByHash(ctx, s.issuer.TokenHash(rawToken))
	if err != nil {
		if isNotFound(err) {
			return "", errInvalidVerificationToken()
		}
		return "", fmt.Errorf("lookup verification token: %w", err)
	}
	now := time.Now()
	if token.ConsumedAt != nil || !now.Before(token.ExpiresAt) {
		return "", errInvalidVerificationToken()
	}
	if err := s.store.ConsumeVerificationToken(ctx, token.ID, token.UserID, token.Channel, now); err != nil {
		if errors.Is(err, ErrVerificationTokenConsumed) {
			return "", errInvalidVerificationToken()
		}
		return "", fmt.Errorf("consume verification token: %w", err)
	}
	if err := s.audit(ctx, token.UserID, ActionVerificationVerified, "user", token.UserID.String(), map[string]any{
		"channel": string(token.Channel),
	}); err != nil {
		return "", err
	}
	return token.Channel, nil
}

// ResendVerification re-issues a verification token for the account behind
// email. To keep the endpoint enumeration-safe it returns a neutral success
// (handled=false, no error) for unknown emails — and for known accounts on
// channels that need no token (already verified, or phone requested for an
// account without one). Rate abuse is bounded by the per-user cooldown and
// rolling limit plus the per-IP auth limiter; exceeding either is a 429.
// The returned echo is non-nil only when MOTIVRA_VERIFICATION_DEV_ECHO is
// enabled AND a token was actually issued.
func (s *Service) ResendVerification(ctx context.Context, email string, channel VerificationChannel) (VerificationChannel, *VerificationEcho, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", nil, platform.ErrValidation("email is required",
			platform.FieldError{Field: "email", Issue: "required"})
	}
	if channel == "" {
		channel = ChannelEmail
	}
	if !IsValidVerificationChannel(channel) {
		return "", nil, platform.ErrValidation("channel must be email or phone",
			platform.FieldError{Field: "channel", Issue: "invalid"})
	}

	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if isNotFound(err) {
			// Anti-enumeration: identical 202 shape, no write, no audit row.
			return channel, nil, nil
		}
		return "", nil, fmt.Errorf("lookup user: %w", err)
	}
	if channel == ChannelEmail && user.EmailVerifiedAt != nil {
		return channel, nil, nil
	}
	if channel == ChannelPhone && (user.Phone == "" || user.PhoneVerifiedAt != nil) {
		return channel, nil, nil
	}

	now := time.Now()
	count, latest, err := s.store.VerificationTokenStats(ctx, user.ID, channel, now.Add(-verificationResendLimitWindow))
	if err != nil {
		return "", nil, err
	}
	if !latest.IsZero() && s.verification.ResendCooldown > 0 && now.Sub(latest) < s.verification.ResendCooldown {
		retryIn := (s.verification.ResendCooldown - now.Sub(latest)).Round(time.Second)
		return "", nil, errResendLimited(fmt.Sprintf(
			"verification resend cooldown active for this channel; retry in %s", retryIn))
	}
	if count >= s.verification.ResendLimit {
		return "", nil, errResendLimited(fmt.Sprintf(
			"verification resend limit reached for this channel; at most %d tokens per 24h",
			s.verification.ResendLimit))
	}

	raw, expiresAt, err := s.issueVerificationToken(ctx, user, channel, "resend")
	if err != nil {
		return "", nil, err
	}
	var echo *VerificationEcho
	if s.verification.DevEcho {
		echo = &VerificationEcho{Channel: channel, Token: raw, ExpiresAt: expiresAt}
	}
	return channel, echo, nil
}
