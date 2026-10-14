package identity

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// identityTables are every table of the identity chain, truncated between
// tests so each starts from a clean schema.
var identityTables = []string{"audit_log", "verification_tokens", "sessions", "user_roles", "users"}

// integrationPool connects to the Postgres instance named by
// TEST_DATABASE_URL through the shared pgtest harness (issue #28, deferral
// 3), applies the identity migration chain and returns a pool plus a
// Service over it. Tests skip when the variable is unset.
func integrationPool(t *testing.T) (*platform.JWTValidator, *Service, *PostgresStore) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, identitymigrations.FS, "schema_migrations_identity")
	pgtest.Truncate(t, pool, identityTables...)

	store := NewPostgresStore(pool)
	validator := platform.NewJWTValidator(testSecret, testIssuer, testAudience)
	return validator, NewService(store, newTestIssuer(), nil), store
}

// truncateAll resets the identity tables after a test body ran.
func truncateAll(t *testing.T, store *PostgresStore) {
	t.Helper()
	pgtest.Truncate(t, store.pool, identityTables...)
}

func TestPostgresStoreFullFlow(t *testing.T) {
	validator, svc, store := integrationPool(t)

	resp, err := svc.Register(t.Context(), "Integration@Example.com", "+254700000009", "integration-pass", "Integration Tester", "ci-runner", "127.0.0.1")
	require.NoError(t, err)
	claims, err := validator.Validate(resp.AccessToken)
	require.NoError(t, err)
	userID, err := uuid.Parse(claims.UserID)
	require.NoError(t, err)

	// Duplicate email (case-insensitive) conflicts at the database level.
	_, err = svc.Register(t.Context(), "integration@example.com", "", "integration-pass", "Duplicate", "", "")
	require.Equal(t, 409, appErr(t, err).Status)

	loggedIn, err := svc.Login(t.Context(), "integration@example.com", "integration-pass", "ci-runner-2", "127.0.0.2")
	require.NoError(t, err)
	_, err = validator.Validate(loggedIn.AccessToken)
	require.NoError(t, err)

	rotated, err := svc.RefreshTokens(t.Context(), loggedIn.RefreshToken)
	require.NoError(t, err)
	_, err = svc.RefreshTokens(t.Context(), loggedIn.RefreshToken)
	requireUnauthorized(t, err)

	// Logout by refresh token (the public-endpoint path), then verify
	// the revoked token can neither refresh nor log out again.
	require.NoError(t, svc.LogoutByRefreshToken(t.Context(), rotated.RefreshToken, userID))
	_, err = svc.RefreshTokens(t.Context(), rotated.RefreshToken)
	requireUnauthorized(t, err)
	err = svc.LogoutByRefreshToken(t.Context(), rotated.RefreshToken, userID)
	requireUnauthorized(t, err)

	// Session-id logout stays idempotent for the already-revoked row.
	rotatedClaims, err := validator.Validate(rotated.AccessToken)
	require.NoError(t, err)
	sessionID, err := uuid.Parse(rotatedClaims.SessionID)
	require.NoError(t, err)
	require.NoError(t, svc.Logout(t.Context(), sessionID, userID))

	// Admin listing returns the single account, hashes stripped.
	users, err := svc.ListUsers(t.Context(), 10, 0)
	require.NoError(t, err)
	require.Len(t, users, 1)
	require.Empty(t, users[0].PasswordHash)
	require.Equal(t, []Role{RoleCustomer}, users[0].Roles)

	profile, err := svc.Profile(t.Context(), userID)
	require.NoError(t, err)
	require.Empty(t, profile.PasswordHash)
	require.Equal(t, []Role{RoleCustomer}, profile.Roles)

	require.Equal(t, 1, countAudit(t, store, ActionUserRegistered))
	require.Equal(t, 1, countAudit(t, store, ActionUserLogin))
	require.Equal(t, 1, countAudit(t, store, ActionSessionRefreshed))
	require.Equal(t, 1, countAudit(t, store, ActionSessionRevoked))

	truncateAll(t, store)
}

// TestPostgresStoreUserCRUDRoleGrantsAndSessions exercises the store's user
// CRUD, the role-grant surface (AssignRole / GetRoleGrants, including
// tenant-scoped grants and duplicate conflicts) and raw session persistence
// against real PostgreSQL.
func TestPostgresStoreUserCRUDRoleGrantsAndSessions(t *testing.T) {
	_, svc, store := integrationPool(t)
	ctx := t.Context()

	resp, err := svc.Register(ctx, "crud-user@example.com", "+254700000011", "integration-pass", "CRUD Tester", "ci-runner", "127.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	user, err := store.GetUserByEmail(ctx, "crud-user@example.com")
	require.NoError(t, err)

	// GetUser returns the created row with its initial grant.
	got, err := store.GetUser(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.ID, got.ID)
	require.Equal(t, "crud-user@example.com", got.Email)
	require.Equal(t, "+254700000011", got.Phone)
	require.Equal(t, StatusActive, got.Status)
	require.Equal(t, []Role{RoleCustomer}, got.Roles)
	require.False(t, got.CreatedAt.IsZero())
	require.False(t, got.UpdatedAt.IsZero())

	// Email lookup is case-insensitive and trims input.
	byEmail, err := store.GetUserByEmail(ctx, "  CRUD-USER@example.com ")
	require.NoError(t, err)
	require.Equal(t, user.ID, byEmail.ID)

	// ListUsers paginates newest-first with roles loaded.
	page, err := store.ListUsers(ctx, 10, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, []Role{RoleCustomer}, page[0].Roles)
	off, err := store.ListUsers(ctx, 10, 1)
	require.NoError(t, err)
	require.Empty(t, off, "offset past the end yields an empty page")

	// Role grants: a personal grant and a tenant-scoped grant persist with
	// their tenant; duplicates conflict at the database level.
	tenantA, tenantB := uuid.New(), uuid.New()
	require.NoError(t, store.AssignRole(ctx, user.ID, RoleGarage, nil))
	require.NoError(t, store.AssignRole(ctx, user.ID, RoleFleetAdmin, &tenantA))
	err = store.AssignRole(ctx, user.ID, RoleGarage, nil)
	require.Equal(t, 409, appErr(t, err).Status, "duplicate personal grant conflicts")
	err = store.AssignRole(ctx, user.ID, RoleFleetAdmin, &tenantB)
	require.Equal(t, 409, appErr(t, err).Status, "duplicate tenant-scoped grant conflicts")

	grants, err := store.GetRoleGrants(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, grants, 3, "initial CUSTOMER + GARAGE + FLEET_ADMIN")
	require.Equal(t, RoleCustomer, grants[0].Role)
	require.Nil(t, grants[0].TenantID)
	require.Equal(t, RoleGarage, grants[1].Role)
	require.Nil(t, grants[1].TenantID)
	require.Equal(t, RoleFleetAdmin, grants[2].Role)
	require.NotNil(t, grants[2].TenantID)
	require.Equal(t, tenantA, *grants[2].TenantID)

	// The service layer's AssignRole composes store write + audit + event;
	// with the publisher nil the event is skipped and the write still lands.
	actor := uuid.New()
	require.NoError(t, svc.AssignRole(ctx, user.ID, RoleDispatcher, &tenantB, actor))
	grants, err = store.GetRoleGrants(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, grants, 4)
	require.Equal(t, RoleDispatcher, grants[3].Role)
	require.Equal(t, tenantB, *grants[3].TenantID)
	require.Equal(t, 1, countAudit(t, store, ActionRoleGranted))

	// The refreshed user carries every granted role.
	roles, err := store.GetUser(ctx, user.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []Role{RoleCustomer, RoleGarage, RoleFleetAdmin, RoleDispatcher}, roles.Roles)

	// Sessions: direct row lifecycle — insert, find by digest, revoke,
	// revoked rows stay queryable with revoked_at stamped.
	issuer := newTestIssuer()
	sess := Session{
		ID:               uuid.New(),
		UserID:           user.ID,
		RefreshTokenHash: issuer.RefreshTokenHash("opaque-raw-token"),
		DeviceName:       "integration-runner",
		IPAddress:        "127.0.0.1",
		ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
	}
	require.NoError(t, store.CreateSession(ctx, sess))
	loaded, err := store.GetSessionByRefreshHash(ctx, issuer.RefreshTokenHash("opaque-raw-token"))
	require.NoError(t, err)
	require.Equal(t, sess.ID, loaded.ID)
	require.Nil(t, loaded.RevokedAt)

	require.NoError(t, store.RevokeSession(ctx, sess.ID))
	revoked, err := store.GetSessionByRefreshHash(ctx, issuer.RefreshTokenHash("opaque-raw-token"))
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt, "revocation stamps revoked_at")
	require.NoError(t, store.RevokeSession(ctx, sess.ID), "revoking twice is idempotent")

	_, err = store.GetSessionByRefreshHash(ctx, issuer.RefreshTokenHash("no-such-token"))
	require.True(t, isNotFound(err), "unknown digest must surface as not-found, got %v", err)

	truncateAll(t, store)
}

// countAudit counts audit rows for one action, proving audit writes persist.
func countAudit(t *testing.T, store *PostgresStore, action string) int {
	t.Helper()
	var n int
	require.NoError(t, store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n))
	return n
}

// TestPostgresStoreVerificationFlow exercises the verification flow against
// real PostgreSQL: token persistence (digest only), single-use consumption
// with the channel timestamp, expiry, resend cooldown, the grace-window
// refresh gate on both sides of the boundary, and the password-change
// session sweep. Gated on TEST_DATABASE_URL like the other suites.
func TestPostgresStoreVerificationFlow(t *testing.T) {
	validator, svc, store := integrationPool(t)
	ctx := t.Context()

	registered, err := svc.Register(ctx, "verifyflow@example.com", "+254700000015", "integration-pass", "Verify Flow", "ci-runner", "127.0.0.1")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(ctx, "verifyflow@example.com")
	require.NoError(t, err)
	require.Nil(t, user.EmailVerifiedAt)
	require.Nil(t, user.PhoneVerifiedAt)
	require.Equal(t, 2, countAudit(t, store, ActionVerificationIssued), "registration issued one token per present channel: email + phone")

	// Store stats see the registration token for the email channel only.
	count, latest, err := store.VerificationTokenStats(ctx, user.ID, ChannelEmail, time.Now().Add(-verificationResendLimitWindow))
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.False(t, latest.IsZero())
	// The phone channel's latest issuance is the registration phone token
	// (the user registered with a phone); `latest` is unfiltered by the
	// since window by contract.
	_, latestPhone, err := store.VerificationTokenStats(ctx, user.ID, ChannelPhone, time.Now())
	require.NoError(t, err)
	require.False(t, latestPhone.IsZero(), "the registration phone token is the latest phone-channel issuance")

	// Immediate resend trips the cooldown at the database level.
	_, _, err = svc.ResendVerification(ctx, "verifyflow@example.com", ChannelEmail)
	require.Equal(t, 429, appErr(t, err).Status)

	// Issue a fresh token through the service and consume it: single use,
	// channel stamped, audited.
	raw, _, err := svc.issueVerificationToken(ctx, user, ChannelEmail, "resend")
	require.NoError(t, err)
	require.Equal(t, 1, countAudit(t, store, ActionVerificationResent))
	channel, err := svc.Verify(ctx, raw)
	require.NoError(t, err)
	require.Equal(t, ChannelEmail, channel)
	verified, err := store.GetUserByEmail(ctx, "verifyflow@example.com")
	require.NoError(t, err)
	require.NotNil(t, verified.EmailVerifiedAt)

	// Replay is rejected by the conditional UPDATE, not just the service.
	_, err = svc.Verify(ctx, raw)
	requireUnauthorized(t, err)

	// Expiry: an aged-out token is rejected and stamps nothing.
	rawExpired, _, err := svc.issueVerificationToken(ctx, verified, ChannelEmail, "resend")
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `UPDATE verification_tokens SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`,
		svc.issuer.TokenHash(rawExpired))
	require.NoError(t, err)
	_, err = svc.Verify(ctx, rawExpired)
	requireUnauthorized(t, err)

	// Grace gate, both sides of the boundary: backdate creation past the
	// default 24h window, refresh refuses; the verification timestamp lifts
	// the gate without a new session.
	_, err = store.pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '25 hours' WHERE id = $1`, user.ID)
	require.NoError(t, err)
	_, err = svc.RefreshTokens(ctx, registered.RefreshToken)
	require.Equal(t, 403, appErr(t, err).Status, "unverified-past-grace refresh is refused")
	_, err = svc.Login(ctx, "verifyflow@example.com", "integration-pass", "", "")
	require.Equal(t, 403, appErr(t, err).Status, "unverified-past-grace login is refused")
	claims, err := validator.Validate(registered.AccessToken)
	require.NoError(t, err)
	require.Equal(t, user.ID.String(), claims.UserID, "the access token itself still validates during the window boundary test")

	// Phone verification completes the account (registration phone token).
	phoneUser, err := store.GetUserByEmail(ctx, "verifyflow@example.com")
	require.NoError(t, err)
	require.False(t, phoneUser.verificationSatisfied(), "email verified, phone still pending")
	rawPhone, _, err := svc.issueVerificationToken(ctx, phoneUser, ChannelPhone, "resend")
	require.NoError(t, err)
	_, err = svc.Verify(ctx, rawPhone)
	require.NoError(t, err)
	fullyVerified, err := store.GetUserByEmail(ctx, "verifyflow@example.com")
	require.NoError(t, err)
	require.True(t, fullyVerified.verificationSatisfied())
	require.Equal(t, 2, countAudit(t, store, ActionVerificationVerified), "one verified event per consumed token")

	// Password change: breach-unchecked here (no breacher wired), but the
	// hash update + full session sweep + audit are exercised end-to-end.
	require.NoError(t, svc.ChangePassword(ctx, user.ID, "integration-pass", "rotated-pass-77"))
	_, err = svc.RefreshTokens(ctx, registered.RefreshToken)
	requireUnauthorized(t, err, "the password change revoked the pre-change session")
	require.Equal(t, 1, countAudit(t, store, ActionPasswordChanged))
	relaunched, err := svc.Login(ctx, "verifyflow@example.com", "rotated-pass-77", "ci-runner-3", "127.0.0.3")
	require.NoError(t, err)
	_, err = validator.Validate(relaunched.AccessToken)
	require.NoError(t, err)
	_, err = svc.Login(ctx, "verifyflow@example.com", "integration-pass", "", "")
	requireUnauthorized(t, err, "the old password no longer authenticates")

	truncateAll(t, store)
}

// TestPostgresStoreAuditReadAPI proves the audit read path against real
// PostgreSQL (issue #88): seeded rows come back newest first through the
// exact-match filters, the tenant constraint is applied at the query
// level, pagination clamps, and the append-only trigger still refuses to
// mutate rows the API just read.
func TestPostgresStoreAuditReadAPI(t *testing.T) {
	_, _, store := integrationPool(t)
	ctx := t.Context()

	tenantA, tenantB := uuid.New(), uuid.New()
	actor, other := uuid.New(), uuid.New()
	rows := []struct {
		action string
		tenant *uuid.UUID
		act    *uuid.UUID
	}{
		{"dispatch.decision.scored", &tenantA, &actor},
		{"dispatch.decision.scored", &tenantB, &actor},
		{"identity.user.registered", nil, nil},
		{"identity.user.login", nil, &other},
	}
	for _, row := range rows {
		require.NoError(t, store.InsertAudit(ctx, AuditEntry{
			ActorID: row.act, Action: row.action, ObjectType: "dispatch_decision",
			ObjectID: uuid.NewString(), TenantID: row.tenant,
			Metadata: map[string]any{"k": "v"},
		}))
	}

	// Unfiltered page, newest first (created_at DESC, id DESC).
	page, err := store.ListAudit(ctx, AuditFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page, 4)
	require.Equal(t, "identity.user.login", page[0].Action, "the last insert is newest")
	for i := 1; i < len(page); i++ {
		require.Less(t, page[i].ID, page[i-1].ID, "strictly descending append-only ids")
	}
	require.NotNil(t, page[0].Metadata, "metadata round-trips as JSONB")

	// Tenant constraint applied at the query level.
	aPage, err := store.ListAudit(ctx, AuditFilter{TenantID: &tenantA})
	require.NoError(t, err)
	require.Len(t, aPage, 1)
	require.NotNil(t, aPage[0].TenantID)
	require.Equal(t, tenantA, *aPage[0].TenantID)

	// Actor + action + object-type filters compose.
	actorPage, err := store.ListAudit(ctx, AuditFilter{ActorID: &actor, Action: "dispatch.decision.scored"})
	require.NoError(t, err)
	require.Len(t, actorPage, 2)

	// Pagination: limit/offset walk the newest-first page, and the clamp
	// bounds an oversized limit.
	paged, err := store.ListAudit(ctx, AuditFilter{Limit: 2, Offset: 1})
	require.NoError(t, err)
	require.Len(t, paged, 2)
	require.Less(t, paged[0].ID, page[0].ID, "offset skips the newest row")

	// since/until window: a future since excludes everything.
	future := time.Now().Add(time.Hour)
	empty, err := store.ListAudit(ctx, AuditFilter{Since: &future})
	require.NoError(t, err)
	require.Empty(t, empty)

	// The read API's rows stay append-only: mutation is refused by the
	// trigger even after they have been projected to a caller.
	_, err = store.pool.Exec(ctx, `UPDATE audit_log SET action = 'tampered'`)
	require.Error(t, err, "audit_log is append-only (ADR-0003/0004)")

	truncateAll(t, store)
}
