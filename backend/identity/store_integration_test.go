package identity

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// integrationPool connects to the Postgres instance named by
// TEST_DATABASE_URL, applies the identity migration chain and returns a
// pool plus a Service over it. Tests skip when the variable is unset.
func integrationPool(t *testing.T) (*platform.JWTValidator, *Service, *PostgresStore) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}

	ctx := t.Context()
	pool, err := platform.NewPostgres(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, platform.MigrateUp(ctx, pool, identitymigrations.FS, "schema_migrations_identity"))
	_, err = pool.Exec(ctx, `TRUNCATE audit_log, sessions, user_roles, users CASCADE`)
	require.NoError(t, err)

	store := NewPostgresStore(pool)
	validator := platform.NewJWTValidator(testSecret, testIssuer, testAudience)
	return validator, NewService(store, newTestIssuer()), store
}

// truncateAll resets the identity tables after a test body ran.
func truncateAll(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.pool.Exec(t.Context(), `TRUNCATE audit_log, sessions, user_roles, users CASCADE`)
	require.NoError(t, err)
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

// countAudit counts audit rows for one action, proving audit writes persist.
func countAudit(t *testing.T, store *PostgresStore, action string) int {
	t.Helper()
	var n int
	require.NoError(t, store.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n))
	return n
}
