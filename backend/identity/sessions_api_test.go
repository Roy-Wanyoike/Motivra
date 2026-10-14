package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// deleteAuthed sends a DELETE with a Bearer access token.
func deleteAuthed(t *testing.T, h http.Handler, path, accessToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSessionManagementEndpoints is the issue #87 acceptance flow for the
// session API: list (credential-free, exactly one current session), single
// revoke with ownership-scoped 404s (foreign and unknown ids are
// indistinguishable) and 422 on malformed ids, revoke-all with its count,
// the refresh refusal that follows revocation — and the audit trail of
// both actions (identity.session.revoked / identity.session.revoked_all).
func TestSessionManagementEndpoints(t *testing.T) {
	router, _, store, _ := newTestAPI()

	alice := registerPrincipal(t, router, "alice.sessions@api.test")
	aliceUser, err := store.GetUserByEmail(t.Context(), "alice.sessions@api.test")
	require.NoError(t, err)

	// A second device: alice now owns two live sessions.
	phoneLogin := postJSON(t, router, "/v1/auth/login",
		`{"email":"alice.sessions@api.test","password":"strong-passphrase","device_name":"pixel-8"}`)
	require.Equal(t, http.StatusOK, phoneLogin.Code)
	var phone AccessResponse
	require.NoError(t, json.Unmarshal(phoneLogin.Body.Bytes(), &phone))

	// --- LIST ---
	rec := getAuthed(t, router, "/v1/auth/sessions", alice.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list sessionListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Sessions, 2)

	currents := 0
	for _, sess := range list.Sessions {
		require.NotEqual(t, uuid.Nil, sess.ID)
		require.NotEmpty(t, sess.CreatedAt)
		require.NotEmpty(t, sess.ExpiresAt)
		require.NotEmpty(t, sess.AbsoluteExpiresAt)
		require.Nil(t, sess.RevokedAt, "no session has been revoked yet")
		if sess.Current {
			currents++
		}
	}
	require.Equal(t, 1, currents, "exactly the session behind the presented access token is current")
	require.NotContains(t, rec.Body.String(), "refresh_token", "projections never carry credential material")
	require.NotContains(t, rec.Body.String(), "password")

	// The GET used the registration pair, so the phone session is the
	// revocation target.
	var phoneSession sessionResponse
	for _, sess := range list.Sessions {
		if !sess.Current {
			phoneSession = sess
		}
	}
	require.False(t, phoneSession.Current)

	// --- REVOKE ONE ---
	rec = deleteAuthed(t, router, "/v1/auth/sessions/"+phoneSession.ID.String(), alice.AccessToken)
	require.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())

	// The revoked session's refresh token is dead; the current one rotates.
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+phone.RefreshToken+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = getAuthed(t, router, "/v1/auth/sessions", alice.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	for _, sess := range list.Sessions {
		if sess.ID == phoneSession.ID {
			require.NotNil(t, sess.RevokedAt, "the revoked session renders as signed out")
		}
	}

	// The revocation is audited with the API attribution and the redacted
	// device name.
	revokes := store.auditsByAction(ActionSessionRevoked)
	require.Len(t, revokes, 1)
	require.NotNil(t, revokes[0].ActorID)
	require.Equal(t, aliceUser.ID, *revokes[0].ActorID)
	require.Equal(t, "session", revokes[0].ObjectType)
	require.Equal(t, phoneSession.ID.String(), revokes[0].ObjectID)
	require.Equal(t, "sessions_api", revokes[0].Metadata["via"])

	// --- 404/422 SPACE ---
	bob := registerPrincipal(t, router, "bob.sessions@api.test")
	bobList := getAuthed(t, router, "/v1/auth/sessions", bob.AccessToken)
	require.Equal(t, http.StatusOK, bobList.Code)
	var bobSessions sessionListResponse
	require.NoError(t, json.Unmarshal(bobList.Body.Bytes(), &bobSessions))
	require.NotEmpty(t, bobSessions.Sessions)
	bobSession := bobSessions.Sessions[0]

	rec = deleteAuthed(t, router, "/v1/auth/sessions/"+bobSession.ID.String(), alice.AccessToken)
	require.Equal(t, http.StatusNotFound, rec.Code, "alice cannot revoke bob's session")
	recUnknown := deleteAuthed(t, router, "/v1/auth/sessions/"+uuid.New().String(), alice.AccessToken)
	require.Equal(t, http.StatusNotFound, recUnknown.Code, "unknown ids are not found")
	require.Equal(t, rec.Body.String(), recUnknown.Body.String(),
		"foreign and unknown sessions answer the identical 404 — no existence oracle across accounts")
	rec = deleteAuthed(t, router, "/v1/auth/sessions/not-a-uuid", alice.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// The 404s are NOT audited: only real state changes leave audit rows.
	require.Len(t, store.auditsByAction(ActionSessionRevoked), 1)

	// --- REVOKE ALL ---
	rec = deleteAuthed(t, router, "/v1/auth/sessions", alice.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	var revokedAll revokedAllResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &revokedAll))
	require.Equal(t, 1, revokedAll.Revoked, "only the still-live registration session remains")

	// Sign-out-everywhere includes the calling session: the presented
	// refresh token can no longer rotate; the access token itself stays
	// valid until expiry (within one access TTL).
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+alice.RefreshToken+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = getAuthed(t, router, "/v1/auth/sessions", alice.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, "access tokens live out their TTL after revocation")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	for _, sess := range list.Sessions {
		require.NotNil(t, sess.RevokedAt, "every alice session is revoked after the sweep")
	}

	revokedAllAudits := store.auditsByAction(ActionSessionRevokedAll)
	require.Len(t, revokedAllAudits, 1)
	require.NotNil(t, revokedAllAudits[0].ActorID)
	require.Equal(t, aliceUser.ID, *revokedAllAudits[0].ActorID)
	require.Equal(t, "user", revokedAllAudits[0].ObjectType)
	require.Equal(t, aliceUser.ID.String(), revokedAllAudits[0].ObjectID)
	require.Equal(t, 1, revokedAllAudits[0].Metadata["revoked"], "the sweep carries its count")
}

// TestSessionManagementRequiresAuthentication: all three routes sit behind
// RequireAuthenticated — no token, no session visibility or control.
func TestSessionManagementRequiresAuthentication(t *testing.T) {
	router, _, _, _ := newTestAPI()

	rec := getAuthed(t, router, "/v1/auth/sessions", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = deleteAuthed(t, router, "/v1/auth/sessions/"+uuid.New().String(), "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = deleteAuthed(t, router, "/v1/auth/sessions", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
