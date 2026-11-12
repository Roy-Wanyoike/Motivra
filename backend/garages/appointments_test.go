package garages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakeStore appointment seam: mirrors the PostgresStore transactional
// --- semantics (scope predicate, status guard, same-transaction audit).

// appointmentVisible mirrors the PostgresStore scope predicate: an
// operator reads across tenants, everyone else reads their own tenant
// exactly, and a zero scope matches nothing (fail closed).
func appointmentVisible(a Appointment, scope Scope) bool {
	if scope.Operator {
		return true
	}
	if scope.TenantID == nil {
		return false
	}
	return a.TenantID == *scope.TenantID
}

// overlappingActive reports whether two ACTIVE appointments' one-hour
// slots intersect (the same predicate the SQL uses).
func overlappingActive(existing, incoming Appointment) bool {
	if !existing.active() {
		return false
	}
	start := incoming.ScheduledFor
	end := start.Add(appointmentSlotDuration)
	return existing.ScheduledFor.Before(end) && existing.ScheduledFor.Add(appointmentSlotDuration).After(start)
}

func (f *fakeStore) CreateAppointment(_ context.Context, a *Appointment, slot CreateSlot, audit AppointmentAuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAudit {
		return errors.New("audit_log unavailable")
	}
	overlapping := 0
	for _, other := range f.appointments {
		if other.TenantID != a.TenantID || !overlappingActive(other, *a) {
			continue
		}
		overlapping++
		if effectiveBay(other.Bay) == effectiveBay(slot.Bay) {
			return fmt.Errorf("garages: create appointment: %w", ErrSlotConflict)
		}
	}
	if slot.TechnicianSlots > 0 && overlapping >= slot.TechnicianSlots {
		return fmt.Errorf("garages: create appointment: %w (technician capacity)", ErrSlotConflict)
	}
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.CreatedAt, a.UpdatedAt = f.stamp(), f.stamp()
	f.appointments[a.ID] = *a
	audit.ObjectID = a.ID
	f.appointmentAudits = append(f.appointmentAudits, audit)
	return nil
}

func (f *fakeStore) GetAppointment(_ context.Context, id uuid.UUID, scope Scope) (Appointment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.appointments[id]
	if !ok || !appointmentVisible(a, scope) {
		return Appointment{}, fmt.Errorf("garages: get appointment %s: %w", id, ErrAppointmentNotFound)
	}
	return a, nil
}

func (f *fakeStore) ListAppointments(_ context.Context, tenantID uuid.UUID, filter AppointmentFilter, limit, offset int) ([]Appointment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Appointment
	for _, a := range f.appointments {
		if a.TenantID != tenantID {
			continue
		}
		if filter.Status != nil && a.Status != *filter.Status {
			continue
		}
		if filter.From != nil && a.ScheduledFor.Before(*filter.From) {
			continue
		}
		if filter.To != nil && !a.ScheduledFor.Before(*filter.To) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ScheduledFor.Equal(out[j].ScheduledFor) {
			return out[i].ScheduledFor.After(out[j].ScheduledFor)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) TransitionAppointment(_ context.Context, id uuid.UUID, scope Scope, allowedFrom []string, to string, audit AppointmentAuditRecord) (Appointment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.appointments[id]
	if !ok || !appointmentVisible(a, scope) {
		return Appointment{}, fmt.Errorf("garages: transition appointment %s: %w", id, ErrAppointmentNotFound)
	}
	allowed := false
	for _, from := range allowedFrom {
		if a.Status == from {
			allowed = true
			break
		}
	}
	if !allowed {
		return Appointment{}, fmt.Errorf("garages: transition appointment %s: %w (%s → %s)", id, ErrInvalidTransition, a.Status, to)
	}
	if f.failAudit {
		return Appointment{}, errors.New("audit_log unavailable")
	}
	audit.ObjectID = a.ID
	audit.TenantID = a.TenantID
	audit.From = a.Status
	audit.To = to
	a.Status = to
	a.UpdatedAt = f.stamp()
	f.appointments[id] = a
	f.appointmentAudits = append(f.appointmentAudits, audit)
	return a, nil
}

// --- helpers -------------------------------------------------------------

// seedCapacity writes the tenant's declared capacity directly into the
// fake store (the #170 model the appointment slot validation reads).
func seedCapacity(t *testing.T, store *fakeStore, tenant uuid.UUID, bays, technicianSlots int) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.capacity[tenant] = Capacity{
		TenantID:        tenant,
		Bays:            bays,
		OpeningTime:     DefaultOpeningTime,
		ClosingTime:     DefaultClosingTime,
		TechnicianSlots: technicianSlots,
		CreatedAt:       store.stamp(),
		UpdatedAt:       store.stamp(),
	}
}

// futureSlot returns a slot start two days out at 10:00 UTC — inside the
// conventional 08:00–18:00 operating day and never in the past.
func futureSlot() time.Time {
	day := time.Now().UTC().Add(48 * time.Hour)
	return time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC)
}

// appointmentBody builds a create body for the given slot time.
func appointmentBody(jobID string, at time.Time) []byte {
	return []byte(fmt.Sprintf(`{"job_id":%q,"scheduled_for":%q}`, jobID, at.Format(time.RFC3339)))
}

// createAppointment POSTs an appointment as token and requires 201.
func createAppointment(t *testing.T, h http.Handler, token string, garage uuid.UUID, body []byte) map[string]any {
	t.Helper()
	w := do(t, h, http.MethodPost, "/v1/garages/"+garage.String()+"/appointments", token, body)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	return decodeBody(t, w)
}

// transition POSTs one lifecycle action and returns the recorder.
func transition(t *testing.T, h http.Handler, token, id, action string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, http.MethodPost, "/v1/appointments/"+id+"/"+action, token, nil)
}

// decodeList decodes a JSON array response.
func decodeList(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var list []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list), "body: %s", w.Body.String())
	return list
}

func TestAppointmentLifecycleHappyPath(t *testing.T) {
	t.Parallel()
	h, store, pub := newTestAPI(t)
	tenant := uuid.New()
	seedCapacity(t, store, tenant, 2, 2)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())

	// CUSTOMER opens a request against the garage.
	created := createAppointment(t, h, customer, tenant, appointmentBody(uuid.New().String(), futureSlot()))
	id, ok := created["id"].(string)
	require.True(t, ok)
	assert.Equal(t, AppointmentRequested, created["status"])
	assert.Equal(t, tenant.String(), created["garage_tenant_id"])
	require.Len(t, store.appointmentAudits, 1, "creation appends one audit row")
	assert.Equal(t, ActionAppointmentCreated, store.appointmentAudits[0].Action)
	assert.Equal(t, "", store.appointmentAudits[0].From)
	assert.Equal(t, AppointmentRequested, store.appointmentAudits[0].To)
	assert.False(t, store.appointmentAudits[0].Override)
	require.Len(t, pub.ofType(t, EventAppointmentCreated), 1)

	// GARAGE staff confirm, then complete.
	w := transition(t, h, garage, id, "confirm")
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, AppointmentConfirmed, decodeBody(t, w)["status"])
	w = transition(t, h, garage, id, "complete")
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, AppointmentCompleted, decodeBody(t, w)["status"])

	// The audit trail holds one append-only row per lifecycle change, in
	// order; garage moves are not overrides.
	require.Len(t, store.appointmentAudits, 3)
	assert.Equal(t, ActionAppointmentConfirmed, store.appointmentAudits[1].Action)
	assert.Equal(t, AppointmentRequested, store.appointmentAudits[1].From)
	assert.Equal(t, AppointmentConfirmed, store.appointmentAudits[1].To)
	assert.False(t, store.appointmentAudits[1].Override)
	assert.Equal(t, ActionAppointmentCompleted, store.appointmentAudits[2].Action)
	assert.Equal(t, AppointmentConfirmed, store.appointmentAudits[2].From)
	assert.False(t, store.appointmentAudits[2].Override)
	require.Len(t, pub.ofType(t, EventAppointmentStatusChanged), 2)

	// GET returns the stored record.
	w = do(t, h, http.MethodGet, "/v1/appointments/"+id, garage, nil)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, AppointmentCompleted, decodeBody(t, w)["status"])

	// Terminal states are immutable: every move is refused 409.
	for _, action := range []string{"confirm", "complete", "cancel", "no-show"} {
		w := transition(t, h, garage, id, action)
		assert.Equal(t, http.StatusConflict, w.Code, "action %s on terminal state", action)
	}
	assert.Len(t, store.appointmentAudits, 3, "refused transitions append nothing")
}

func TestAppointmentInvalidTransitions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		steps  []string // actions already applied after creation
		action string   // the refused move
	}{
		{"complete_from_requested", nil, "complete"},
		{"no_show_from_requested", nil, "no-show"},
		{"confirm_from_confirmed", []string{"confirm"}, "confirm"},
		{"complete_from_completed", []string{"confirm", "complete"}, "confirm"},
		{"cancel_from_cancelled", []string{"cancel"}, "cancel"},
		{"complete_from_cancelled", []string{"cancel"}, "complete"},
		{"no_show_from_cancelled", []string{"cancel"}, "no-show"},
		{"confirm_from_no_show", []string{"confirm", "no-show"}, "confirm"},
		{"cancel_from_completed", []string{"confirm", "complete"}, "cancel"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, store, _ := newTestAPI(t)
			tenant := uuid.New()
			seedCapacity(t, store, tenant, 1, 1)
			customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
			garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())
			created := createAppointment(t, h, customer, tenant, appointmentBody(uuid.New().String(), futureSlot()))
			id := created["id"].(string)
			for _, step := range tc.steps {
				w := transition(t, h, garage, id, step)
				require.Equal(t, http.StatusOK, w.Code, "step %s: %s", step, w.Body.String())
			}
			auditsBefore := len(store.appointmentAudits)

			// Baseline = status after the applied steps (not at creation:
			// stepped cases have legitimately advanced already).
			wb := do(t, h, http.MethodGet, "/v1/appointments/"+id, garage, nil)
			require.Equal(t, http.StatusOK, wb.Code)
			baseline := decodeBody(t, wb)["status"]

			w := transition(t, h, garage, id, tc.action)
			require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
			body := decodeBody(t, w)
			assert.Equal(t, "conflict", body["code"], "problem+json carries the conflict code")

			// Nothing moved, nothing was appended.
			w = do(t, h, http.MethodGet, "/v1/appointments/"+id, garage, nil)
			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, baseline, decodeBody(t, w)["status"], "status unchanged")
			assert.Len(t, store.appointmentAudits, auditsBefore, "refused transition appends no audit row")
		})
	}
}

func TestAppointmentRBACMatrix(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	other := uuid.New()
	// Wide technician capacity: every case below books its own labelled bay
	// in the same window; saturation is a separate test's concern.
	seedCapacity(t, store, tenant, 8, 64)
	seedCapacity(t, store, other, 8, 64)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())
	garageOther := mintGarageToken(t, uuid.New().String(), RoleGarage, other.String())
	admin := mintGarageToken(t, uuid.New().String(), RoleAdmin, "")
	superAdmin := mintGarageToken(t, uuid.New().String(), RoleSuperAdmin, "")
	dispatcher := mintGarageToken(t, uuid.New().String(), RoleDispatcher, "")
	bodyWithBay := func(bay string) []byte {
		return []byte(fmt.Sprintf(`{"job_id":%q,"scheduled_for":%q,"bay":%q}`, uuid.New().String(), futureSlot().Format(time.RFC3339), bay))
	}

	// One stable appointment for the read matrix.
	created := createAppointment(t, h, customer, tenant, bodyWithBay("base"))
	id := created["id"].(string)

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			token string
			want  int
		}{
			{"customer_against_garage", customer, http.StatusCreated},
			{"garage_own_tenant", garage, http.StatusCreated},
			{"garage_other_tenant", garageOther, http.StatusForbidden},
			{"dispatcher", dispatcher, http.StatusForbidden},
			{"admin", admin, http.StatusForbidden},
			{"super_admin", superAdmin, http.StatusForbidden},
			{"unauthenticated", "", http.StatusUnauthorized},
		}
		for i, tc := range cases {
			w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", tc.token, bodyWithBay(fmt.Sprintf("create-%d", i)))
			assert.Equal(t, tc.want, w.Code, "%s: %s", tc.name, w.Body.String())
		}
	})

	t.Run("transition", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			token string
			want  int
		}{
			{"garage_own_tenant", garage, http.StatusOK},
			{"garage_other_tenant", garageOther, http.StatusNotFound},
			{"admin_override", admin, http.StatusOK},
			{"super_admin_override", superAdmin, http.StatusOK},
			{"dispatcher", dispatcher, http.StatusForbidden},
			{"customer", customer, http.StatusForbidden},
			{"unauthenticated", "", http.StatusUnauthorized},
		}
		for i, tc := range cases {
			// A fresh requested appointment per case: every expectation is
			// evaluated against the same starting state.
			fresh := createAppointment(t, h, customer, tenant, bodyWithBay(fmt.Sprintf("transition-%d", i)))
			w := transition(t, h, tc.token, fresh["id"].(string), "confirm")
			assert.Equal(t, tc.want, w.Code, "%s: %s", tc.name, w.Body.String())
		}
	})

	t.Run("read_single", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			token string
			want  int
		}{
			{"garage_own_tenant", garage, http.StatusOK},
			{"garage_other_tenant", garageOther, http.StatusNotFound},
			{"admin", admin, http.StatusOK},
			{"dispatcher", dispatcher, http.StatusOK},
			{"customer", customer, http.StatusForbidden},
			{"unauthenticated", "", http.StatusUnauthorized},
		}
		for _, tc := range cases {
			w := do(t, h, http.MethodGet, "/v1/appointments/"+id, tc.token, nil)
			assert.Equal(t, tc.want, w.Code, "%s: %s", tc.name, w.Body.String())
		}
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			token string
			want  int
		}{
			{"garage_own_tenant", garage, http.StatusOK},
			{"garage_other_tenant_path_mismatch", garageOther, http.StatusForbidden},
			{"admin", admin, http.StatusOK},
			{"dispatcher", dispatcher, http.StatusOK},
			{"customer", customer, http.StatusForbidden},
			{"unauthenticated", "", http.StatusUnauthorized},
		}
		for _, tc := range cases {
			w := do(t, h, http.MethodGet, "/v1/garages/"+tenant.String()+"/appointments", tc.token, nil)
			assert.Equal(t, tc.want, w.Code, "%s: %s", tc.name, w.Body.String())
		}
	})
}

func TestAppointmentAdminOverrideAudited(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	seedCapacity(t, store, tenant, 1, 1)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	admin := mintGarageToken(t, uuid.New().String(), RoleAdmin, "")
	created := createAppointment(t, h, customer, tenant, appointmentBody(uuid.New().String(), futureSlot()))
	id := created["id"].(string)

	w := transition(t, h, admin, id, "confirm")
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, store.appointmentAudits, 2)
	override := store.appointmentAudits[1]
	assert.Equal(t, ActionAppointmentConfirmed, override.Action)
	assert.True(t, override.Override, "cross-tenant intervention is stamped as an override")
	assert.Equal(t, tenant, override.TenantID, "the audit row names the owning tenant")
}

func TestAppointmentCrossTenantIsolation(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	seedCapacity(t, store, tenantA, 2, 2)
	seedCapacity(t, store, tenantB, 2, 2)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	garageA := mintGarageToken(t, uuid.New().String(), RoleGarage, tenantA.String())
	garageB := mintGarageToken(t, uuid.New().String(), RoleGarage, tenantB.String())
	admin := mintGarageToken(t, uuid.New().String(), RoleAdmin, "")

	created := createAppointment(t, h, customer, tenantA, appointmentBody(uuid.New().String(), futureSlot()))
	id := created["id"].(string)

	// Garage B never sees garage A's appointments.
	w := do(t, h, http.MethodGet, "/v1/appointments/"+id, garageB, nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "a foreign tenant reads as missing, not forbidden")
	w = transition(t, h, garageB, id, "confirm")
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = do(t, h, http.MethodGet, "/v1/garages/"+tenantB.String()+"/appointments", garageB, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, decodeList(t, w), "garage B's own calendar is empty")

	// Operators read across tenants (ADR-0004) but only the addressed one.
	w = do(t, h, http.MethodGet, "/v1/appointments/"+id, admin, nil)
	require.Equal(t, http.StatusOK, w.Code)
	w = do(t, h, http.MethodGet, "/v1/garages/"+tenantA.String()+"/appointments", admin, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeList(t, w), 1)

	// And garage A sees its own single appointment.
	w = do(t, h, http.MethodGet, "/v1/garages/"+tenantA.String()+"/appointments", garageA, nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, decodeList(t, w), 1)
}

func TestAppointmentDoubleBookingRejected(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	// Four declared technician slots: the bay-dimension cases below stack
	// parallel bookings in the same window without saturating technicians;
	// the saturation dimension gets its own tenant.
	seedCapacity(t, store, tenant, 2, 4)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	slot := futureSlot()
	bay1 := "bay-1"
	bay2 := "bay-2"

	bodyWith := func(bay *string, at time.Time) []byte {
		b := fmt.Sprintf(`{"job_id":%q,"scheduled_for":%q`, uuid.New().String(), at.Format(time.RFC3339))
		if bay != nil {
			b += fmt.Sprintf(`,"bay":%q`, *bay)
		}
		return []byte(b + "}")
	}

	created := createAppointment(t, h, customer, tenant, bodyWith(&bay1, slot))
	id := created["id"].(string)

	t.Run("same_bay_overlapping", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(&bay1, slot.Add(30*time.Minute)))
		require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
		assert.Equal(t, "conflict", decodeBody(t, w)["code"])
	})
	t.Run("same_bay_adjacent_ok", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(&bay1, slot.Add(time.Hour)))
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	})
	t.Run("different_bay_same_time_ok", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(&bay2, slot))
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	})
	t.Run("general_pool_conflicts", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(nil, slot))
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		w = do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(nil, slot.Add(30*time.Minute)))
		assert.Equal(t, http.StatusConflict, w.Code, "two unbayed bookings in the same slot conflict")
	})
	t.Run("terminal_releases_slot", func(t *testing.T) {
		t.Parallel()
		garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())
		w := transition(t, h, garage, id, "cancel")
		require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		w = do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyWith(&bay1, slot))
		assert.Equal(t, http.StatusCreated, w.Code, "a cancelled appointment no longer holds the slot: %s", w.Body.String())
	})
	t.Run("technician_capacity_saturation", func(t *testing.T) {
		t.Parallel()
		tenant2 := uuid.New()
		seedCapacity(t, store, tenant2, 2, 1)
		cust2 := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", cust2, bodyWith(&bay1, slot))
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		w = do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", cust2, bodyWith(&bay2, slot))
		assert.Equal(t, http.StatusConflict, w.Code, "one declared technician slot cannot take two parallel bookings")
	})
}

func TestAppointmentSlotValidation(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	seedCapacity(t, store, tenant, 2, 2)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	bodyAt := func(at time.Time) []byte { return appointmentBody(uuid.New().String(), at) }

	t.Run("capacity_not_declared", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+uuid.New().String()+"/appointments", customer, bodyAt(futureSlot()))
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, "body: %s", w.Body.String())
	})
	t.Run("zero_bays", func(t *testing.T) {
		t.Parallel()
		tenant2 := uuid.New()
		seedCapacity(t, store, tenant2, 0, 1)
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", customer, bodyAt(futureSlot()))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})
	t.Run("zero_technician_slots", func(t *testing.T) {
		t.Parallel()
		tenant2 := uuid.New()
		seedCapacity(t, store, tenant2, 1, 0)
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", customer, bodyAt(futureSlot()))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})
	t.Run("past_slot", func(t *testing.T) {
		t.Parallel()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyAt(time.Now().Add(-time.Hour)))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	})
	t.Run("outside_operating_hours", func(t *testing.T) {
		t.Parallel()
		day := futureSlot()
		early := time.Date(day.Year(), day.Month(), day.Day(), 7, 30, 0, 0, time.UTC)
		late := time.Date(day.Year(), day.Month(), day.Day(), 17, 30, 0, 0, time.UTC) // slot would end 18:30
		for _, at := range []time.Time{early, late} {
			w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyAt(at))
			assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "slot %s must be refused", at)
		}
	})
	t.Run("operating_hours_boundaries_ok", func(t *testing.T) {
		t.Parallel()
		day := futureSlot()
		opening := time.Date(day.Year(), day.Month(), day.Day(), 8, 0, 0, 0, time.UTC)
		lastSlot := time.Date(day.Year(), day.Month(), day.Day(), 17, 0, 0, 0, time.UTC) // ends exactly at closing
		for _, at := range []time.Time{opening, lastSlot} {
			w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, bodyAt(at))
			assert.Equal(t, http.StatusCreated, w.Code, "slot %s inside operating hours: %s", at, w.Body.String())
		}
	})
	t.Run("declared_hours_respected", func(t *testing.T) {
		t.Parallel()
		tenant2 := uuid.New()
		store.mu.Lock()
		store.capacity[tenant2] = Capacity{TenantID: tenant2, Bays: 1, OpeningTime: "09:00", ClosingTime: "12:00", TechnicianSlots: 1}
		store.mu.Unlock()
		day := futureSlot()
		w := do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", customer, bodyAt(time.Date(day.Year(), day.Month(), day.Day(), 8, 0, 0, 0, time.UTC)))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "outside the declared 09:00–12:00 window")
		w = do(t, h, http.MethodPost, "/v1/garages/"+tenant2.String()+"/appointments", customer, bodyAt(time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC)))
		assert.Equal(t, http.StatusCreated, w.Code, "inside the declared window: %s", w.Body.String())
	})
	t.Run("malformed_input", func(t *testing.T) {
		t.Parallel()
		path := "/v1/garages/" + tenant.String() + "/appointments"
		w := do(t, h, http.MethodPost, path, customer, []byte(`{"scheduled_for":"`+futureSlot().Format(time.RFC3339)+`"}`))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "missing job_id")
		w = do(t, h, http.MethodPost, path, customer, []byte(`{"job_id":"`+uuid.New().String()+`"}`))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "missing scheduled_for")
		w = do(t, h, http.MethodPost, path, customer, []byte(`{"job_id":"`+uuid.New().String()+`","scheduled_for":"not-a-time"}`))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "malformed RFC 3339")
		w = do(t, h, http.MethodPost, "/v1/garages/not-a-uuid/appointments", customer, bodyAt(futureSlot()))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "malformed garage id")
	})
}

func TestAppointmentListFilters(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	seedCapacity(t, store, tenant, 3, 3)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())

	slot := futureSlot()
	var ids []string
	for i, hour := range []int{9, 10, 11} {
		at := time.Date(slot.Year(), slot.Month(), slot.Day(), hour, 0, 0, 0, time.UTC)
		created := createAppointment(t, h, customer, tenant, appointmentBody(uuid.New().String(), at))
		ids = append(ids, created["id"].(string))
		_ = i
	}
	// Confirm the middle one; complete the last.
	require.Equal(t, http.StatusOK, transition(t, h, garage, ids[1], "confirm").Code)
	require.Equal(t, http.StatusOK, transition(t, h, garage, ids[2], "confirm").Code)
	require.Equal(t, http.StatusOK, transition(t, h, garage, ids[2], "complete").Code)

	listPath := "/v1/garages/" + tenant.String() + "/appointments"
	t.Run("all_newest_first", func(t *testing.T) {
		w := do(t, h, http.MethodGet, listPath, garage, nil)
		require.Equal(t, http.StatusOK, w.Code)
		list := decodeList(t, w)
		require.Len(t, list, 3)
		assert.Equal(t, ids[2], list[0]["id"], "ordered by scheduled_for, newest first")
		assert.Equal(t, ids[0], list[2]["id"])
	})
	t.Run("status_filter", func(t *testing.T) {
		w := do(t, h, http.MethodGet, listPath+"?status=confirmed", garage, nil)
		require.Equal(t, http.StatusOK, w.Code)
		list := decodeList(t, w)
		require.Len(t, list, 1)
		assert.Equal(t, ids[1], list[0]["id"])
		assert.Equal(t, AppointmentConfirmed, list[0]["status"])
	})
	t.Run("window_filter", func(t *testing.T) {
		from := time.Date(slot.Year(), slot.Month(), slot.Day(), 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
		to := time.Date(slot.Year(), slot.Month(), slot.Day(), 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		w := do(t, h, http.MethodGet, listPath+"?from="+from+"&to="+to, garage, nil)
		require.Equal(t, http.StatusOK, w.Code)
		list := decodeList(t, w)
		require.Len(t, list, 2, "the 10:00 and 11:00 slots are inside the window")
	})
	t.Run("paging", func(t *testing.T) {
		w := do(t, h, http.MethodGet, listPath+"?limit=2&offset=1", garage, nil)
		require.Equal(t, http.StatusOK, w.Code)
		list := decodeList(t, w)
		require.Len(t, list, 2)
		assert.Equal(t, ids[1], list[0]["id"])
		assert.Equal(t, ids[0], list[1]["id"])
	})
	t.Run("bad_params", func(t *testing.T) {
		for _, q := range []string{"?status=nope", "?limit=0", "?limit=1000", "?offset=-1", "?from=yesterday"} {
			w := do(t, h, http.MethodGet, listPath+q, garage, nil)
			assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "%s: %s", q, w.Body.String())
		}
	})
}

func TestAppointmentAuditFailureFailsRequest(t *testing.T) {
	t.Parallel()
	h, store, _ := newTestAPI(t)
	tenant := uuid.New()
	seedCapacity(t, store, tenant, 1, 1)
	customer := mintGarageToken(t, uuid.New().String(), RoleCustomer, "")
	garage := mintGarageToken(t, uuid.New().String(), RoleGarage, tenant.String())
	body := appointmentBody(uuid.New().String(), futureSlot())

	store.failAudit = true
	w := do(t, h, http.MethodPost, "/v1/garages/"+tenant.String()+"/appointments", customer, body)
	require.Equal(t, http.StatusInternalServerError, w.Code, "an audit failure must fail the request")
	assert.Equal(t, "internal_error", decodeBody(t, w)["code"])
	assert.Empty(t, store.appointments, "the write rolled back with the audit")

	store.failAudit = false
	created := createAppointment(t, h, customer, tenant, body)
	id := created["id"].(string)

	store.failAudit = true
	w = transition(t, h, garage, id, "confirm")
	require.Equal(t, http.StatusInternalServerError, w.Code, "an audit failure must fail the transition")
	got, err := store.GetAppointment(t.Context(), uuid.MustParse(id), Scope{TenantID: &tenant})
	require.NoError(t, err)
	assert.Equal(t, AppointmentRequested, got.Status, "the transition rolled back with the audit")
	assert.Len(t, store.appointmentAudits, 1, "only the creation audit exists")
}
