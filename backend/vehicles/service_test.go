package vehicles

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// fakeStore is an in-memory Store used to exercise the Service without a
// database. It mirrors the real store's contract, including unique-VIN
// conflicts, foreign-key failures and the seeded registration note.
type fakeStore struct {
	mu       sync.Mutex
	vehicles map[uuid.UUID]Vehicle
	byVIN    map[string]uuid.UUID
	history  map[uuid.UUID][]HistoryEvent
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		vehicles: make(map[uuid.UUID]Vehicle),
		byVIN:    make(map[string]uuid.UUID),
		history:  make(map[uuid.UUID][]HistoryEvent),
	}
}

func (f *fakeStore) CreateVehicle(_ context.Context, v *Vehicle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byVIN[v.VIN]; exists {
		return &pgconn.PgError{Code: "23505"}
	}
	now := time.Now().UTC()
	v.CreatedAt = now
	v.UpdatedAt = now
	stored := *v
	f.vehicles[v.ID] = stored
	f.byVIN[v.VIN] = v.ID
	owner := v.OwnerUserID
	f.history[v.ID] = append(f.history[v.ID], HistoryEvent{
		VehicleID:  v.ID,
		EventType:  HistoryEventTypeNote,
		OccurredAt: now,
		Summary:    initialHistorySummary,
		RecordedBy: &owner,
		CreatedAt:  now,
	})
	return nil
}

func (f *fakeStore) GetVehicle(_ context.Context, vehicleID uuid.UUID) (Vehicle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vehicles[vehicleID]
	if !ok {
		return Vehicle{}, fmt.Errorf("vehicles: get vehicle %s: %w", vehicleID, ErrVehicleNotFound)
	}
	return v, nil
}

func (f *fakeStore) ListVehiclesByOwner(_ context.Context, ownerID uuid.UUID) ([]Vehicle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Vehicle, 0)
	for _, v := range f.vehicles {
		if v.OwnerUserID == ownerID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f *fakeStore) RecordHistoryEvent(_ context.Context, e HistoryEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vehicles[e.VehicleID]; !ok {
		return fmt.Errorf("vehicles: record history event: %w", ErrVehicleNotFound)
	}
	f.history[e.VehicleID] = append(f.history[e.VehicleID], e)
	return nil
}

func (f *fakeStore) ListHistory(_ context.Context, vehicleID uuid.UUID, limit, offset int) ([]HistoryEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	if offset < 0 {
		offset = 0
	}
	events := make([]HistoryEvent, len(f.history[vehicleID]))
	copy(events, f.history[vehicleID])
	sort.SliceStable(events, func(i, j int) bool { return events[i].OccurredAt.After(events[j].OccurredAt) })
	if offset >= len(events) {
		return []HistoryEvent{}, nil
	}
	events = events[offset:]
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (f *fakeStore) GetPassport(_ context.Context, vehicleID uuid.UUID) (Passport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vehicles[vehicleID]
	if !ok {
		return Passport{}, fmt.Errorf("vehicles: get passport %s: %w", vehicleID, ErrVehicleNotFound)
	}
	p := Passport{
		ID:                v.ID,
		TenantID:          v.TenantID,
		OwnerUserID:       v.OwnerUserID,
		VIN:               v.VIN,
		Make:              v.Make,
		Model:             v.Model,
		Year:              v.Year,
		Plate:             v.Plate,
		Color:             v.Color,
		MileageLatestKm:   v.MileageLatestKm,
		ServiceEventCount: int64(len(f.history[vehicleID])),
		CreatedAt:         v.CreatedAt,
	}
	for _, e := range f.history[vehicleID] {
		if p.LastServiceAt == nil || e.OccurredAt.After(*p.LastServiceAt) {
			last := e.OccurredAt
			p.LastServiceAt = &last
		}
	}
	return p, nil
}

func (f *fakeStore) UpdateMileage(_ context.Context, vehicleID uuid.UUID, km int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vehicles[vehicleID]
	if !ok {
		return fmt.Errorf("vehicles: update mileage %s: %w", vehicleID, ErrVehicleNotFound)
	}
	now := time.Now().UTC()
	v.MileageLatestKm = km
	v.UpdatedAt = now
	f.vehicles[vehicleID] = v
	reading := km
	f.history[vehicleID] = append(f.history[vehicleID], HistoryEvent{
		VehicleID:  vehicleID,
		EventType:  HistoryEventTypeMileage,
		OccurredAt: now,
		OdometerKm: &reading,
		Summary:    fmt.Sprintf("Odometer reading recorded: %d km", km),
		CreatedAt:  now,
	})
	return nil
}

// fakePublisher captures published events for assertions.
type fakePublisher struct {
	mu     sync.Mutex
	domain string
	events []platform.Event
}

func (f *fakePublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domain = domain
	f.events = append(f.events, e)
	return nil
}

func (f *fakePublisher) published() (string, []platform.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.domain, append([]platform.Event(nil), f.events...)
}

// requireStatus asserts that err is a platform error with the given HTTP
// status.
func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	appErr := platform.AsError(err)
	require.Equal(t, status, appErr.Status)
}

// payloadMap decodes an event payload into a generic map.
func payloadMap(t *testing.T, e platform.Event) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &m))
	return m
}

// registerTestVehicle registers a valid vehicle on svc and returns it.
func registerTestVehicle(t *testing.T, svc *Service, mileage int64) *Vehicle {
	t.Helper()
	v := &Vehicle{
		OwnerUserID:     uuid.New(),
		VIN:             "1HGCM82633A004352",
		Make:            "Honda",
		Model:           "Accord",
		Year:            2020,
		Plate:           "KDA 123A",
		Color:           "Silver",
		MileageLatestKm: mileage,
	}
	require.NoError(t, svc.RegisterVehicle(context.Background(), v, v.OwnerUserID))
	return v
}

func TestRegisterVehicleNormalizesVINAndPublishesCreated(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)

	tenant := uuid.New()
	v := &Vehicle{
		TenantID:        &tenant,
		OwnerUserID:     uuid.New(),
		VIN:             " 1hg-cm826 33a004352 ",
		Make:            " honda ",
		Model:           " accord ",
		Year:            2020,
		Plate:           " kda 123a ",
		Color:           " silver ",
		MileageLatestKm: 1200,
	}
	require.NoError(t, svc.RegisterVehicle(context.Background(), v, v.OwnerUserID))

	assert.Equal(t, "1HGCM82633A004352", v.VIN, "VIN must be normalized and uppercased")
	assert.Equal(t, "honda", v.Make, "make must be trimmed")
	assert.Equal(t, "accord", v.Model, "model must be trimmed")
	assert.NotEqual(t, uuid.Nil, v.ID, "store must assign an ID")

	domain, events := pub.published()
	assert.Equal(t, EventDomain, domain)
	require.Len(t, events, 1, "exactly one vehicle.created.v1 event must be published")
	ev := events[0]
	assert.Equal(t, EventVehicleCreated, ev.EventType)
	assert.Equal(t, v.ID.String(), ev.AggregateID)
	assert.Equal(t, tenant.String(), ev.TenantID)
	assert.Equal(t, v.OwnerUserID.String(), ev.ActorID)

	payload := payloadMap(t, ev)
	assert.Equal(t, "1HGCM82633A004352", payload["vin"])
	assert.Equal(t, v.OwnerUserID.String(), payload["owner_user_id"])
	assert.Equal(t, float64(1200), payload["mileage_latest_km"])

	history, err := store.ListHistory(context.Background(), v.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, history, 1, "registration must seed exactly one history event")
	assert.Equal(t, HistoryEventTypeNote, history[0].EventType)
	assert.Equal(t, initialHistorySummary, history[0].Summary)
}

func TestRegisterVehicleRejectsInvalidVIN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		vin  string
	}{
		{name: "bad checksum", vin: "1HGCM82633A004353"},
		{name: "too short", vin: "1HGCM82633A00435"},
		{name: "contains forbidden I", vin: "1HGCM826I3A004352"},
		{name: "empty", vin: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			pub := &fakePublisher{}
			svc := NewService(store, pub)

			v := &Vehicle{OwnerUserID: uuid.New(), VIN: tc.vin, Make: "Honda", Model: "Accord", Year: 2020}
			err := svc.RegisterVehicle(context.Background(), v, v.OwnerUserID)
			requireStatus(t, err, http.StatusUnprocessableEntity)

			_, events := pub.published()
			assert.Empty(t, events, "no event may be published for a rejected vehicle")
			assert.Empty(t, store.vehicles, "a rejected vehicle must not be stored")
		})
	}
}

func TestRegisterVehicleRejectsYearOutOfRange(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, nil)

	for _, year := range []int{MinYearOfManufacture - 1, MaxYearOfManufacture + 1} {
		v := &Vehicle{OwnerUserID: uuid.New(), VIN: "1HGCM82633A004352", Make: "Honda", Model: "Accord", Year: year}
		err := svc.RegisterVehicle(context.Background(), v, v.OwnerUserID)
		requireStatus(t, err, http.StatusUnprocessableEntity)
	}
}

func TestRegisterVehicleRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	svc := NewService(newFakeStore(), nil)

	v := &Vehicle{VIN: "1HGCM82633A004352", Make: "Honda", Model: "Accord", Year: 2020}
	err := svc.RegisterVehicle(context.Background(), v, uuid.Nil)
	requireStatus(t, err, http.StatusUnprocessableEntity)
}

func TestRegisterVehicleDuplicateVINReturnsConflict(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)

	registerTestVehicle(t, svc, 0)
	second := &Vehicle{
		OwnerUserID: uuid.New(),
		VIN:         " 1HGCM82633A004352 ", // same VIN, raw form
		Make:        "Honda",
		Model:       "Civic",
		Year:        2021,
	}
	err := svc.RegisterVehicle(context.Background(), second, second.OwnerUserID)
	requireStatus(t, err, http.StatusConflict)

	_, events := pub.published()
	require.Len(t, events, 1, "only the first registration may publish an event")
	assert.Equal(t, EventVehicleCreated, events[0].EventType)
}

func TestRegisterVehicleWithoutPublisherSkipsEvents(t *testing.T) {
	t.Parallel()
	svc := NewService(newFakeStore(), nil)

	v := registerTestVehicle(t, svc, 100)
	assert.NotEqual(t, uuid.Nil, v.ID, "registration must succeed without a publisher")
}

func TestRecordEventValidatesType(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)
	v := registerTestVehicle(t, svc, 0)

	err := svc.RecordEvent(context.Background(), &HistoryEvent{
		VehicleID: v.ID,
		EventType: "oil-change",
		Summary:   "not a valid type",
	})
	requireStatus(t, err, http.StatusUnprocessableEntity)

	_, events := pub.published()
	require.Len(t, events, 1, "only the registration event may exist so far")
	assert.Equal(t, EventVehicleCreated, events[0].EventType)

	recorder := uuid.New()
	require.NoError(t, svc.RecordEvent(context.Background(), &HistoryEvent{
		VehicleID:   v.ID,
		EventType:   HistoryEventTypeRepair,
		Summary:     " Replaced alternator ",
		EvidenceRef: "evidence://photos/alt-1",
		RecordedBy:  &recorder,
	}))

	_, events = pub.published()
	require.Len(t, events, 2)
	ev := events[1]
	assert.Equal(t, EventVehicleHistoryUpdated, ev.EventType)
	assert.Equal(t, v.ID.String(), ev.AggregateID)
	assert.Equal(t, recorder.String(), ev.ActorID)
	payload := payloadMap(t, ev)
	assert.Equal(t, HistoryEventTypeRepair, payload["event_type"])
	assert.Equal(t, "Replaced alternator", payload["summary"])
}

func TestRecordEventDefaultsOccurredAtAndID(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, nil)
	v := registerTestVehicle(t, svc, 0)

	e := &HistoryEvent{VehicleID: v.ID, EventType: HistoryEventTypeInspection, Summary: "Pre-purchase inspection"}
	before := time.Now().UTC()
	require.NoError(t, svc.RecordEvent(context.Background(), e))
	assert.NotEqual(t, uuid.Nil, e.ID, "event ID must be assigned")
	assert.False(t, e.OccurredAt.IsZero(), "occurred_at must default to now")
	assert.WithinDuration(t, before, e.OccurredAt, 5*time.Second)
}

func TestRecordEventVehicleNotFound(t *testing.T) {
	t.Parallel()
	svc := NewService(newFakeStore(), nil)

	err := svc.RecordEvent(context.Background(), &HistoryEvent{
		VehicleID: uuid.New(),
		EventType: HistoryEventTypeNote,
		Summary:   "orphaned",
	})
	requireStatus(t, err, http.StatusNotFound)
	assert.Equal(t, "not_found", platform.AsError(err).Code)
}

func TestHistoryReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, nil)
	v := registerTestVehicle(t, svc, 0)

	base := time.Now().UTC().Add(-time.Hour)
	ids := make([]uuid.UUID, 0, 3)
	for i, offset := range []time.Duration{0, 30 * time.Minute, 59 * time.Minute} {
		e := &HistoryEvent{
			VehicleID:  v.ID,
			EventType:  HistoryEventTypeNote,
			Summary:    fmt.Sprintf("event %d", i),
			OccurredAt: base.Add(offset),
		}
		require.NoError(t, svc.RecordEvent(context.Background(), e))
		ids = append(ids, e.ID)
	}

	got, err := svc.History(context.Background(), v.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 4, "seeded note plus three recorded events")
	assert.Equal(t, HistoryEventTypeNote, got[0].EventType, "the seeded note carries the registration timestamp")
	assert.Equal(t, ids[2], got[1].ID, "newest recorded event must come first")
	assert.Equal(t, ids[1], got[2].ID)
	assert.Equal(t, ids[0], got[3].ID)
	for i := 1; i < len(got); i++ {
		assert.False(t, got[i-1].OccurredAt.Before(got[i].OccurredAt), "history must be ordered occurred_at DESC")
	}
}

func TestRecordMileageRejectsNegative(t *testing.T) {
	t.Parallel()
	svc := NewService(newFakeStore(), nil)

	err := svc.RecordMileage(context.Background(), uuid.New(), -1, nil)
	requireStatus(t, err, http.StatusUnprocessableEntity)
}

func TestRecordMileageRejectsDecrease(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)
	v := registerTestVehicle(t, svc, 1000)

	err := svc.RecordMileage(context.Background(), v.ID, 999, nil)
	requireStatus(t, err, http.StatusConflict)

	got, err := svc.GetVehicle(context.Background(), v.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1000), got.MileageLatestKm, "mileage must be unchanged after a rejected decrease")

	_, events := pub.published()
	require.Len(t, events, 1, "a rejected reading must not publish an event")
	assert.Equal(t, EventVehicleCreated, events[0].EventType)
}

func TestRecordMileagePublishesOldAndNewKm(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &fakePublisher{}
	svc := NewService(store, pub)
	v := registerTestVehicle(t, svc, 1000)

	recorder := uuid.New()
	require.NoError(t, svc.RecordMileage(context.Background(), v.ID, 1500, &recorder))

	_, events := pub.published()
	require.Len(t, events, 2)
	ev := events[1]
	assert.Equal(t, EventVehicleMileageRecorded, ev.EventType)
	assert.Equal(t, v.ID.String(), ev.AggregateID)
	assert.Equal(t, recorder.String(), ev.ActorID)

	payload := payloadMap(t, ev)
	assert.Equal(t, float64(1000), payload["old_km"])
	assert.Equal(t, float64(1500), payload["new_km"])

	got, err := svc.GetVehicle(context.Background(), v.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1500), got.MileageLatestKm)

	p, err := svc.Passport(context.Background(), v.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1500), p.MileageLatestKm)
	assert.Equal(t, int64(2), p.ServiceEventCount, "registration note plus mileage event")
	require.NotNil(t, p.LastServiceAt)
}

func TestRecordMileageVehicleNotFound(t *testing.T) {
	t.Parallel()
	svc := NewService(newFakeStore(), nil)

	err := svc.RecordMileage(context.Background(), uuid.New(), 100, nil)
	requireStatus(t, err, http.StatusNotFound)
}

func TestGetVehicleMapsMissingRowToNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc := NewService(store, nil)
	v := registerTestVehicle(t, svc, 0)

	// Simulate the row disappearing beneath the service: the store reports
	// ErrVehicleNotFound and the service maps it to a not-found platform error.
	store.mu.Lock()
	store.vehicles = map[uuid.UUID]Vehicle{}
	store.mu.Unlock()

	_, err := svc.GetVehicle(context.Background(), v.ID)
	require.Error(t, err)
	requireStatus(t, err, http.StatusNotFound)
	assert.Equal(t, "not_found", platform.AsError(err).Code)
}
