package jobs

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// TestEventEnvelopeCarriesActorAndCorrelation pins the envelope-completion
// contract of deferral 2 (issue #28): the acting principal lands on the
// envelope — not only inside the payload — and the platform server's
// request id becomes the correlation_id (ADR-0002: propagated from the
// originating request).
func TestEventEnvelopeCarriesActorAndCorrelation(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-jobs-1")

	job := jobInStore(t, store, StatusCreated)
	actor := uuid.New()
	require.NoError(t, svc.Transition(ctx, job.ID, StatusTriaging, &actor, "intake triage", opScope()))

	events := pub.ofType(t, EventJobStatusChanged)
	require.Len(t, events, 1)
	e := events[0]

	assert.Equal(t, "motivra.jobs.job.status.changed.v1", platform.Subject(EventDomain, e),
		"subject is motivra.<domain>.<event_type> (ADR-0002)")
	assert.Equal(t, actor.String(), e.ActorID, "envelope actor mirrors the transition actor")
	assert.Equal(t, "req-jobs-1", e.CorrelationID, "request id becomes the correlation id")
	assert.Equal(t, job.ID.String(), e.AggregateID)
	assert.Empty(t, e.TenantID, "jobs tables carry no tenant_id yet (documented follow-up in #28)")
	assert.Empty(t, e.CausationID, "request-initiated events are initiators: causation stays null (ADR-0002)")
	assert.NotEmpty(t, e.EventID)
	assert.Equal(t, platform.SchemaVersion, e.SchemaVersion)

	// A system actor (nil) publishes an empty envelope actor, mirroring the
	// payload's omitted actor.
	require.NoError(t, svc.Transition(ctx, job.ID, StatusDispatching, nil, "routed to dispatch", opScope()))
	events = pub.ofType(t, EventJobStatusChanged)
	require.Len(t, events, 2)
	assert.Empty(t, events[1].ActorID, "nil actor records a system actor as empty")
	assert.Equal(t, "req-jobs-1", events[1].CorrelationID)
}
