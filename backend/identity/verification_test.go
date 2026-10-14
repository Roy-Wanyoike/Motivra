package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// newVerificationTestService wires a Service with the in-memory store and
// the dev token echo enabled, so tests can obtain raw verification tokens
// exactly the way a local (provider-less) deployment does.
func newVerificationTestService(mods ...func(*VerificationConfig)) (*Service, *fakeStore) {
	store := newFakeStore()
	cfg := DefaultVerificationConfig()
	cfg.DevEcho = true
	for _, mod := range mods {
		mod(&cfg)
	}
	return NewService(store, newTestIssuer(), nil, WithVerification(cfg)), store
}

// newVerificationTestAPI is newTestAPI with the dev token echo enabled.
func newVerificationTestAPI() (http.Handler, *Service, *fakeStore) {
	svc, store := newVerificationTestService()
	validator := platform.NewJWTValidator(testSecret, testIssuer, testAudience)
	cfg := platform.LoadForTest()
	srv := platform.NewServer(cfg, platform.NewLogger(cfg.App), platform.WithMiddleware(platform.AuthMiddleware(validator)))
	Routes(srv.Router, svc, nil)
	return srv, svc, store
}

// echoFor picks the echo of one channel or fails the test.
func echoFor(t *testing.T, resp AccessResponse, channel VerificationChannel) VerificationEcho {
	t.Helper()
	for _, e := range resp.Verification {
		if e.Channel == channel {
			return e
		}
	}
	t.Fatalf("no verification echo for channel %s in %+v", channel, resp.Verification)
	return VerificationEcho{}
}

// postAuthedJSON sends a JSON POST with a Bearer access token.
func postAuthedJSON(t *testing.T, handler http.Handler, accessToken, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestRegisterIssuesVerificationTokens(t *testing.T) {
	svc, store := newVerificationTestService()

	resp, err := svc.Register(t.Context(), "verify@example.com", "+254700000010", "strong-passphrase", "Verifier", "", "")
	require.NoError(t, err)
	require.Len(t, resp.Verification, 2, "one token per present channel: email + phone")
	require.Len(t, store.verificationTokens(), 2)

	emailEcho := echoFor(t, resp, ChannelEmail)
	phoneEcho := echoFor(t, resp, ChannelPhone)
	require.NotEqual(t, emailEcho.Token, phoneEcho.Token)
	require.True(t, emailEcho.ExpiresAt.After(time.Now().Add(DefaultVerificationTokenTTL-time.Minute)))

	// The raw token is never persisted; the row carries the keyed digest.
	user, err := store.GetUserByEmail(t.Context(), "verify@example.com")
	require.NoError(t, err)
	for _, row := range store.verificationTokens() {
		require.Equal(t, user.ID, row.UserID)
		require.NotContains(t, row.TokenHash, emailEcho.Token, "only the HMAC digest is stored")
		require.Nil(t, row.ConsumedAt)
	}
	require.Equal(t, 2, len(store.auditsByAction(ActionVerificationIssued)),
		"issuance is audited per channel")

	// A user without a phone gets exactly one token.
	svc2, store2 := newVerificationTestService()
	resp2, err := svc2.Register(t.Context(), "emailonly@example.com", "", "strong-passphrase", "Email Only", "", "")
	require.NoError(t, err)
	require.Len(t, resp2.Verification, 1)
	require.Equal(t, ChannelEmail, resp2.Verification[0].Channel)
	require.Len(t, store2.verificationTokens(), 1)
}

func TestVerifyConsumesTokenSingleUse(t *testing.T) {
	svc, store := newVerificationTestService()

	resp, err := svc.Register(t.Context(), "single@example.com", "", "strong-passphrase", "Single", "", "")
	require.NoError(t, err)
	echo := echoFor(t, resp, ChannelEmail)
	user, err := store.GetUserByEmail(t.Context(), "single@example.com")
	require.NoError(t, err)
	require.Nil(t, user.EmailVerifiedAt)

	channel, err := svc.Verify(t.Context(), echo.Token)
	require.NoError(t, err)
	require.Equal(t, ChannelEmail, channel)

	user, err = store.GetUserByEmail(t.Context(), "single@example.com")
	require.NoError(t, err)
	require.NotNil(t, user.EmailVerifiedAt, "verification stamps the channel")
	require.Len(t, store.auditsByAction(ActionVerificationVerified), 1)

	// Single use: the same token never verifies (or reveals state) again.
	_, err = svc.Verify(t.Context(), echo.Token)
	requireUnauthorized(t, err, "a consumed token is rejected")

	// Unknown tokens are indistinguishable from consumed ones.
	_, err = svc.Verify(t.Context(), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0")
	requireUnauthorized(t, err)

	// Empty token is a validation error, not a 401.
	_, err = svc.Verify(t.Context(), "")
	require.Equal(t, 422, appErr(t, err).Status)
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	svc, store := newVerificationTestService()

	resp, err := svc.Register(t.Context(), "expired@example.com", "", "strong-passphrase", "Expired", "", "")
	require.NoError(t, err)
	echo := echoFor(t, resp, ChannelEmail)
	require.Len(t, store.verificationTokens(), 1)
	store.expireVerificationToken(store.verificationTokens()[0].ID)

	_, err = svc.Verify(t.Context(), echo.Token)
	requireUnauthorized(t, err, "expired tokens are rejected")
	user, err := store.GetUserByEmail(t.Context(), "expired@example.com")
	require.NoError(t, err)
	require.Nil(t, user.EmailVerifiedAt, "nothing is stamped for an expired token")
}

func TestVerifyPhoneChannel(t *testing.T) {
	svc, store := newVerificationTestService()

	resp, err := svc.Register(t.Context(), "phone@example.com", "+254700000012", "strong-passphrase", "Phone", "", "")
	require.NoError(t, err)
	phoneEcho := echoFor(t, resp, ChannelPhone)

	channel, err := svc.Verify(t.Context(), phoneEcho.Token)
	require.NoError(t, err)
	require.Equal(t, ChannelPhone, channel)
	user, err := store.GetUserByEmail(t.Context(), "phone@example.com")
	require.NoError(t, err)
	require.Nil(t, user.EmailVerifiedAt, "verifying the phone token does not touch email")
	require.NotNil(t, user.PhoneVerifiedAt)
}

// TestVerificationGraceBoundary pins both sides of the issue #86 rule:
// within the grace window an unverified account can still rotate sessions;
// once it ends, refresh is refused UNTIL the account is verified — and the
// refused refresh must not have consumed the presented session.
func TestVerificationGraceBoundary(t *testing.T) {
	store := newFakeStore()
	issuer := NewIssuer(testSecret, testIssuer, testAudience, WithVerificationGrace(time.Hour))
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err, "fresh issuance inside the grace window works")

	// Just before the boundary: rotation still works.
	store.ageUser(user.ID, time.Now().Add(-time.Hour+time.Minute))
	rotated, _, err := issuer.Refresh(t.Context(), store, resp.RefreshToken)
	require.NoError(t, err)

	// Just after the boundary: refresh is refused with 403...
	store.ageUser(user.ID, time.Now().Add(-time.Hour-time.Minute))
	_, _, err = issuer.Refresh(t.Context(), store, rotated.RefreshToken)
	e := appErr(t, err)
	require.Equal(t, 403, e.Status)
	require.Contains(t, e.Detail, "verification")

	// ...without consuming the presented session: after verification the
	// SAME token refreshes again.
	tokenID := uuid.New()
	require.NoError(t, store.CreateVerificationToken(t.Context(), VerificationToken{
		ID:        tokenID,
		UserID:    user.ID,
		Channel:   ChannelEmail,
		TokenHash: "grace-boundary-test-hash",
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, store.ConsumeVerificationToken(t.Context(), tokenID, user.ID, ChannelEmail, time.Now()))
	_, _, err = issuer.Refresh(t.Context(), store, rotated.RefreshToken)
	require.NoError(t, err, "refresh works again once the channel is verified")

	// A fresh login is gated identically: unverified past grace cannot
	// obtain a new refresh session. Login loads the account fresh from
	// the store (service.go Login), so the test reloads the aged user
	// the same way instead of passing the stale registration struct.
	store2 := newFakeStore()
	require.NoError(t, store2.CreateUser(t.Context(), &user))
	store2.ageUser(user.ID, time.Now().Add(-2*time.Hour))
	aged, err := store2.GetUser(t.Context(), user.ID)
	require.NoError(t, err)
	_, err = issuer.Issue(t.Context(), store2, aged, "", "")
	require.Equal(t, 403, appErr(t, err).Status, "login cannot mint refresh sessions past the grace window")
}

func TestGraceZeroDisablesGate(t *testing.T) {
	store := newFakeStore()
	issuer := NewIssuer(testSecret, testIssuer, testAudience, WithVerificationGrace(0))
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))
	store.ageUser(user.ID, time.Now().Add(-100*24*time.Hour))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err, "grace 0 disables the verification gate")
	_, _, err = issuer.Refresh(t.Context(), store, resp.RefreshToken)
	require.NoError(t, err)
}

func TestServiceLoginAndRefreshGatedPastGrace(t *testing.T) {
	svc, store := newVerificationTestService()
	registered, err := svc.Register(t.Context(), "gated@example.com", "", "strong-passphrase", "Gated", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "gated@example.com")
	require.NoError(t, err)

	// Age the account past the default 24h grace window.
	store.ageUser(user.ID, time.Now().Add(-25*time.Hour))

	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	require.Equal(t, 403, appErr(t, err).Status, "unverified accounts cannot rotate past the grace window")
	_, err = svc.Login(t.Context(), "gated@example.com", "strong-passphrase", "", "")
	require.Equal(t, 403, appErr(t, err).Status, "unverified accounts cannot obtain fresh sessions past the grace window")

	// Verify through the flow; the gate lifts.
	echo := echoFor(t, registered, ChannelEmail)
	_, err = svc.Verify(t.Context(), echo.Token)
	require.NoError(t, err)
	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	require.NoError(t, err, "verified accounts refresh again")
	_, err = svc.Login(t.Context(), "gated@example.com", "strong-passphrase", "", "")
	require.NoError(t, err, "verified accounts log in again")
}

func TestResendVerificationCooldownAndLimit(t *testing.T) {
	svc, store := newVerificationTestService(func(c *VerificationConfig) {
		c.ResendLimit = 3
	})
	registered, err := svc.Register(t.Context(), "resend@example.com", "", "strong-passphrase", "Resend", "", "")
	require.NoError(t, err)
	require.Len(t, store.auditsByAction(ActionVerificationIssued), 1, "registration issuance")

	// Immediate resend trips the cooldown (the registration token is the
	// latest issuance for the channel).
	_, _, err = svc.ResendVerification(t.Context(), "resend@example.com", ChannelEmail)
	require.Equal(t, 429, appErr(t, err).Status, "cooldown refuses an immediate resend")
	require.Empty(t, store.auditsByAction(ActionVerificationResent))

	// Backdate the latest token past the cooldown: the resend succeeds.
	store.backdateVerificationToken(store.verificationTokens()[0].ID, time.Now().Add(-2*time.Minute))
	channel, echo, err := svc.ResendVerification(t.Context(), "RESEND@Example.com", ChannelEmail)
	require.NoError(t, err, "email matching is case-insensitive")
	require.Equal(t, ChannelEmail, channel)
	require.NotNil(t, echo, "dev echo carries the resent token")
	require.Len(t, store.auditsByAction(ActionVerificationResent), 1)
	require.Len(t, store.auditsByAction(ActionVerificationIssued), 1, "resends audit as resent, not issued")
	require.Len(t, store.verificationTokens(), 2)

	// The resent token verifies like any other.
	ch, err := svc.Verify(t.Context(), echo.Token)
	require.NoError(t, err)
	require.Equal(t, ChannelEmail, ch)
	_ = registered

	// Rolling limit: 3 tokens per 24h (1 registration + 2 resends).
	svc2, store2 := newVerificationTestService(func(c *VerificationConfig) {
		c.ResendCooldown = 0
		c.ResendLimit = 3
	})
	_, err = svc2.Register(t.Context(), "limit@example.com", "", "strong-passphrase", "Limit", "", "")
	require.NoError(t, err)
	for i := 0; i < 2; i++ { // two more resends reach the limit
		_, _, err = svc2.ResendVerification(t.Context(), "limit@example.com", ChannelEmail)
		require.NoError(t, err)
	}
	_, _, err = svc2.ResendVerification(t.Context(), "limit@example.com", ChannelEmail)
	require.Equal(t, 429, appErr(t, err).Status, "the rolling limit refuses further tokens")
	require.Len(t, store2.verificationTokens(), 3)
	require.Equal(t, 2, len(store2.auditsByAction(ActionVerificationResent)))
}

func TestResendNeutralForUnknownEmailAndSatisfiedChannels(t *testing.T) {
	svc, store := newVerificationTestService()

	// Unknown email: neutral success, no write, no audit (anti-enumeration).
	channel, echo, err := svc.ResendVerification(t.Context(), "ghost@example.com", ChannelEmail)
	require.NoError(t, err)
	require.Equal(t, ChannelEmail, channel)
	require.Nil(t, echo)
	require.Empty(t, store.verificationTokens())
	require.Empty(t, store.audits)

	// Validation errors.
	_, _, err = svc.ResendVerification(t.Context(), "", ChannelEmail)
	require.Equal(t, 422, appErr(t, err).Status)
	_, _, err = svc.ResendVerification(t.Context(), "x@example.com", "carrier-pigeon")
	require.Equal(t, 422, appErr(t, err).Status)

	// Phone resend for an account without a phone: neutral no-op.
	_, err = svc.Register(t.Context(), "nophone@example.com", "", "strong-passphrase", "No Phone", "", "")
	require.NoError(t, err)
	channel, echo, err = svc.ResendVerification(t.Context(), "nophone@example.com", ChannelPhone)
	require.NoError(t, err)
	require.Nil(t, echo)
	require.Len(t, store.verificationTokens(), 1, "only the registration email token exists")

	// Already-verified channels do not consume resend budget.
	resp, err := svc.Register(t.Context(), "done@example.com", "+254700000013", "strong-passphrase", "Done", "", "")
	require.NoError(t, err)
	_, err = svc.Verify(t.Context(), echoFor(t, resp, ChannelEmail).Token)
	require.NoError(t, err)
	before := len(store.verificationTokens())
	_, echo, err = svc.ResendVerification(t.Context(), "done@example.com", ChannelEmail)
	require.NoError(t, err)
	require.Nil(t, echo)
	require.Len(t, store.verificationTokens(), before, "no token for a verified channel")
}

func TestVerificationHTTPFlow(t *testing.T) {
	router, _, store := newVerificationTestAPI()

	rec := postJSON(t, router, "/v1/auth/register",
		`{"email":"httpverify@api.test","password":"strong-passphrase","full_name":"HTTP","phone":"+254700000014"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	var registered AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registered))
	require.Len(t, registered.Verification, 2, "dev echo carries email + phone tokens")
	emailEcho := echoFor(t, registered, ChannelEmail)

	// Verify endpoint: 200 with the channel, then 401 on replay.
	rec = postJSON(t, router, "/v1/auth/verify", `{"token":"`+emailEcho.Token+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"channel":"email"}`, rec.Body.String())
	rec = postJSON(t, router, "/v1/auth/verify", `{"token":"`+emailEcho.Token+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "single use at the HTTP boundary")
	rec = postJSON(t, router, "/v1/auth/verify", `{"token":"no-such-token"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = postJSON(t, router, "/v1/auth/verify", `{"token":""}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Profile now reports the verified channel.
	rec = getAuthed(t, router, "/v1/users/me", registered.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	var profile map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	require.NotNil(t, profile["email_verified_at"])
	require.Nil(t, profile["phone_verified_at"])

	// Resend endpoint: cooldown trips right after registration (429), a
	// backdated token allows the resend (202 + echo), the resent token
	// verifies the phone channel. The backdate targets the PHONE token
	// — the cooldown is per user+channel, so moving the email token's
	// creation would not lift the phone channel's cooldown.
	rec = postJSON(t, router, "/v1/auth/verification/resend", `{"email":"httpverify@api.test","channel":"phone"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	for _, tok := range store.verificationTokens() {
		if tok.Channel == ChannelPhone {
			store.backdateVerificationToken(tok.ID, time.Now().Add(-2*time.Minute))
		}
	}
	rec = postJSON(t, router, "/v1/auth/verification/resend", `{"email":"httpverify@api.test","channel":"phone"}`)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var resent struct {
		Channel string `json:"channel"`
		Token   string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resent))
	require.Equal(t, "phone", resent.Channel)
	require.NotEmpty(t, resent.Token)
	require.Len(t, store.auditsByAction(ActionVerificationResent), 1)

	rec = postJSON(t, router, "/v1/auth/verify", `{"token":"`+resent.Token+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"channel":"phone"}`, rec.Body.String())

	// Unknown email resends are neutral 202s that leak nothing.
	rec = postJSON(t, router, "/v1/auth/verification/resend", `{"email":"ghost@api.test"}`)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.JSONEq(t, `{"channel":"email"}`, rec.Body.String())

	// Invalid channel is a 422.
	rec = postJSON(t, router, "/v1/auth/verification/resend", `{"email":"httpverify@api.test","channel":"fax"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestVerificationGraceHTTPBoundary(t *testing.T) {
	router, svc, store := newVerificationTestAPI()

	rec := postJSON(t, router, "/v1/auth/register",
		`{"email":"grace@api.test","password":"strong-passphrase","full_name":"Grace"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	var registered AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registered))
	emailEcho := echoFor(t, registered, ChannelEmail)

	// Inside the grace window: refresh works.
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+registered.RefreshToken+`"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var rotated AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rotated))

	// Past the grace window: 403 until verified.
	user, err := store.GetUserByEmail(t.Context(), "grace@api.test")
	require.NoError(t, err)
	store.ageUser(user.ID, time.Now().Add(-25*time.Hour))
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+rotated.RefreshToken+`"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, "refresh is refused once the grace window ends")
	require.Contains(t, rec.Body.String(), "verification")

	// Verify: the same refresh token works again (the refusal consumed
	// nothing).
	_, err = svc.Verify(t.Context(), emailEcho.Token)
	require.NoError(t, err)
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+rotated.RefreshToken+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "refresh works again after verification")
}

func TestPasswordChangeHTTP(t *testing.T) {
	router, _, store := newVerificationTestAPI()

	rec := postJSON(t, router, "/v1/auth/register",
		`{"email":"pwchange@api.test","password":"original-pass-1","full_name":"PW"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	var registered AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &registered))

	// Unauthenticated change is a 401.
	rec = postJSON(t, router, "/v1/auth/password", `{"current_password":"original-pass-1","new_password":"replacement-pass-1"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Wrong current password is a 401.
	rec = postAuthedJSON(t, router, registered.AccessToken, "/v1/auth/password",
		`{"current_password":"wrong-current-1","new_password":"replacement-pass-1"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Successful change: 204, sessions swept, audit written.
	rec = postAuthedJSON(t, router, registered.AccessToken, "/v1/auth/password",
		`{"current_password":"original-pass-1","new_password":"replacement-pass-1"}`)
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+registered.RefreshToken+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "the password change revoked the caller's session")
	require.Len(t, store.auditsByAction(ActionPasswordChanged), 1)

	// The new password logs in.
	rec = postJSON(t, router, "/v1/auth/login", `{"email":"pwchange@api.test","password":"replacement-pass-1"}`)
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestVerificationConfigValidate pins the boot-time validation contract.
func TestVerificationConfigValidate(t *testing.T) {
	valid := DefaultVerificationConfig()
	require.NoError(t, valid.Validate(platform.EnvDevelopment))
	require.NoError(t, valid.Validate(platform.EnvProduction))

	echoProd := valid
	echoProd.DevEcho = true
	require.Error(t, echoProd.Validate(platform.EnvProduction), "the token echo must not boot in production")
	require.Error(t, echoProd.Validate(platform.EnvStaging))
	require.NoError(t, echoProd.Validate(platform.EnvDevelopment))

	noGrace := valid
	noGrace.Grace = 0
	require.NoError(t, noGrace.Validate(platform.EnvProduction), "grace 0 is the documented opt-out")

	negative := valid
	negative.Grace = -time.Minute
	require.Error(t, negative.Validate(platform.EnvDevelopment))
	zeroTTL := valid
	zeroTTL.TokenTTL = 0
	require.Error(t, zeroTTL.Validate(platform.EnvDevelopment))
	zeroLimit := valid
	zeroLimit.ResendLimit = 0
	require.Error(t, zeroLimit.Validate(platform.EnvDevelopment))
	negativeCooldown := valid
	negativeCooldown.ResendCooldown = -time.Second
	require.Error(t, negativeCooldown.Validate(platform.EnvDevelopment))
}

// TestLoadVerificationConfigAndBreach pins the env parsing of the identity
// contract variables.
func TestLoadVerificationConfigAndBreach(t *testing.T) {
	t.Setenv("MOTIVRA_VERIFICATION_GRACE", "2h")
	t.Setenv("MOTIVRA_VERIFICATION_TOKEN_TTL", "4h")
	t.Setenv("MOTIVRA_VERIFICATION_RESEND_COOLDOWN", "90s")
	t.Setenv("MOTIVRA_VERIFICATION_RESEND_LIMIT", "7")
	t.Setenv("MOTIVRA_VERIFICATION_DEV_ECHO", "true")
	t.Setenv("MOTIVRA_HIBP_ENABLED", "true")
	t.Setenv("MOTIVRA_BREACH_DENYLIST_FILE", "/tmp/denylist.txt")
	t.Setenv("MOTIVRA_BREACH_FAIL_CLOSED", "true")

	vcfg := LoadVerificationConfig()
	require.Equal(t, 2*time.Hour, vcfg.Grace)
	require.Equal(t, 4*time.Hour, vcfg.TokenTTL)
	require.Equal(t, 90*time.Second, vcfg.ResendCooldown)
	require.Equal(t, 7, vcfg.ResendLimit)
	require.True(t, vcfg.DevEcho)

	bcfg := LoadBreachConfig()
	require.True(t, bcfg.HIBPEnabled)
	require.Equal(t, "/tmp/denylist.txt", bcfg.DenylistFile)
	require.True(t, bcfg.FailClosed)

	// Unset values fall back to the defaults.
	t.Setenv("MOTIVRA_VERIFICATION_GRACE", "")
	require.Equal(t, DefaultVerificationGrace, LoadVerificationConfig().Grace)
}

// TestSessionStoreImplementationGuards keeps every store honest about the
// full SessionStore surface added by the breach/verification work.
func TestSessionStoreImplementationGuards(t *testing.T) {
	var _ SessionStore = (*fakeStore)(nil)
	var _ SessionStore = (*PostgresStore)(nil)
	var _ PasswordBreacher = (*HIBPChecker)(nil)
	var _ PasswordBreacher = (*DenylistChecker)(nil)
	var _ PasswordBreacher = (*ChainBreacher)(nil)
}
