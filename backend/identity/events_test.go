package identity

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// recordingPublisher captures published events for assertions, mirroring
// the capturePublisher (jobs) and fakePublisher (vehicles) fakes.
type recordingPublisher struct {
	mu     sync.Mutex
	events []platform.Event
	domain []string
	fail   bool
}

func (p *recordingPublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("injected publish failure")
	}
	p.domain = append(p.domain, domain)
	p.events = append(p.events, e)
	return nil
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

// newEventTestService wires a Service with the in-memory store and the
// recording publisher.
func newEventTestService() (*Service, *fakeStore, *recordingPublisher) {
	store := newFakeStore()
	pub := &recordingPublisher{}
	return NewService(store, newTestIssuer(), pub), store, pub
}

func TestRegisterPublishesUserCreated(t *testing.T) {
	svc, store, pub := newEventTestService()

	resp, err := svc.Register(t.Context(), "Event-User@Example.com", "+254700000099", "lovelace-123", "Event User", "pixel-9", "127.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)

	require.Equal(t, 1, pub.count(), "one completed registration publishes exactly one event")
	pub.mu.Lock()
	e, domain := pub.events[0], pub.domain[0]
	pub.mu.Unlock()

	user, err := store.GetUserByEmail(t.Context(), "event-user@example.com")
	require.NoError(t, err)

	// Domain and topic discipline (ADR-0002).
	assert.Equal(t, EventDomain, domain, "events publish under the identity domain")
	assert.Equal(t, "motivra.identity.user.created.v1", platform.Subject(domain, e))
	assert.Equal(t, EventUserCreated, e.EventType)
	assert.Equal(t, platform.SchemaVersion, e.SchemaVersion)

	// Envelope completeness.
	_, err = uuid.Parse(e.EventID)
	assert.NoError(t, err, "event_id must be a UUID (the dedupe key)")
	assert.Equal(t, user.ID.String(), e.AggregateID)
	assert.Empty(t, e.TenantID, "personal accounts are tenant-less: the users table carries no tenant_id")
	assert.Equal(t, user.ID.String(), e.ActorID, "the registrant causes their own account creation")
	assert.False(t, e.Timestamp.IsZero())
	assert.Empty(t, e.CausationID, "request-initiated events are initiators: causation stays null (ADR-0002)")

	// Payload: minimal, IDs and state names, no secrets.
	var payload UserCreatedPayload
	require.NoError(t, json.Unmarshal(e.Payload, &payload))
	assert.Equal(t, user.ID, payload.UserID)
	assert.Equal(t, StatusActive, payload.Status)
	assert.Equal(t, []Role{RoleCustomer}, payload.Roles)
	assert.NotContains(t, string(e.Payload), "password", "payload must never carry credential material")
	assert.NotContains(t, string(e.Payload), "lovelace-123")
	assert.NotContains(t, string(e.Payload), "127.0.0.1", "payload stays minimal: no transport metadata")
}

func TestRegisterPublishesNothingOnFailure(t *testing.T) {
	svc, _, pub := newEventTestService()

	// Validation failure: no write, no event.
	_, err := svc.Register(t.Context(), "not-an-email", "", "lovelace-123", "Event User", "", "")
	require.Error(t, err)
	assert.Equal(t, 0, pub.count(), "validation failures publish nothing")

	// Store conflict: the write did not succeed, no event.
	_, err = svc.Register(t.Context(), "dup@example.com", "", "lovelace-123", "First", "", "")
	require.NoError(t, err)
	require.Equal(t, 1, pub.count())
	_, err = svc.Register(t.Context(), "DUP@example.com", "+254700000098", "lovelace-123", "Second", "", "")
	require.Error(t, err, "duplicate email must conflict")
	assert.Equal(t, 1, pub.count(), "conflicted registrations publish nothing")

	// Publisher failure surfaces: events are part of the domain contract.
	svc2, _, pub2 := newEventTestService()
	pub2.fail = true
	_, err = svc2.Register(t.Context(), "failed-publish@example.com", "", "lovelace-123", "Event User", "", "")
	require.Error(t, err, "a failed publish must fail the request")
	assert.Equal(t, 0, pub2.count())
}

func TestAssignRolePublishesRoleGranted(t *testing.T) {
	svc, store, pub := newEventTestService()

	tenant := uuid.New()
	actor := uuid.New()
	resp, err := svc.Register(t.Context(), "grantee@example.com", "", "lovelace-123", "Grantee", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken, "the seed registration must issue a token")
	grantee, err := store.GetUserByEmail(t.Context(), "grantee@example.com")
	require.NoError(t, err)
	require.Equal(t, 1, pub.count(), "registration seeds user.created only")

	ctx := context.WithValue(t.Context(), middleware.RequestIDKey, "req-grant-42")
	require.NoError(t, svc.AssignRole(ctx, grantee.ID, RoleFleetAdmin, &tenant, actor))

	require.Equal(t, 2, pub.count(), "the grant adds exactly one event")
	pub.mu.Lock()
	e := pub.events[1]
	pub.mu.Unlock()

	assert.Equal(t, "motivra.identity.role.granted.v1", platform.Subject(EventDomain, e))
	assert.Equal(t, EventRoleGranted, e.EventType)
	_, err = uuid.Parse(e.EventID)
	assert.NoError(t, err)
	assert.Equal(t, grantee.ID.String(), e.AggregateID)
	assert.Equal(t, tenant.String(), e.TenantID, "tenant-scoped grants carry the tenant on the envelope")
	assert.Equal(t, actor.String(), e.ActorID, "the granting principal is the actor")
	assert.Equal(t, "req-grant-42", e.CorrelationID, "correlation_id comes from the platform request-ID middleware")

	var payload RoleGrantedPayload
	require.NoError(t, json.Unmarshal(e.Payload, &payload))
	assert.Equal(t, grantee.ID, payload.UserID)
	assert.Equal(t, RoleFleetAdmin, payload.Role)
	require.NotNil(t, payload.TenantID)
	assert.Equal(t, tenant, *payload.TenantID)

	// The grant is audited (privileged action) and readable through grants.
	entries := store.auditsByAction(ActionRoleGranted)
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].ActorID)
	assert.Equal(t, actor, *entries[0].ActorID)
}

func TestAssignRolePublishesNothingOnFailure(t *testing.T) {
	svc, store, pub := newEventTestService()

	resp, err := svc.Register(t.Context(), "role-fail@example.com", "", "lovelace-123", "Role Fail", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, resp.AccessToken)
	user, err := store.GetUserByEmail(t.Context(), "role-fail@example.com")
	require.NoError(t, err)
	require.Equal(t, 1, pub.count())

	tenant := uuid.New()
	actor := uuid.New()

	// Unknown user: the write fails, no event.
	unknown := uuid.New()
	err = svc.AssignRole(t.Context(), unknown, RoleGarage, nil, actor)
	require.True(t, errors.Is(err, ErrUserNotFound), "unknown grantee must surface as not-found, got %v", err)
	assert.Equal(t, 1, pub.count())

	// Duplicate grant: the write conflicts, no event.
	require.NoError(t, svc.AssignRole(t.Context(), user.ID, RoleGarage, nil, actor))
	require.Equal(t, 2, pub.count())
	err = svc.AssignRole(t.Context(), user.ID, RoleGarage, nil, actor)
	require.Error(t, err, "duplicate grant must conflict")
	assert.Equal(t, 2, pub.count(), "conflicted grants publish nothing")

	// Same role, different tenant scope: a distinct grant, so it succeeds.
	require.NoError(t, svc.AssignRole(t.Context(), user.ID, RoleGarage, &tenant, actor))
	assert.Equal(t, 3, pub.count())
}

func TestRegisterCorrelationFromRequestContext(t *testing.T) {
	svc, _, pub := newEventTestService()

	// The platform server middleware puts the request ID in the context
	// (chi middleware.RequestID); the envelope must inherit it as the
	// correlation_id (ADR-0002).
	ctx := context.WithValue(t.Context(), middleware.RequestIDKey, "req-corr-7")
	_, err := svc.Register(ctx, "corr@example.com", "", "lovelace-123", "Corr", "", "")
	require.NoError(t, err)
	require.Equal(t, 1, pub.count())
	assert.Equal(t, "req-corr-7", pub.events[0].CorrelationID)

	// Non-HTTP callers carry no request ID: correlation stays empty.
	svc2, _, pub2 := newEventTestService()
	_, err = svc2.Register(context.Background(), "corr2@example.com", "", "lovelace-123", "Corr2", "", "")
	require.NoError(t, err)
	require.Equal(t, 1, pub2.count())
	assert.Empty(t, pub2.events[0].CorrelationID)
}

// Compile-time guard that the recording publisher satisfies the platform
// contract it stands in for.
var _ platform.Publisher = (*recordingPublisher)(nil)
