package platform

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewEventDefaults(t *testing.T) {
	e, err := NewEvent("vehicle.created.v1", "vehicle-123", "org-9", "user-5", "corr-1", map[string]any{"make": "Toyota"})
	require.NoError(t, err)
	require.NotEmpty(t, e.EventID)
	require.Equal(t, "vehicle.created.v1", e.EventType)
	require.Equal(t, 1, e.SchemaVersion)
	require.Equal(t, "vehicle-123", e.AggregateID)
	require.Equal(t, "org-9", e.TenantID)
	require.Equal(t, "user-5", e.ActorID)
	require.WithinDuration(t, time.Now().UTC(), e.Timestamp, time.Minute)
	require.JSONEq(t, `{"make":"Toyota"}`, string(e.Payload))
}

func TestNewEventValidation(t *testing.T) {
	_, err := NewEvent("", "agg", "", "", "", nil)
	require.Error(t, err)
	_, err = NewEvent("vehicle.created.v1", "", "", "", "", nil)
	require.Error(t, err)
	_, err = NewEvent("vehicle.created.v1", "agg", "", "", "", make(chan int))
	require.Error(t, err)
}

func TestSubject(t *testing.T) {
	e, _ := NewEvent("vehicle.created.v1", "v1", "", "", "", nil)
	require.Equal(t, "motivra.vehicles.vehicle.created.v1", Subject("vehicles", e))
}

func TestEventJSONRoundTrip(t *testing.T) {
	e, err := NewEvent("payment.completed.v1", "pay-1", "org-1", "user-1", "corr-1", map[string]any{"amount_minor": 250000})
	require.NoError(t, err)
	raw, err := json.Marshal(e)
	require.NoError(t, err)

	var back Event
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, e, back)
}

func TestNoopDedupe(t *testing.T) {
	d := NoopDedupe{}
	seen, err := d.Seen(nil, "evt-1")
	require.NoError(t, err)
	require.False(t, seen)
	require.NoError(t, d.MarkSeen(nil, "evt-1", time.Hour))
}
