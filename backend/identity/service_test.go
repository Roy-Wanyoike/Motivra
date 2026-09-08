package identity

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// newTestService wires a Service over the in-memory store.
func newTestService() (*Service, *fakeStore) {
	store := newFakeStore()
	return NewService(store, newTestIssuer()), store
}

// appErr converts err into the platform error for status/code assertions.
func appErr(t *testing.T, err error) *platform.Error {
	t.Helper()
	require.Error(t, err)
	return platform.AsError(err)
}

func TestRegisterIssuesTokensAndAudits(t *testing.T) {
	svc, store := newTestService()

	resp, err := svc.Register(t.Context(), "Ada@example.com", "+254700000001", "lovelace-123", "Ada Lovelace", "pixel-8", "41.90.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	require.NotEmpty(t, resp.RefreshToken)

	claims, err := platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(resp.AccessToken)
	require.NoError(t, err)

	user, err := store.GetUserByEmail(t.Context(), "ada@example.com")
	require.NoError(t, err)
	require.Equal(t, user.ID.String(), claims.UserID)
	require.Equal(t, "ada@example.com", user.Email, "email stored lowercased")
	require.Equal(t, []Role{RoleCustomer}, user.Roles)
	require.Equal(t, StatusActive, user.Status)
	ok, err := VerifyPassword("lovelace-123", user.PasswordHash)
	require.NoError(t, err)
	require.True(t, ok)

	entries := store.auditsByAction(ActionUserRegistered)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].ActorID)
	require.Equal(t, user.ID, *entries[0].ActorID)
	require.Equal(t, "user", entries[0].ObjectType)
	require.Equal(t, user.ID.String(), entries[0].ObjectID)
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	svc, store := newTestService()

	cases := []struct {
		name     string
		email    string
		phone    string
		password string
		fullName string
	}{
		{"bad email", "not-an-email", "", "password-123", "A B"},
		{"empty email", "", "", "password-123", "A B"},
		{"empty full name", "a@example.com", "", "password-123", ""},
		{"short password", "a@example.com", "", "short", "A B"},
		{"overlong password", "a@example.com", "", string(make([]byte, 129)), "A B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Register(t.Context(), tc.email, tc.phone, tc.password, tc.fullName, "", "")
			require.Equal(t, 422, appErr(t, err).Status)
		})
	}
	require.Empty(t, store.audits, "rejected registrations are not audited")
}

func TestRegisterDuplicateEmailConflicts(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.Register(t.Context(), "dup@example.com", "", "password-123", "First", "", "")
	require.NoError(t, err)
	_, err = svc.Register(t.Context(), "DUP@example.com", "", "password-456", "Second", "", "")
	e := appErr(t, err)
	require.Equal(t, 409, e.Status)
	require.Equal(t, "conflict", e.Code)
}

func TestLoginSuccessAndIdenticalFailures(t *testing.T) {
	svc, store := newTestService()
	const password = "correct-horse-9"
	_, err := svc.Register(t.Context(), "login@example.com", "", password, "Login User", "", "")
	require.NoError(t, err)

	resp, err := svc.Login(t.Context(), "LOGIN@example.com", password, "web", "10.1.2.3")
	require.NoError(t, err)
	_, err = platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(resp.AccessToken)
	require.NoError(t, err)
	require.Len(t, store.auditsByAction(ActionUserLogin), 1)

	_, errUnknown := svc.Login(t.Context(), "ghost@example.com", password, "", "")
	_, errWrong := svc.Login(t.Context(), "login@example.com", "wrong-password", "", "")
	requireUnauthorized(t, errUnknown)
	requireUnauthorized(t, errWrong)

	// Constant behavior: unknown email and bad password are indistinguishable.
	unknown, wrong := appErr(t, errUnknown), appErr(t, errWrong)
	require.Equal(t, unknown.Code, wrong.Code)
	require.Equal(t, unknown.Detail, wrong.Detail)
	require.Equal(t, unknown.Status, wrong.Status)
}

func TestLoginRejectsInactiveAccounts(t *testing.T) {
	svc, store := newTestService()
	_, err := svc.Register(t.Context(), "suspended@example.com", "", "password-123", "Suspended", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "suspended@example.com")
	require.NoError(t, err)
	store.setStatus(user.ID, StatusSuspended)

	_, err = svc.Login(t.Context(), "suspended@example.com", "password-123", "", "")
	require.Equal(t, 403, appErr(t, err).Status)
}

func TestRefreshTokensAndLogoutAudited(t *testing.T) {
	svc, store := newTestService()
	registered, err := svc.Register(t.Context(), "flow@example.com", "", "password-123", "Flow User", "", "")
	require.NoError(t, err)

	refreshed, err := svc.RefreshTokens(t.Context(), registered.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, registered.RefreshToken, refreshed.RefreshToken)
	require.Len(t, store.auditsByAction(ActionSessionRefreshed), 1)

	// The rotated token can no longer refresh.
	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	requireUnauthorized(t, err)

	// Logout revokes the current session and audits it.
	claims, err := platform.NewJWTValidator(testSecret, testIssuer, testAudience).Validate(refreshed.AccessToken)
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "flow@example.com")
	require.NoError(t, err)
	sessionID := mustParseUUID(t, claims.SessionID)
	require.NoError(t, svc.Logout(t.Context(), sessionID, user.ID))
	require.Len(t, store.auditsByAction(ActionSessionRevoked), 1)

	_, err = svc.RefreshTokens(t.Context(), refreshed.RefreshToken)
	requireUnauthorized(t, err)

	// Logout is idempotent for already-revoked sessions; unknown ids 404.
	require.NoError(t, svc.Logout(t.Context(), sessionID, user.ID))
	err = svc.Logout(t.Context(), uuid.New(), user.ID)
	require.Equal(t, 404, appErr(t, err).Status)
}

func TestProfileOmitsPasswordHash(t *testing.T) {
	svc, store := newTestService()
	_, err := svc.Register(t.Context(), "profile@example.com", "+254700000002", "password-123", "Profile User", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "profile@example.com")
	require.NoError(t, err)

	profile, err := svc.Profile(t.Context(), user.ID)
	require.NoError(t, err)
	require.Empty(t, profile.PasswordHash)
	require.Equal(t, []Role{RoleCustomer}, profile.Roles)

	_, err = svc.Profile(t.Context(), mustParseUUID(t, "00000000-0000-0000-0000-000000000001"))
	require.Equal(t, 404, appErr(t, err).Status)
}

func TestAuditFailureFailsTheRequest(t *testing.T) {
	svc, store := newTestService()
	store.failAudit = true

	_, err := svc.Register(t.Context(), "audited@example.com", "", "password-123", "Audited", "", "")
	require.Error(t, err, "audit failures must fail registration")
	_, err = svc.Login(t.Context(), "audited@example.com", "password-123", "", "")
	require.Error(t, err, "audit failures must fail login")
}

func TestRecordAuditRequiresFields(t *testing.T) {
	store := newFakeStore()
	err := RecordAudit(t.Context(), store, AuditEntry{ObjectType: "user", ObjectID: "x"})
	require.Equal(t, 422, appErr(t, err).Status)
	err = RecordAudit(t.Context(), store, AuditEntry{Action: "identity.test", ObjectID: "x"})
	require.Equal(t, 422, appErr(t, err).Status)
	require.NoError(t, RecordAudit(t.Context(), store, AuditEntry{Action: "identity.test", ObjectType: "user", ObjectID: "x"}))
	require.Len(t, store.audits, 1)
	require.NotNil(t, store.audits[0].Metadata, "nil metadata is normalized")
}

func TestListUsersPaginatesAndStripsHashes(t *testing.T) {
	svc, _ := newTestService()
	for i := 0; i < 3; i++ {
		_, err := svc.Register(t.Context(), "list"+string(rune('0'+i))+"@example.com", "", "password-123", "List User", "", "")
		require.NoError(t, err)
	}

	first, err := svc.ListUsers(t.Context(), 2, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)
	second, err := svc.ListUsers(t.Context(), 2, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.NotEqual(t, first[0].ID, second[0].ID, "pages do not overlap")
	for _, u := range append(first, second...) {
		require.Empty(t, u.PasswordHash)
		require.Equal(t, []Role{RoleCustomer}, u.Roles)
	}

	// Bounds are clamped, not rejected: huge limits cap at 100, zero/negative
	// fall back to the default, negative offsets floor at 0.
	limited, err := svc.ListUsers(t.Context(), 1000, -5)
	require.NoError(t, err)
	require.Len(t, limited, 3)
	tiny, err := svc.ListUsers(t.Context(), 0, 99)
	require.NoError(t, err)
	require.Empty(t, tiny)
}

func TestLogoutByRefreshToken(t *testing.T) {
	svc, store := newTestService()
	registered, err := svc.Register(t.Context(), "bye@example.com", "", "password-123", "Bye User", "", "")
	require.NoError(t, err)
	user, err := store.GetUserByEmail(t.Context(), "bye@example.com")
	require.NoError(t, err)

	require.NoError(t, svc.LogoutByRefreshToken(t.Context(), registered.RefreshToken, user.ID))
	require.Len(t, store.auditsByAction(ActionSessionRevoked), 1)

	// The revoked token cannot refresh and cannot log out again; both yield
	// the same unauthorized error (no state disclosure).
	_, err = svc.RefreshTokens(t.Context(), registered.RefreshToken)
	requireUnauthorized(t, err)
	err = svc.LogoutByRefreshToken(t.Context(), registered.RefreshToken, user.ID)
	requireUnauthorized(t, err)

	err = svc.LogoutByRefreshToken(t.Context(), "", user.ID)
	require.Equal(t, 422, appErr(t, err).Status)
}

// mustParseUUID parses s or fails the test.
func mustParseUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}
