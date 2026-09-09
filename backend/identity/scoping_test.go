package identity

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cross-principal read isolation (ADR-0004: "Integration tests prove
// cross-tenant denial"). Two principals' fixtures are seeded; acting as
// principal A, every read path must return zero data from principal B —
// and a denial must not disclose existence.
//
// The identity tables are deliberately not multi-tenant: users and sessions
// carry no tenant_id (users is the platform-wide account directory that
// issues the tokens every other domain consumes), so the enforceable scope
// here is principal-level, exactly like jobs' resource-level scope. Every
// read falls into one of three enforced classes, and the tests below pin
// one case per class:
//
//  1. subject-scoped — GET /v1/users/me reads the token subject only;
//     there is no handle by which one principal can request another's row.
//  2. operator-scoped — GET /v1/admin/users is RBAC-gated to ADMIN and
//     SUPER_ADMIN and spans the (by-design global) directory, mirroring the
//     operator surface of the jobs list route.
//  3. credential/possession-scoped — login matches an email to a password,
//     and refresh/logout address a session only through its opaque refresh
//     token. No principal-derived filter applies because possession of the
//     secret IS the authorization (ADR-0004 rotation model); failures are
//     indistinguishable so no state is disclosed.
//
// Static catalogues (the role matrix in RBAC code) are N/A by design and
// carry no read path.

// registerPrincipal seeds one account through the public API and returns
// its first token pair.
func registerPrincipal(t *testing.T, h http.Handler, email string) AccessResponse {
	t.Helper()
	rec := postJSON(t, h, "/v1/auth/register",
		`{"email":"`+email+`","password":"strong-passphrase","full_name":"Tenant `+email+`"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var resp AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestCrossPrincipalProfileReadsAreSelfScoped(t *testing.T) {
	router, _, _, validator := newTestAPI()

	alice := registerPrincipal(t, router, "alice@isol.test")
	bob := registerPrincipal(t, router, "bob@isol.test")

	aliceClaims, err := validator.Validate(alice.AccessToken)
	require.NoError(t, err)
	bobClaims, err := validator.Validate(bob.AccessToken)
	require.NoError(t, err)
	require.NotEqual(t, aliceClaims.UserID, bobClaims.UserID, "the two principals must be distinct subjects")

	// Alice's profile read returns exactly Alice: subject-scoped by the
	// token, never by any client-supplied identifier.
	rec := getAuthed(t, router, "/v1/users/me", alice.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var profile map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	assert.Equal(t, "alice@isol.test", profile["email"])
	assert.Equal(t, aliceClaims.UserID, profile["id"], "the profile id must be the token subject")
	assert.NotContains(t, rec.Body.String(), "bob@isol.test", "principal B must not leak into A's read")

	// There is no per-id user read: probing B's id answers the router's
	// plain 404, indistinguishable from any unknown path (no existence
	// leak, and never a 200 carrying B's data).
	rec = getAuthed(t, router, "/v1/users/"+bobClaims.UserID, alice.AccessToken)
	require.Equal(t, http.StatusNotFound, rec.Code, "no route may address another principal's user row")

	// Symmetric check: Bob sees only Bob.
	rec = getAuthed(t, router, "/v1/users/me", bob.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	assert.Equal(t, "bob@isol.test", profile["email"])
	assert.NotContains(t, rec.Body.String(), "alice@isol.test", "principal A must not leak into B's read")

	// Unauthenticated reads are rejected before scoping applies.
	rec = getAuthed(t, router, "/v1/users/me", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAdminUserListingIsOperatorSurface(t *testing.T) {
	router, svc, store, _ := newTestAPI()

	registerPrincipal(t, router, "carol@isol.test")
	registerPrincipal(t, router, "dave@isol.test")
	erin := registerPrincipal(t, router, "erin@isol.test")

	// A plain CUSTOMER cannot list the directory at all: the route is
	// operator-gated before any row is read.
	rec := getAuthed(t, router, "/v1/admin/users", erin.AccessToken)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Grant ADMIN to erin and re-issue her token the way the identity
	// service itself would (roles load from the grants table).
	admin, err := store.GetUserByEmail(t.Context(), "erin@isol.test")
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(t.Context(), admin.ID, RoleAdmin, nil))
	adminTokens, err := newTestIssuer().Issue(t.Context(), svc.store, admin, "console", "127.0.0.1")
	require.NoError(t, err)

	// The operator listing spans the by-design global directory (the users
	// table has no tenant_id; identity is the token issuer for the whole
	// platform) — the documented operator surface, as with the jobs list.
	rec = getAuthed(t, router, "/v1/admin/users", adminTokens.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var page userListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	emails := map[string]bool{}
	for _, u := range page.Users {
		emails[u.Email] = true
	}
	assert.True(t, emails["carol@isol.test"], "operator listing spans principal carol")
	assert.True(t, emails["dave@isol.test"], "operator listing spans principal dave")

	// Operator visibility never extends to credential material.
	assert.NotContains(t, rec.Body.String(), "password", "no credential material may be serialized")

	// Unauthenticated listing is rejected before scoping applies.
	rec = getAuthed(t, router, "/v1/admin/users", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestSessionReadsArePossessionScoped(t *testing.T) {
	router, _, store, _ := newTestAPI()

	frank := registerPrincipal(t, router, "frank@isol.test")

	// Sessions are addressed only by the opaque refresh token (an
	// HMAC-SHA256 digest looked up in the store); there is no route that
	// reads or revokes a session by id, so one principal cannot probe
	// another's sessions even with a guessed identifier.
	rec := getAuthed(t, router, "/v1/sessions/"+uuid.New().String(), frank.AccessToken)
	require.Equal(t, http.StatusNotFound, rec.Code, "no route may address a session by id")

	// Unknown refresh tokens answer the identical 401 on refresh and
	// logout, so probing cannot distinguish unknown from foreign.
	fabricated := strings.Repeat("A", 64)
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+fabricated+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = postJSON(t, router, "/v1/auth/logout", `{"refresh_token":"`+fabricated+`"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Possession still works for the owner: frank rotates his own session.
	rec = postJSON(t, router, "/v1/auth/refresh", `{"refresh_token":"`+frank.RefreshToken+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var rotated AccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rotated))
	assert.NotEqual(t, frank.RefreshToken, rotated.RefreshToken, "rotation must replace the token")

	// The store lookup is keyed by the token digest, so no other
	// principal's rows are reachable: the isolate holds exactly frank's
	// revoked row and its successor, all owned by frank (white-box
	// confirmation of the lookup key).
	frankUser, err := store.GetUserByEmail(t.Context(), "frank@isol.test")
	require.NoError(t, err)
	require.Len(t, store.sessions, 2, "rotation revokes the presented row and mints its successor")
	for _, sess := range store.sessions {
		assert.Equal(t, frankUser.ID, sess.UserID, "every session in the isolate belongs to frank")
		assert.NotEmpty(t, sess.RefreshTokenHash, "sessions are keyed by digest, never plaintext")
	}
}
