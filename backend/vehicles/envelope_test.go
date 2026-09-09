package vehicles

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// TestEventEnvelopeCorrelationFromRequestContext pins the correlation_id
// half of the envelope-completion contract (issue #28, deferral 2): the
// platform server's request id (chi middleware.RequestID) becomes the event
// correlation_id (ADR-0002: propagated from the originating request), and
// non-HTTP callers publish an empty correlation. Actor/tenant completeness
// on the created event is pinned by TestRegisterVehicleNormalizesVINAndPublishesCreated.
func TestEventEnvelopeCorrelationFromRequestContext(t *testing.T) {
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)

	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-vehicles-1")
	v := &Vehicle{
		OwnerUserID: uuid.New(),
		VIN:         randomVIN(t),
		Make:        "Honda",
		Model:       "Accord",
		Year:        2020,
	}
	require.NoError(t, svc.RegisterVehicle(ctx, v, v.OwnerUserID))

	domain, events := pub.published()
	require.Len(t, events, 1)
	e := events[0]

	assert.Equal(t, EventDomain, domain)
	assert.Equal(t, "motivra.vehicles.vehicle.created.v1", platform.Subject(domain, e))
	assert.Equal(t, "req-vehicles-1", e.CorrelationID, "request id becomes the correlation id")
	assert.Equal(t, v.OwnerUserID.String(), e.ActorID, "the registering owner is the actor")
	assert.Empty(t, e.TenantID, "personal vehicles are tenant-less")
	assert.Empty(t, e.CausationID, "request-initiated events are initiators: causation stays null (ADR-0002)")

	// Without the middleware value in the context the correlation stays empty.
	pub2 := &fakePublisher{}
	svc2 := NewService(store, pub2)
	v2 := &Vehicle{
		OwnerUserID: uuid.New(),
		VIN:         randomVIN(t),
		Make:        "Ford",
		Model:       "Ranger",
		Year:        2021,
	}
	require.NoError(t, svc2.RegisterVehicle(context.Background(), v2, v2.OwnerUserID))
	_, events2 := pub2.published()
	require.Len(t, events2, 1)
	assert.Empty(t, events2[0].CorrelationID, "no request id in context, no correlation id")
}
