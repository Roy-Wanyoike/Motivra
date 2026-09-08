package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// fakeStore is an in-memory Store mirroring the PostgresStore transactional
// semantics closely enough to exercise the service discipline.
type fakeStore struct {
	mu          sync.Mutex
	requests    map[uuid.UUID]ServiceRequest
	jobs        map[uuid.UUID]Job
	assignments map[uuid.UUID]JobAssignment
	transitions []JobTransition
	now         time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		requests:    make(map[uuid.UUID]ServiceRequest),
		jobs:        make(map[uuid.UUID]Job),
		assignments: make(map[uuid.UUID]JobAssignment),
		now:         time.Now().UTC(),
	}
}

func (f *fakeStore) stamp() time.Time {
	f.now = f.now.Add(time.Millisecond)
	return f.now
}

func (f *fakeStore) CreateRequest(_ context.Context, r *ServiceRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	r.CreatedAt, r.UpdatedAt = f.stamp(), f.stamp()
	f.requests[r.ID] = *r
	return nil
}

func (f *fakeStore) GetRequest(_ context.Context, id uuid.UUID) (ServiceRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.requests[id]
	if !ok {
		return ServiceRequest{}, fmt.Errorf("jobs: get service request %s: %w", id, ErrRequestNotFound)
	}
	return r, nil
}

func (f *fakeStore) CreateJob(_ context.Context, j *Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j.ServiceRequestID != nil {
		r, ok := f.requests[*j.ServiceRequestID]
		if !ok || r.Status != RequestStatusReceived {
			return fmt.Errorf("jobs: create job: service request not claimable: %w", ErrAlreadyConverted)
		}
		r.Status = RequestStatusConverted
		f.requests[r.ID] = r
	}
	if j.ID == uuid.Nil {
		j.ID = uuid.New()
	}
	if j.Status == "" {
		j.Status = StatusCreated
	}
	j.CreatedAt, j.UpdatedAt = f.stamp(), f.stamp()
	f.jobs[j.ID] = *j
	return nil
}

func (f *fakeStore) GetJob(_ context.Context, id uuid.UUID) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("jobs: get job %s: %w", id, ErrJobNotFound)
	}
	return j, nil
}

func (f *fakeStore) UpdateJobStatus(_ context.Context, jobID uuid.UUID, newStatus Status, actorID *uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return fmt.Errorf("jobs: update job status %s: %w", jobID, ErrJobNotFound)
	}
	f.transitions = append(f.transitions, JobTransition{
		ID: int64(len(f.transitions) + 1), JobID: jobID, FromStatus: j.Status, ToStatus: newStatus,
		ActorID: actorID, Reason: reason, CreatedAt: f.stamp(),
	})
	j.Status = newStatus
	f.jobs[jobID] = j
	return nil
}

func (f *fakeStore) AssignTechnician(_ context.Context, jobID uuid.UUID, technicianID uuid.UUID, assignedBy *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return fmt.Errorf("jobs: assign technician to job %s: %w", jobID, ErrJobNotFound)
	}
	for id, a := range f.assignments {
		if a.JobID == jobID && a.Status == AssignmentStatusAssigned {
			a.Status = AssignmentStatusReassigned
			f.assignments[id] = a
		}
	}
	f.assignments[uuid.New()] = JobAssignment{
		JobID: jobID, TechnicianID: technicianID, AssignedBy: assignedBy,
		Status: AssignmentStatusAssigned, CreatedAt: f.stamp(),
	}
	f.transitions = append(f.transitions, JobTransition{
		ID: int64(len(f.transitions) + 1), JobID: jobID, FromStatus: j.Status, ToStatus: StatusAssigned,
		ActorID: assignedBy, Reason: "technician assigned", CreatedAt: f.stamp(),
	})
	j.Status = StatusAssigned
	j.TechnicianID = &technicianID
	f.jobs[jobID] = j
	return nil
}

func (f *fakeStore) AcceptAssignment(_ context.Context, jobID uuid.UUID, technicianID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[jobID]
	if !ok {
		return fmt.Errorf("jobs: accept assignment for job %s: %w", jobID, ErrJobNotFound)
	}
	found := false
	for id, a := range f.assignments {
		if a.JobID == jobID && a.TechnicianID == technicianID && a.Status == AssignmentStatusAssigned {
			a.Status = AssignmentStatusAccepted
			now := f.stamp()
			a.AcceptedAt = &now
			f.assignments[id] = a
			found = true
		}
	}
	if !found {
		return fmt.Errorf("jobs: accept assignment: no open assignment of job %s to technician %s", jobID, technicianID)
	}
	f.transitions = append(f.transitions, JobTransition{
		ID: int64(len(f.transitions) + 1), JobID: jobID, FromStatus: j.Status, ToStatus: StatusAccepted,
		ActorID: &technicianID, Reason: "technician accepted assignment", CreatedAt: f.stamp(),
	})
	j.Status = StatusAccepted
	f.jobs[jobID] = j
	return nil
}

func (f *fakeStore) ListJobsByStatus(_ context.Context, status Status, limit, offset int) ([]Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Job
	for _, j := range f.jobs {
		if j.Status == status {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) ListTransitions(_ context.Context, jobID uuid.UUID) ([]JobTransition, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []JobTransition
	for _, t := range f.transitions {
		if t.JobID == jobID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out, nil
}

// capturePublisher records every published event and can be told to fail.
type capturePublisher struct {
	mu     sync.Mutex
	events []platform.Event
	fail   bool
}

func (p *capturePublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("jetstream unavailable")
	}
	e.CorrelationID = domain // record the domain the service chose
	p.events = append(p.events, e)
	return nil
}

func (p *capturePublisher) ofType(t *testing.T, eventType string) []platform.Event {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []platform.Event
	for _, e := range p.events {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

// newFixture wires a Service to a fresh fake store and capturing publisher.
func newFixture() (*Service, *fakeStore, *capturePublisher) {
	store := newFakeStore()
	pub := &capturePublisher{}
	return NewService(store, pub), store, pub
}

func sampleRequest() *ServiceRequest {
	return &ServiceRequest{
		CustomerID:  uuid.New(),
		Description: "Engine overheats on the highway, coolant smell in cabin",
	}
}

// driveDispatching moves a fresh job CREATED -> TRIAGING -> DISPATCHING.
func driveDispatching(t *testing.T, svc *Service, jobID uuid.UUID) {
	t.Helper()
	require.NoError(t, svc.Transition(context.Background(), jobID, StatusTriaging, nil, "triaged"))
	require.NoError(t, svc.Transition(context.Background(), jobID, StatusDispatching, nil, "dispatching"))
}

func TestCreateRequestPublishesReceivedEvent(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	r := sampleRequest()
	require.NoError(t, svc.CreateRequest(ctx, r))

	assert.NotEqual(t, uuid.Nil, r.ID, "request ID must be assigned")
	assert.Equal(t, RequestStatusReceived, r.Status)
	assert.NotZero(t, r.CreatedAt)

	stored, err := store.GetRequest(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, RequestStatusReceived, stored.Status)

	events := pub.ofType(t, EventRequestReceived)
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "jobs", e.CorrelationID, "publish domain must be jobs")
	assert.Equal(t, r.ID.String(), e.AggregateID)
	var payload RequestReceivedPayload
	require.NoError(t, json.Unmarshal(e.Payload, &payload))
	assert.Equal(t, r.ID, payload.RequestID)
	assert.Equal(t, r.CustomerID, payload.CustomerID)
}

func TestCreateRequestValidationRejectsAndPublishesNothing(t *testing.T) {
	cases := map[string]*ServiceRequest{
		"missing customer":  {Description: "x"},
		"blank description": {CustomerID: uuid.New(), Description: "   "},
		"lat without lng":   {CustomerID: uuid.New(), Description: "x", LocationLat: floatPtr(1.0)},
		"lat out of bounds": {CustomerID: uuid.New(), Description: "x", LocationLat: floatPtr(91.0), LocationLng: floatPtr(0.0)},
		"lng out of bounds": {CustomerID: uuid.New(), Description: "x", LocationLat: floatPtr(0.0), LocationLng: floatPtr(-181.0)},
		"nan latitude":      {CustomerID: uuid.New(), Description: "x", LocationLat: floatPtr(1.0), LocationLng: nanPtr()},
	}
	for name, r := range cases {
		svc, _, pub := newFixture()
		err := svc.CreateRequest(context.Background(), r)
		require.Error(t, err, name)
		assert.Zero(t, pub.count(), name)
	}
}

func TestCreateJobFromRequestHappyPath(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	r := sampleRequest()
	require.NoError(t, svc.CreateRequest(ctx, r))

	job, err := svc.CreateJobFromRequest(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, job.Status)
	require.NotNil(t, job.ServiceRequestID)
	assert.Equal(t, r.ID, *job.ServiceRequestID)
	assert.Equal(t, r.CustomerID, job.CustomerID)
	assert.Equal(t, r.Description, job.ProblemSummary)

	stored, err := store.GetRequest(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, RequestStatusConverted, stored.Status, "request must be marked converted")

	events := pub.ofType(t, EventJobCreated)
	require.Len(t, events, 1)
	var payload JobCreatedPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	assert.Equal(t, job.ID, payload.JobID)
	require.NotNil(t, payload.ServiceRequestID)
	assert.Equal(t, r.ID, *payload.ServiceRequestID)
	assert.Equal(t, r.Description, payload.ProblemSummary)
}

func TestCreateJobFromRequestDoubleConversionConflicts(t *testing.T) {
	svc, _, pub := newFixture()
	ctx := context.Background()

	r := sampleRequest()
	require.NoError(t, svc.CreateRequest(ctx, r))
	_, err := svc.CreateJobFromRequest(ctx, r.ID)
	require.NoError(t, err)

	_, err = svc.CreateJobFromRequest(ctx, r.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAlreadyConverted), "want ErrAlreadyConverted, got %v", err)
	assert.Len(t, pub.ofType(t, EventJobCreated), 1, "second conversion must not publish")
}

func TestTransitionPublishesStatusChanged(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	actor := uuid.New()

	require.NoError(t, svc.Transition(ctx, job.ID, StatusTriaging, &actor, "customer request triaged"))

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusTriaging, stored.Status)

	events := pub.ofType(t, EventJobStatusChanged)
	require.Len(t, events, 1)
	var payload JobStatusChangedPayload
	require.NoError(t, json.Unmarshal(events[0].Payload, &payload))
	assert.Equal(t, job.ID, payload.JobID)
	assert.Equal(t, StatusCreated, payload.From)
	assert.Equal(t, StatusTriaging, payload.To)
	assert.Equal(t, "customer request triaged", payload.Reason)
	require.NotNil(t, payload.ActorID)
	assert.Equal(t, actor, *payload.ActorID)

	transitions, err := store.ListTransitions(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 1)
	assert.Equal(t, StatusCreated, transitions[0].FromStatus)
	assert.Equal(t, StatusTriaging, transitions[0].ToStatus)
}

func TestIllegalTransitionRejectedWithoutPublishing(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)

	err := svc.Transition(ctx, job.ID, StatusRepairing, nil, "shortcut")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "want ErrConflict, got %v", err)
	assert.Zero(t, pub.count(), "illegal transition must publish nothing")

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCreated, stored.Status, "illegal transition must not mutate")

	transitions, err := store.ListTransitions(ctx, job.ID)
	require.NoError(t, err)
	assert.Empty(t, transitions, "illegal transition must leave no audit row")
}

func TestAssignmentFlowPublishesEvents(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)

	tech := uuid.New()
	dispatcher := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech, &dispatcher))

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAssigned, stored.Status)
	require.NotNil(t, stored.TechnicianID)
	assert.Equal(t, tech, *stored.TechnicianID)

	assigned := pub.ofType(t, EventJobAssigned)
	require.Len(t, assigned, 1)
	var payload JobAssignedPayload
	require.NoError(t, json.Unmarshal(assigned[0].Payload, &payload))
	assert.Equal(t, job.ID, payload.JobID)
	assert.Equal(t, tech, payload.TechnicianID)
	require.NotNil(t, payload.AssignedBy)
	assert.Equal(t, dispatcher, *payload.AssignedBy)

	// Acceptance: ASSIGNED -> ACCEPTED with a status.changed event.
	require.NoError(t, svc.Accept(ctx, job.ID, tech))

	stored, err = store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAccepted, stored.Status)

	// driveDispatching already emitted two status.changed events; find the
	// acceptance one.
	var changePayload JobStatusChangedPayload
	found := false
	for _, e := range pub.ofType(t, EventJobStatusChanged) {
		var p JobStatusChangedPayload
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		if p.From == StatusAssigned && p.To == StatusAccepted {
			changePayload = p
			found = true
		}
	}
	require.True(t, found, "assignment acceptance must publish status.changed")
	assert.Equal(t, job.ID, changePayload.JobID)
	assert.Equal(t, "technician accepted assignment", changePayload.Reason)

	transitions, err := store.ListTransitions(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 4) // TRIAGING, DISPATCHING, ASSIGNED, ACCEPTED
	assert.Equal(t, StatusCreated, transitions[0].FromStatus)
	assert.Equal(t, StatusTriaging, transitions[0].ToStatus)
	assert.Equal(t, StatusDispatching, transitions[2].FromStatus)
	assert.Equal(t, StatusAssigned, transitions[2].ToStatus)
	last := transitions[len(transitions)-1]
	assert.Equal(t, StatusAssigned, last.FromStatus)
	assert.Equal(t, StatusAccepted, last.ToStatus)
}

func TestReassignmentGoesThroughDispatching(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)

	tech1, tech2, dispatcher := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech1, &dispatcher))
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech2, &dispatcher))

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAssigned, stored.Status)
	require.NotNil(t, stored.TechnicianID)
	assert.Equal(t, tech2, *stored.TechnicianID, "job must carry the new technician")

	// The reassignment emitted ASSIGNED -> DISPATCHING (status.changed);
	// the assignment itself is announced by job.assigned.v1.
	var dispatchingBack []JobStatusChangedPayload
	for _, e := range pub.ofType(t, EventJobStatusChanged) {
		var p JobStatusChangedPayload
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		if p.From == StatusAssigned && p.To == StatusDispatching {
			dispatchingBack = append(dispatchingBack, p)
		}
	}
	require.Len(t, dispatchingBack, 1, "reassignment must step back through DISPATCHING")

	assigned := pub.ofType(t, EventJobAssigned)
	require.Len(t, assigned, 2)
	var lastAssigned JobAssignedPayload
	require.NoError(t, json.Unmarshal(assigned[1].Payload, &lastAssigned))
	assert.Equal(t, tech2, lastAssigned.TechnicianID)

	transitions, err := store.ListTransitions(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 5) // TRIAGING, DISPATCHING, ASSIGNED, ASSIGNED->DISPATCHING, DISPATCHING->ASSIGNED
	assert.Equal(t, StatusAssigned, transitions[3].FromStatus)
	assert.Equal(t, StatusDispatching, transitions[3].ToStatus)
	assert.Equal(t, StatusDispatching, transitions[4].FromStatus)
	assert.Equal(t, StatusAssigned, transitions[4].ToStatus)

	// The first assignment must have been retired.
	open := 0
	for _, a := range store.assignments {
		if a.JobID == job.ID && a.Status == AssignmentStatusAssigned {
			open++
		}
	}
	assert.Equal(t, 1, open, "only the latest assignment stays open")
}

func TestReassignSameTechnicianConflicts(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)

	tech := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech, nil))

	before := pub.count()
	err := svc.AssignTechnician(ctx, job.ID, tech, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "want ErrConflict, got %v", err)
	assert.Equal(t, before, pub.count(), "conflict must publish nothing")

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAssigned, stored.Status)
}

func TestAcceptRejectsWrongTechnician(t *testing.T) {
	svc, store, pub := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)
	tech := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech, nil))

	other := uuid.New()
	before := pub.count()
	err := svc.Accept(ctx, job.ID, other)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict), "want ErrConflict, got %v", err)
	assert.Equal(t, before, pub.count())

	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAssigned, stored.Status)
}

func TestFullHappyChainReachesCompleted(t *testing.T) {
	svc, store, _ := newFixture()
	ctx := context.Background()

	job := jobInStore(t, store, StatusCreated)
	driveDispatching(t, svc, job.ID)
	tech := uuid.New()
	require.NoError(t, svc.AssignTechnician(ctx, job.ID, tech, nil))

	steps := []struct {
		to     Status
		reason string
	}{
		{StatusAccepted, "accepted"},
		{StatusEnRoute, "rolling"},
		{StatusArrived, "on site"},
		{StatusInspection, "inspect"},
		{StatusDiagnosis, "diagnose"},
		{StatusEstimate, "estimate"},
		{StatusAwaitingApproval, "quote sent"},
		{StatusApproved, "customer approved"},
		{StatusRepairing, "repair"},
		{StatusVerification, "verify"},
		{StatusCompleted, "done"},
	}
	for _, step := range steps {
		require.NoError(t, svc.Transition(ctx, job.ID, step.to, &tech, step.reason), "step to %s", step.to)
	}
	stored, err := store.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, stored.Status)
	assert.True(t, IsTerminal(stored.Status))

	transitions, err := store.ListTransitions(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, transitions, 14) // 3 pre-acceptance moves + 11 chain steps
	assert.Equal(t, StatusVerification, transitions[len(transitions)-1].FromStatus)
}

func TestTerminalStatesImmutable(t *testing.T) {
	for _, terminal := range []Status{StatusCompleted, StatusCancelled, StatusFailed} {
		svc, store, pub := newFixture()
		job := jobInStore(t, store, terminal)

		for _, to := range AllStatuses() {
			err := svc.Transition(context.Background(), job.ID, to, nil, "attempt")
			assert.True(t, errors.Is(err, ErrConflict),
				"terminal %s -> %s must conflict, got %v", terminal, to, err)
		}
		err := svc.AssignTechnician(context.Background(), job.ID, uuid.New(), nil)
		assert.True(t, errors.Is(err, ErrConflict), "terminal %s cannot be assigned: %v", terminal, err)
		err = svc.Accept(context.Background(), job.ID, uuid.New())
		assert.True(t, errors.Is(err, ErrConflict), "terminal %s cannot be accepted: %v", terminal, err)
		assert.Zero(t, pub.count(), "terminal job must publish nothing")
	}
}

func TestJobsByStatusListsAndPaginates(t *testing.T) {
	svc, store, _ := newFixture()
	ctx := context.Background()

	created := jobInStore(t, store, StatusCreated)
	dispatching := jobInStore(t, store, StatusDispatching)
	jobInStore(t, store, StatusCreated)

	got, err := svc.JobsByStatus(ctx, StatusCreated, 0, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)

	got, err = svc.JobsByStatus(ctx, StatusDispatching, 10, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, dispatching.ID, got[0].ID)

	got, err = svc.JobsByStatus(ctx, StatusCreated, 1, 1)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, created.ID, got[0].ID, "offset must skip the newest job")

	_, err = svc.JobsByStatus(ctx, Status("NOT_A_STATUS"), 10, 0)
	require.Error(t, err)

	// Terminal and dead-end statuses are valid filters too.
	for _, status := range []Status{StatusCompleted, StatusCancelled, StatusFailed, StatusEscalated} {
		_, err := svc.JobsByStatus(ctx, status, 10, 0)
		assert.NoError(t, err, "status %s must be a valid filter", status)
	}
}

func TestNilPublisherIsTolerated(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, nil)
	ctx := context.Background()

	r := sampleRequest()
	require.NoError(t, svc.CreateRequest(ctx, r))
	job, err := svc.CreateJobFromRequest(ctx, r.ID)
	require.NoError(t, err)
	require.NoError(t, svc.Transition(ctx, job.ID, StatusTriaging, nil, "ok"))
}

func TestPublisherFailurePropagates(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{fail: true}
	svc := NewService(store, pub)

	r := sampleRequest()
	err := svc.CreateRequest(context.Background(), r)
	require.Error(t, err, "a failed publish must surface to the caller")
}

// jobInStore injects a standalone job in the given status, bypassing the
// service (used to seed states without driving the whole chain).
func jobInStore(t *testing.T, store *fakeStore, status Status) Job {
	t.Helper()
	job := Job{
		CustomerID:     uuid.New(),
		Status:         status,
		ProblemSummary: "injected",
	}
	require.NoError(t, store.CreateJob(context.Background(), &job))
	return job
}

func floatPtr(v float64) *float64 { return &v }

func nanPtr() *float64 {
	v := math.NaN()
	return &v
}
