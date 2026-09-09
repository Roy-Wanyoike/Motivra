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
var identityTables = []string{"audit_log", "sessions", "user_roles", "users"}

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
