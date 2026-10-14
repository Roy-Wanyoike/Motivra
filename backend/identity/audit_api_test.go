package identity

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Admin audit read API (issue #88): GET /v1/admin/audit. The append-only
// audit trail becomes queryable for ADMIN and SUPER_ADMIN callers with
// exact-match filters, an optional time window and offset pagination. The
// tenant scope is derived from the validated claims — an ADMIN whose token
// carries a tenant is always filtered to it, and no query parameter can
// widen that (AC: "403 for non-admin, tenant-filtered for ADMIN").
//
// The seeded rows use the dispatch domain's decision-record action string
// to prove cross-domain queryability: backend/dispatch writes dispatch
// decision records into the same append-only trail, and the audit API must
// return them with full attribution (identity does not import dispatch).
// Registration itself also writes audit rows, so the decision-row
// assertions always filter on the decision action to stay exact.

const decisionAction = "dispatch.decision.scored"

// grantAndIssue grants role to the account (optionally tenant-scoped) and
// re-issues its token the way the identity service itself would, so the
// access token reflects the new role and any org-scoped tenant claim.
func grantAndIssue(t *testing.T, svc *Service, store *fakeStore, email string, role Role, tenantID *uuid.UUID) AccessResponse {
	t.Helper()
	admin, err := store.GetUserByEmail(t.Context(), email)
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(t.Context(), admin.ID, role, tenantID))
	tokens, err := newTestIssuer().Issue(t.Context(), svc.store, admin, "console", "127.0.0.1")
	require.NoError(t, err)
	return tokens
}

// seedDecisionRecord appends one dispatch-shaped decision row.
func seedDecisionRecord(t *testing.T, store *fakeStore, objectID uuid.UUID, tenantID *uuid.UUID, actorID *uuid.UUID) {
	t.Helper()
	require.NoError(t, store.InsertAudit(t.Context(), AuditEntry{
		ActorID:    actorID,
		Action:     decisionAction,
		ObjectType: "dispatch_decision",
		ObjectID:   objectID.String(),
		TenantID:   tenantID,
		Metadata:   map[string]any{"inputs_sha256": "deadbeef", "chosen_score": 0.9},
	}))
}

// getAuditPage performs a GET /v1/admin/audit and decodes the page.
func getAuditPage(t *testing.T, router http.Handler, query, token string) auditListResponse {
	t.Helper()
	rec := getAuthed(t, router, "/v1/admin/audit"+query, token)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var page auditListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	return page
}

func TestAdminAuditEndpointAuthz(t *testing.T) {
	router, svc, store, _ := newTestAPI()

	seedDecisionRecord(t, store, uuid.New(), nil, nil)

	customer := registerPrincipal(t, router, "audit.customer@isol.test")
	rec := getAuthed(t, router, "/v1/admin/audit", customer.AccessToken)
	require.Equal(t, http.StatusForbidden, rec.Code, "a plain CUSTOMER must not read the audit trail")

	// A TECHNICIAN is equally outside the admin surface.
	techTokens := grantAndIssue(t, svc, store, "audit.customer@isol.test", RoleTechnician, nil)
	rec = getAuthed(t, router, "/v1/admin/audit", techTokens.AccessToken)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Unauthenticated probes are rejected before scoping applies.
	rec = getAuthed(t, router, "/v1/admin/audit", "")
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// SUPER_ADMIN reads the platform-wide trail, newest first, with full
	// attribution on the wire.
	registerPrincipal(t, router, "audit.super@isol.test")
	superTokens := grantAndIssue(t, svc, store, "audit.super@isol.test", RoleSuperAdmin, nil)
	page := getAuditPage(t, router, "?action="+decisionAction, superTokens.AccessToken)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, decisionAction, page.Entries[0].Action)
	assert.Equal(t, "dispatch_decision", page.Entries[0].ObjectType)
	assert.NotEmpty(t, page.Entries[0].ID, "the append-only row id is part of the projection")
	assert.NotEmpty(t, page.Entries[0].CreatedAt)
	assert.Equal(t, 20, page.Limit, "the default page size is echoed back")
}

func TestAdminAuditTenantFiltering(t *testing.T) {
	router, svc, store, _ := newTestAPI()

	tenantA, tenantB := uuid.New(), uuid.New()
	rowA, rowB, personal := uuid.New(), uuid.New(), uuid.New()
	seedDecisionRecord(t, store, rowA, &tenantA, nil)
	seedDecisionRecord(t, store, rowB, &tenantB, nil)
	seedDecisionRecord(t, store, personal, nil, nil) // tenant-less system row
	query := "?action=" + decisionAction

	registerPrincipal(t, router, "tenant.a.admin@isol.test")
	adminA := grantAndIssue(t, svc, store, "tenant.a.admin@isol.test", RoleAdmin, &tenantA)

	// The tenant-scoped ADMIN sees exactly tenant A's rows.
	page := getAuditPage(t, router, query, adminA.AccessToken)
	require.Len(t, page.Entries, 1, "tenant A admin sees exactly tenant A's rows")
	require.NotNil(t, page.Entries[0].TenantID)
	assert.Equal(t, tenantA, *page.Entries[0].TenantID)
	assert.Equal(t, rowA.String(), page.Entries[0].ObjectID)

	// A query parameter can never widen the tenant scope: ?tenant_id= is
	// ignored for non-SUPER_ADMIN callers and the claim filter still binds.
	page = getAuditPage(t, router, query+"&tenant_id="+tenantB.String(), adminA.AccessToken)
	require.Len(t, page.Entries, 1, "tenant B rows stay invisible to tenant A's admin")
	assert.Equal(t, tenantA, *page.Entries[0].TenantID)

	// A different tenant's admin sees none of tenant A's rows, even when
	// asking for them by id.
	registerPrincipal(t, router, "tenant.b.admin@isol.test")
	adminB := grantAndIssue(t, svc, store, "tenant.b.admin@isol.test", RoleAdmin, &tenantB)
	page = getAuditPage(t, router, query+"&tenant_id="+tenantA.String(), adminB.AccessToken)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, tenantB, *page.Entries[0].TenantID, "cross-tenant narrowing is refused for ADMIN")

	// A tenant-less ADMIN keeps the documented platform-wide operator
	// surface (the same posture as GET /v1/admin/users): all tenants plus
	// the tenant-less rows.
	registerPrincipal(t, router, "global.admin@isol.test")
	globalAdmin := grantAndIssue(t, svc, store, "global.admin@isol.test", RoleAdmin, nil)
	page = getAuditPage(t, router, query, globalAdmin.AccessToken)
	require.Len(t, page.Entries, 3, "tenant-less admin spans the platform trail")

	// SUPER_ADMIN spans everything and may narrow explicitly.
	registerPrincipal(t, router, "audit.super2@isol.test")
	superTokens := grantAndIssue(t, svc, store, "audit.super2@isol.test", RoleSuperAdmin, nil)
	page = getAuditPage(t, router, query, superTokens.AccessToken)
	require.Len(t, page.Entries, 3, "SUPER_ADMIN spans both tenants and personal rows")

	page = getAuditPage(t, router, query+"&tenant_id="+tenantB.String(), superTokens.AccessToken)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, tenantB, *page.Entries[0].TenantID)

	// A malformed SUPER_ADMIN narrowing filter is a validation error.
	rec := getAuthed(t, router, "/v1/admin/audit?tenant_id=not-a-uuid", superTokens.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// TestAdminAuditMalformedTenantClaimFailsClosed proves the fail-closed
// posture: an ADMIN token whose tenant claim does not parse is an
// authentication failure (401), never a silent widening to the
// platform-wide trail.
func TestAdminAuditMalformedTenantClaimFailsClosed(t *testing.T) {
	router, svc, store, _ := newTestAPI()
	registerPrincipal(t, router, "broken.tenant@isol.test")
	_ = grantAndIssue(t, svc, store, "broken.tenant@isol.test", RoleAdmin, nil)
	admin, err := store.GetUserByEmail(t.Context(), "broken.tenant@isol.test")
	require.NoError(t, err)

	// Re-mint the access token with an unparseable tenant claim — the test
	// validator accepts the token (signature, iss, aud are all valid), and
	// the handler must refuse to derive a scope from it.
	claims := jwt.MapClaims{
		"sub":       admin.ID.String(),
		"role":      "ADMIN",
		"tenant_id": "not-a-uuid",
		"iss":       testIssuer,
		"aud":       []string{testAudience},
		"exp":       time.Now().Add(15 * time.Minute).Unix(),
		"iat":       time.Now().Add(-time.Minute).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)

	rec := getAuthed(t, router, "/v1/admin/audit", signed)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "a malformed tenant claim must fail closed, body: %s", rec.Body.String())
}

func TestAdminAuditFiltersAndPagination(t *testing.T) {
	router, svc, store, _ := newTestAPI()

	actor := uuid.New()
	other := uuid.New()
	for i := 0; i < 3; i++ {
		seedDecisionRecord(t, store, uuid.New(), nil, &actor)
	}
	require.NoError(t, store.InsertAudit(t.Context(), AuditEntry{
		ActorID: &other, Action: ActionUserLogin, ObjectType: "user",
		ObjectID: other.String(), Metadata: map[string]any{"ip_address": "192.0.2.9"},
	}))

	registerPrincipal(t, router, "filter.super@isol.test")
	superTokens := grantAndIssue(t, svc, store, "filter.super@isol.test", RoleSuperAdmin, nil)

	// Exact-action filter.
	page := getAuditPage(t, router, "?action="+ActionUserLogin, superTokens.AccessToken)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, other.String(), page.Entries[0].ObjectID)

	// Actor filter.
	page = getAuditPage(t, router, "?actor_id="+actor.String(), superTokens.AccessToken)
	require.Len(t, page.Entries, 3)
	for _, e := range page.Entries {
		require.NotNil(t, e.ActorID)
		assert.Equal(t, actor, *e.ActorID)
	}

	// Object-type filter.
	page = getAuditPage(t, router, "?object_type=dispatch_decision", superTokens.AccessToken)
	require.Len(t, page.Entries, 3)

	// since/until window: a future since excludes everything, a malformed
	// one is a validation error.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	page = getAuditPage(t, router, "?since="+future, superTokens.AccessToken)
	assert.Empty(t, page.Entries)
	page = getAuditPage(t, router, "?until="+future, superTokens.AccessToken)
	assert.NotEmpty(t, page.Entries)
	rec := getAuthed(t, router, "/v1/admin/audit?since=yesterday", superTokens.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// Pagination walks the newest-first page.
	page = getAuditPage(t, router, "?action="+decisionAction+"&limit=1&offset=1", superTokens.AccessToken)
	require.Len(t, page.Entries, 1)
	require.Equal(t, 1, page.Limit)
	require.Equal(t, 1, page.Offset)

	// Malformed limit/actor filters are validation errors.
	rec = getAuthed(t, router, "/v1/admin/audit?limit=abc", superTokens.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	rec = getAuthed(t, router, "/v1/admin/audit?actor_id=zzz", superTokens.AccessToken)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// TestAdminUserReadsAreAudited pins the "admin user reads audited" AC: a
// successful GET /v1/admin/users appends identity.users.listed with full
// attribution, a failed audit fails the request, and denied reads leave no
// rows.
func TestAdminUserReadsAreAudited(t *testing.T) {
	router, svc, store, _ := newTestAPI()
	for _, name := range []string{"u1", "u2", "u3"} {
		registerPrincipal(t, router, name+"@audited.test")
	}
	customer := registerPrincipal(t, router, "plain@audited.test")

	// A denied read is not an audited event (nothing was read).
	rec := getAuthed(t, router, "/v1/admin/users", customer.AccessToken)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, store.auditsByAction(ActionUsersListed))

	registerPrincipal(t, router, "reader@audited.test")
	adminTokens := grantAndIssue(t, svc, store, "reader@audited.test", RoleAdmin, nil)
	admin, err := store.GetUserByEmail(t.Context(), "reader@audited.test")
	require.NoError(t, err)

	rec = getAuthed(t, router, "/v1/admin/users", adminTokens.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	entries := store.auditsByAction(ActionUsersListed)
	require.Len(t, entries, 1, "the privileged read must be audited exactly once")
	entry := entries[0]
	require.NotNil(t, entry.ActorID)
	assert.Equal(t, admin.ID, *entry.ActorID, "the audit row attributes the acting admin")
	assert.Nil(t, entry.TenantID, "a tenant-less admin attributes a personal read")
	assert.Equal(t, "user", entry.ObjectType)
	assert.Equal(t, "collection", entry.ObjectID)
	assert.Equal(t, 20, entry.Metadata["limit"])
	assert.Equal(t, 0, entry.Metadata["offset"])
	assert.Equal(t, 5, entry.Metadata["returned"], "every account the admin saw is counted")

	// A tenant-scoped admin's read carries their tenant on the audit row.
	tenant := uuid.New()
	registerPrincipal(t, router, "tenant.reader@audited.test")
	tenantAdminTokens := grantAndIssue(t, svc, store, "tenant.reader@audited.test", RoleAdmin, &tenant)
	rec = getAuthed(t, router, "/v1/admin/users", tenantAdminTokens.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	entries = store.auditsByAction(ActionUsersListed)
	require.Len(t, entries, 2)
	require.NotNil(t, entries[1].TenantID, "the second, tenant-scoped read is entries[1] (insertion order)")
	assert.Equal(t, tenant, *entries[1].TenantID, "the tenant scope rides the audit row")

	// A failed audit record fails the request (ADR-0004: signals are never
	// lost silently) — the directory page is NOT served.
	svc2, store2 := newTestService()
	store2.failAudit = true
	_, err = svc2.ListUsersForAdmin(t.Context(), admin.ID, nil, 10, 0)
	require.Error(t, err, "audit failure must fail the read")
}
