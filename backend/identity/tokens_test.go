package identity

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// testIssuerConfig matches across issuer and validator, like one deployment.
const (
	testSecret   = "unit-test-secret-that-is-long-enough"
	testIssuer   = "motivra-identity-test"
	testAudience = "motivra-api-test"
)

// newTestIssuer builds an Issuer for tests.
func newTestIssuer() *Issuer {
	return NewIssuer(testSecret, testIssuer, testAudience)
}

// testUser returns a user with the given roles.
func testUser(roles ...Role) User {
	return User{
		ID:           uuid.New(),
		Email:        "user@example.com",
		FullName:     "Test User",
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Status:       StatusActive,
		Roles:        roles,
	}
}

// requireUnauthorized asserts err renders as an HTTP 401 application error.
func requireUnauthorized(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	appErr := platform.AsError(err)
	require.Equal(t, 401, appErr.Status)
}

func TestIssueProducesValidatableAccessToken(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer, RoleAdmin)

	resp, err := issuer.Issue(t.Context(), store, user, "android-phone", "10.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)
	require.WithinDuration(t, time.Now().Add(DefaultAccessTTL), resp.ExpiresAt, 5*time.Second)

	claims, err := platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, user.ID.String(), claims.UserID)
	require.Equal(t, "ADMIN", claims.Role, "highest-priority role wins")
	require.Empty(t, claims.TenantID, "personal accounts carry no tenant")
	require.NotEqual(t, uuid.Nil.String(), claims.SessionID)
	require.True(t, claims.ExpiresAt.After(time.Now()))
	require.True(t, claims.IssuedAt.Before(time.Now().Add(time.Second)))

	// The refresh token is 256 bits of entropy, base64url-encoded.
	raw, err := base64.RawURLEncoding.DecodeString(resp.RefreshToken)
	require.NoError(t, err)
	require.Len(t, raw, refreshTokenBytes)

	// The session is persisted with the hash, never the token itself.
	hash := issuer.RefreshTokenHash(resp.RefreshToken)
	sess, err := store.GetSessionByRefreshHash(t.Context(), hash)
	require.NoError(t, err)
	require.Equal(t, sess.ID.String(), claims.SessionID)
	require.Equal(t, user.ID, sess.UserID)
	require.Equal(t, "android-phone", sess.DeviceName)
	require.Equal(t, "10.0.0.1", sess.IPAddress)
	require.Nil(t, sess.RevokedAt)
	require.True(t, sess.ExpiresAt.After(time.Now().Add(DefaultRefreshTTL-time.Minute)))
}

func TestIssueTenantClaimFromHighestOrgRole(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer)
	tenantA, tenantB := uuid.New(), uuid.New()
	require.NoError(t, store.CreateUser(t.Context(), &user))
	require.NoError(t, store.AssignRole(t.Context(), user.ID, RoleGarage, &tenantA))
	require.NoError(t, store.AssignRole(t.Context(), user.ID, RoleFleetAdmin, &tenantB))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err)
	claims, err := platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(resp.AccessToken)
	require.NoError(t, err)
	require.Equal(t, tenantB.String(), claims.TenantID, "highest-priority org role decides the tenant")
	require.Equal(t, "FLEET_ADMIN", claims.Role)
}

func TestRefreshRotatesAndInvalidatesOldToken(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))

	first, err := issuer.Issue(t.Context(), store, user, "web", "192.168.1.1")
	require.NoError(t, err)

	second, _, err := issuer.Refresh(t.Context(), store, first.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, first.RefreshToken, second.RefreshToken)
	require.NotEqual(t, first.AccessToken, second.AccessToken)

	// Reuse of the rotated token is rejected.
	_, _, err = issuer.Refresh(t.Context(), store, first.RefreshToken)
	requireUnauthorized(t, err)

	// The new pair is usable and validates.
	claims, err := platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(second.AccessToken)
	require.NoError(t, err)
	require.Equal(t, user.ID.String(), claims.UserID)
}

func TestRefreshRejectsExpiredToken(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err)
	hash := issuer.RefreshTokenHash(resp.RefreshToken)
	sess, err := store.GetSessionByRefreshHash(t.Context(), hash)
	require.NoError(t, err)
	store.expireSession(sess.ID)

	_, _, err = issuer.Refresh(t.Context(), store, resp.RefreshToken)
	requireUnauthorized(t, err)
}

func TestRefreshRejectsRevokedAndUnknownTokens(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err)
	hash := issuer.RefreshTokenHash(resp.RefreshToken)
	sess, err := store.GetSessionByRefreshHash(t.Context(), hash)
	require.NoError(t, err)

	// Revoke the session; refresh and a second revoke-then-refresh fail.
	require.NoError(t, issuer.Revoke(t.Context(), store, sess.ID))
	_, _, err = issuer.Refresh(t.Context(), store, resp.RefreshToken)
	requireUnauthorized(t, err)

	// Unknown refresh tokens are rejected identically.
	_, _, err = issuer.Refresh(t.Context(), store, base64.RawURLEncoding.EncodeToString(make([]byte, refreshTokenBytes)))
	requireUnauthorized(t, err)
	_, _, err = issuer.Refresh(t.Context(), store, "")
	requireUnauthorized(t, err)
}

func TestRevokeIsIdempotentForKnownSessions(t *testing.T) {
	store := newFakeStore()
	issuer := newTestIssuer()
	user := testUser(RoleCustomer)
	require.NoError(t, store.CreateUser(t.Context(), &user))

	resp, err := issuer.Issue(t.Context(), store, user, "", "")
	require.NoError(t, err)
	sess, err := store.GetSessionByRefreshHash(t.Context(), issuer.RefreshTokenHash(resp.RefreshToken))
	require.NoError(t, err)

	require.NoError(t, issuer.Revoke(t.Context(), store, sess.ID))
	require.NoError(t, issuer.Revoke(t.Context(), store, sess.ID), "revoking twice stays successful")
	require.Error(t, issuer.Revoke(t.Context(), store, uuid.New()), "unknown sessions are not found")
}

func TestRefreshTokenHashIsKeyedAndDeterministic(t *testing.T) {
	issuerA := NewIssuer("secret-a-motivra-identity", testIssuer, testAudience)
	issuerB := NewIssuer("secret-b-motivra-identity", testIssuer, testAudience)
	token := "some-opaque-refresh-token"

	require.Equal(t, issuerA.RefreshTokenHash(token), issuerA.RefreshTokenHash(token))
	require.NotEqual(t, issuerA.RefreshTokenHash(token), issuerA.RefreshTokenHash(token+"x"))
	require.NotEqual(t, issuerA.RefreshTokenHash(token), issuerB.RefreshTokenHash(token),
		"the digest is keyed with the issuer secret (ADR-0004)")
}

func TestHighestRolePrecedence(t *testing.T) {
	require.Equal(t, "SUPER_ADMIN", HighestRole([]Role{RoleCustomer, RoleSuperAdmin, RoleAdmin}))
	require.Equal(t, "TECHNICIAN", HighestRole([]Role{RoleTechnician, RoleCustomer}))
	require.Equal(t, "CUSTOMER", HighestRole([]Role{RoleCustomer}))
	require.Empty(t, HighestRole(nil))
	require.Empty(t, HighestRole([]Role{Role("UNKNOWN")}))
}
