package inspections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validTemplateContent returns checklist content in two byte layouts with
// identical semantics (key order differs) — the canonicalization proof
// material for hash pinning.
func validTemplateContent(keyOrderA, keyOrderB bool) json.RawMessage {
	if keyOrderA {
		return json.RawMessage(`{"items":[{"key":"brakes","label":"Brakes","required":true}],"title":"Partner checklist"}`)
	}
	return json.RawMessage(`{"title":"Partner checklist","items":[{"required":true,"label":"Brakes","key":"brakes"}]}`)
}

func authorInput(name string, content json.RawMessage) AuthoredTemplateInput {
	return AuthoredTemplateInput{Name: name, Content: content}
}

func TestCreateAuthoredTemplateDraft(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	tenant := uuid.New()
	author := uuid.New()

	tmpl, err := svc.CreateAuthoredTemplate(context.Background(), author, tenant,
		authorInput("Partner Brake Checklist", validTemplateContent(true, false)), tenantScope(tenant))
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, tmpl.ID)
	assert.Equal(t, AuthoredDraft, tmpl.Status)
	assert.Empty(t, tmpl.ContentHash, "drafts are not hash-pinned")
	assert.Nil(t, tmpl.PublishedAt)

	// The draft is visible to its tenant scope and stored canonically.
	got, err := svc.GetAuthoredTemplate(context.Background(), tmpl.ID, tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, AuthoredDraft, got.Status)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got.Content, &decoded))
	items, ok := decoded["items"].([]any)
	require.True(t, ok, "content stored as canonical object with items array")
	assert.Len(t, items, 1)

	// Exactly one draft row in the fake store; no audit rows yet (draft
	// edits are not privileged transitions).
	assert.Len(t, store.authoredTemplates, 1)
	assert.Empty(t, store.authoredTemplateAudits)
}

func TestCreateAuthoredTemplateTenantMismatchForbidden(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant, otherTenant := uuid.New(), uuid.New()

	_, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), otherTenant,
		authorInput("Checklist", validTemplateContent(true, false)), tenantScope(tenant))
	require.ErrorIs(t, err, ErrAuthoredTemplateForbidden)
}

func TestCreateAuthoredTemplateOperatorMayAuthorForAnyTenant(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()

	tmpl, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Operator Checklist", validTemplateContent(false, true)), opScope())
	require.NoError(t, err)
	assert.Equal(t, tenant, tmpl.TenantID)
}

func TestCreateAuthoredTemplateValidation(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()
	cases := []struct {
		name    string
		input   AuthoredTemplateInput
		wantErr error
	}{
		{"empty name", AuthoredTemplateInput{Name: "", Content: validTemplateContent(true, false)}, ErrInvalidInput},
		{"name too long", AuthoredTemplateInput{Name: string(make([]byte, 201)), Content: validTemplateContent(true, false)}, ErrInvalidInput},
		{"invalid json", AuthoredTemplateInput{Name: "X", Content: json.RawMessage(`{broken`)}, ErrInvalidInput},
		{"not an object", AuthoredTemplateInput{Name: "X", Content: json.RawMessage(`[1,2,3]`)}, ErrInvalidInput},
		{"missing items", AuthoredTemplateInput{Name: "X", Content: json.RawMessage(`{"title":"no items"}`)}, ErrInvalidInput},
		{"empty items", AuthoredTemplateInput{Name: "X", Content: json.RawMessage(`{"items":[]}`)}, ErrInvalidInput},
	}
	for _, tc := range cases {
		_, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant, tc.input, tenantScope(tenant))
		require.ErrorIs(t, err, tc.wantErr, tc.name)
	}
}

func TestUpdateAuthoredTemplateDraft(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()
	tmpl, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Draft Name", validTemplateContent(true, false)), tenantScope(tenant))
	require.NoError(t, err)

	updated, err := svc.UpdateAuthoredTemplateDraft(context.Background(), tmpl.ID,
		authorInput("Renamed Checklist", validTemplateContent(false, true)), tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, "Renamed Checklist", updated.Name)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(updated.Content, &decoded))
	assert.Equal(t, "Partner checklist", decoded["title"], "content re-canonicalized on update")
}

func TestUpdatePublishedTemplateConflict(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()
	tmpl, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Pinned Checklist", validTemplateContent(true, false)), tenantScope(tenant))
	_, err := svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)

	_, err = svc.UpdateAuthoredTemplateDraft(context.Background(), tmpl.ID,
		authorInput("Tampered", validTemplateContent(false, true)), tenantScope(tenant))
	require.ErrorIs(t, err, ErrConflict, "published templates are hash-pinned and immutable")
}

func TestPublishAuthoredTemplateHashPinning(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	tenant := uuid.New()
	publisher := uuid.New()
	tmpl, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Pinned Checklist", validTemplateContent(true, false)), tenantScope(tenant))
	require.NoError(t, err)

	published, err := svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, publisher, tenantScope(tenant))
	require.NoError(t, err)
	require.NotEmpty(t, published.ContentHash)
	assert.Equal(t, AuthoredPublished, published.Status)
	require.NotNil(t, published.PublishedAt)

	// The pinned hash is the SHA-256 of the CANONICAL content: the same
	// semantics in a different byte layout hash identically.
	want := sha256.Sum256([]byte(published.Content))
	assert.Equal(t, hex.EncodeToString(want[:]), published.ContentHash)

	equivalent, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Pinned Checklist B", validTemplateContent(false, true)), tenantScope(tenant))
	require.NoError(t, err)
	pinned2, err := svc.PublishAuthoredTemplate(context.Background(), equivalent.ID, publisher, tenantScope(tenant))
	require.NoError(t, err)
	// Same canonical content, different template names: the pin covers
	// content only, so the hashes are identical by design.
	assert.Equal(t, published.ContentHash, pinned2.ContentHash, "hash pins content, not the template name")
	// The canonical bytes of both inputs are semantically identical except
	// the name lives outside content; prove canonicalization directly.
	c1, err := canonicalTemplateContent(validTemplateContent(true, false))
	require.NoError(t, err)
	c2, err := canonicalTemplateContent(validTemplateContent(false, true))
	require.NoError(t, err)
	assert.Equal(t, string(c1), string(c2), "key order must not change the canonical form")
	assert.Equal(t, hashTemplateContent(c1), hashTemplateContent(c2))

	// Audit + event: exactly one audited, published transition.
	require.Len(t, store.authoredTemplateAudits, 2)
	assert.Equal(t, AuditActionTemplatePublished, store.authoredTemplateAudits[0].Action)
	assert.Equal(t, publisher, store.authoredTemplateAudits[0].ActorID)
	assert.Equal(t, AuditObjectTypeTemplate, store.authoredTemplateAudits[0].ObjectType)
	require.Len(t, pub.events, 2)
	assert.Equal(t, EventAuthoredTemplatePublished, pub.events[0].EventType)
}

func TestPublishTwiceConflict(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()
	tmpl, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Once Only", validTemplateContent(true, false)), tenantScope(tenant))
	_, err := svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)

	_, err = svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.ErrorIs(t, err, ErrConflict)
}

func TestPublishNameTakenWhilePublished(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenant := uuid.New()
	first, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Same Name", validTemplateContent(true, false)), tenantScope(tenant))
	_, err := svc.PublishAuthoredTemplate(context.Background(), first.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)

	second, err := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Same Name", validTemplateContent(false, true)), tenantScope(tenant))
	require.NoError(t, err, "draft may share the name while the published version is live")
	_, err = svc.PublishAuthoredTemplate(context.Background(), second.ID, uuid.New(), tenantScope(tenant))
	require.ErrorIs(t, err, ErrTemplateNameTaken)

	// Retiring the first frees the name.
	_, err = svc.RetireAuthoredTemplate(context.Background(), first.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)
	_, err = svc.PublishAuthoredTemplate(context.Background(), second.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)
}

func TestRetireLifecycle(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	tenant := uuid.New()
	retirer := uuid.New()
	tmpl, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Short Lived", validTemplateContent(true, false)), tenantScope(tenant))
	published, err := svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.NoError(t, err)

	retired, err := svc.RetireAuthoredTemplate(context.Background(), tmpl.ID, retirer, tenantScope(tenant))
	require.NoError(t, err)
	assert.Equal(t, AuthoredRetired, retired.Status)
	require.NotNil(t, retired.RetiredAt)
	assert.Equal(t, published.ContentHash, retired.ContentHash, "hash stays pinned through retirement")

	// Terminal: no resurrection, no re-retire, no edit.
	_, err = svc.PublishAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.ErrorIs(t, err, ErrConflict)
	_, err = svc.RetireAuthoredTemplate(context.Background(), tmpl.ID, uuid.New(), tenantScope(tenant))
	require.ErrorIs(t, err, ErrConflict)

	// Drafts cannot retire (nothing pinned yet).
	draft, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenant,
		authorInput("Not Yet", validTemplateContent(false, true)), tenantScope(tenant))
	_, err = svc.RetireAuthoredTemplate(context.Background(), draft.ID, uuid.New(), tenantScope(tenant))
	require.ErrorIs(t, err, ErrConflict)

	require.Len(t, store.authoredTemplateAudits, 2)
	assert.Equal(t, AuditActionTemplateRetired, store.authoredTemplateAudits[1].Action)
	require.Len(t, pub.events, 2)
	assert.Equal(t, EventAuthoredTemplateRetired, pub.events[1].EventType)
}

func TestListAuthoredTemplatesScopingAndFiltering(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenantA, tenantB := uuid.New(), uuid.New()

	// Tenant A: one published, one draft, one retired. Tenant B: one published.
	aPub, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenantA,
		authorInput("A Published", validTemplateContent(true, false)), tenantScope(tenantA))
	_, _ = svc.PublishAuthoredTemplate(context.Background(), aPub.ID, uuid.New(), tenantScope(tenantA))
	_, _ = svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenantA,
		authorInput("A Draft", validTemplateContent(false, true)), tenantScope(tenantA))
	aRetired, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenantA,
		authorInput("A Retired", validTemplateContent(false, true)), tenantScope(tenantA))
	_, _ = svc.PublishAuthoredTemplate(context.Background(), aRetired.ID, uuid.New(), tenantScope(tenantA))
	_, _ = svc.RetireAuthoredTemplate(context.Background(), aRetired.ID, uuid.New(), tenantScope(tenantA))
	bPub, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenantB,
		authorInput("B Published", validTemplateContent(true, false)), tenantScope(tenantB))
	_, _ = svc.PublishAuthoredTemplate(context.Background(), bPub.ID, uuid.New(), tenantScope(tenantB))

	// Tenant A scope: all three of its own, never tenant B's.
	all, err := svc.ListAuthoredTemplates(context.Background(), tenantScope(tenantA), nil, 0, 0)
	require.NoError(t, err)
	assert.Len(t, all, 3)
	for _, at := range all {
		assert.Equal(t, tenantA, at.TenantID)
	}

	// Published filter.
	published := AuthoredPublished
	pubA, err := svc.ListAuthoredTemplates(context.Background(), tenantScope(tenantA), &published, 0, 0)
	require.NoError(t, err)
	assert.Len(t, pubA, 1)
	assert.Equal(t, "A Published", pubA[0].Name)

	// Operator scope sees everything.
	op, err := svc.ListAuthoredTemplates(context.Background(), opScope(), nil, 0, 0)
	require.NoError(t, err)
	assert.Len(t, op, 4)

	// Principal-only scope is fail-closed: templates are tenant content.
	principal := uuid.New()
	none, err := svc.ListAuthoredTemplates(context.Background(), staffScope(principal), nil, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, none, "principal-only scope sees no tenant content")

	// Unknown status filter.
	bad := AuthoredTemplateStatus("live")
	_, err = svc.ListAuthoredTemplates(context.Background(), tenantScope(tenantA), &bad, 0, 0)
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestGetAuthoredTemplateCrossTenantNotFound(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := newFixture()
	tenantA, tenantB := uuid.New(), uuid.New()
	tmpl, _ := svc.CreateAuthoredTemplate(context.Background(), uuid.New(), tenantA,
		authorInput("A Only", validTemplateContent(true, false)), tenantScope(tenantA))

	_, err := svc.GetAuthoredTemplate(context.Background(), tmpl.ID, tenantScope(tenantB))
	require.ErrorIs(t, err, ErrAuthoredTemplateNotFound, "cross-tenant reads are 404-equivalent, no existence leak")
}
