package inspections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// fakeStore is an in-memory Store mirroring the PostgresStore transactional
// semantics closely enough to exercise the service discipline — including
// the sealed-after-submit rule the database triggers enforce.
type fakeStore struct {
	mu          sync.Mutex
	sessions    map[uuid.UUID]Session
	findings    map[uuid.UUID]Finding
	evidence    map[uuid.UUID]Evidence
	reports     map[uuid.UUID]Report
	transitions []SessionTransition
	now         time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions: make(map[uuid.UUID]Session),
		findings: make(map[uuid.UUID]Finding),
		evidence: make(map[uuid.UUID]Evidence),
		reports:  make(map[uuid.UUID]Report),
		now:      time.Now().UTC(),
	}
}

func (f *fakeStore) stamp() time.Time {
	f.now = f.now.Add(time.Millisecond)
	return f.now
}

// sessionVisible mirrors the PostgresStore scope predicate for sessions.
func sessionVisible(s Session, scope Scope) bool {
	switch {
	case scope.Unrestricted():
		return true
	case scope.TenantID != nil:
		return s.TenantID != nil && *s.TenantID == *scope.TenantID
	case scope.PrincipalID != nil:
		return s.InspectorID == *scope.PrincipalID ||
			(s.CustomerID != nil && *s.CustomerID == *scope.PrincipalID)
	default:
		return false // zero scope: fail closed
	}
}

func (f *fakeStore) CreateSession(_ context.Context, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	s.CreatedAt, s.UpdatedAt = f.stamp(), f.stamp()
	f.sessions[s.ID] = *s
	return nil
}

func (f *fakeStore) GetSession(_ context.Context, id uuid.UUID, scope Scope) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok || !sessionVisible(s, scope) {
		return Session{}, fmt.Errorf("inspections: get session %s: %w", id, ErrSessionNotFound)
	}
	return s, nil
}

func (f *fakeStore) ListSessions(_ context.Context, scope Scope, filter SessionFilter, limit, offset int) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Session
	for _, s := range f.sessions {
		if !sessionVisible(s, scope) {
			continue
		}
		if filter.VehicleID != nil && (s.VehicleID == nil || *s.VehicleID != *filter.VehicleID) {
			continue
		}
		if filter.JobID != nil && (s.JobID == nil || *s.JobID != *filter.JobID) {
			continue
		}
		if filter.Status != nil && s.Status != *filter.Status {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID.String() > out[j].ID.String()
	})
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) UpdateSessionStatus(_ context.Context, sessionID uuid.UUID, expectedFrom, newStatus Status, actorID *uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("inspections: update session status %s: %w", sessionID, ErrSessionNotFound)
	}
	if s.Status != expectedFrom {
		return fmt.Errorf("inspections: update session status %s: status moved to %s: %w",
			sessionID, s.Status, ErrConflict)
	}
	f.transitions = append(f.transitions, SessionTransition{
		ID: int64(len(f.transitions) + 1), SessionID: sessionID,
		FromStatus: s.Status, ToStatus: newStatus,
		ActorID: actorID, Reason: reason, CreatedAt: f.stamp(),
	})
	s.Status = newStatus
	switch newStatus {
	case StatusInProgress:
		stamp := f.stamp()
		s.StartedAt = &stamp
	case StatusSubmitted:
		stamp := f.stamp()
		s.SubmittedAt = &stamp
	case StatusReportAvailable:
		stamp := f.stamp()
		s.ReportedAt = &stamp
	}
	s.UpdatedAt = f.stamp()
	f.sessions[sessionID] = s
	return nil
}

func (f *fakeStore) InsertFinding(_ context.Context, finding *Finding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if finding.ID == uuid.Nil {
		finding.ID = uuid.New()
	}
	s, ok := f.sessions[finding.SessionID]
	if !ok {
		return fmt.Errorf("inspections: insert finding: %w", ErrSessionNotFound)
	}
	// Mirror the inspection_findings_sealed_after_submit trigger.
	if s.Status != StatusInProgress {
		return fmt.Errorf("inspections: insert finding: inspection_findings is sealed: session %s is not IN_PROGRESS", s.ID)
	}
	finding.CreatedAt = f.stamp()
	f.findings[finding.ID] = *finding
	return nil
}

func (f *fakeStore) ListFindings(_ context.Context, sessionID uuid.UUID, scope Scope) ([]Finding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || !sessionVisible(s, scope) {
		return nil, fmt.Errorf("inspections: list findings: %w", ErrSessionNotFound)
	}
	var out []Finding
	for _, finding := range f.findings {
		if finding.SessionID == sessionID {
			out = append(out, finding)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (f *fakeStore) InsertEvidence(_ context.Context, e *Evidence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	s, ok := f.sessions[e.SessionID]
	if !ok {
		return fmt.Errorf("inspections: insert evidence: %w", ErrSessionNotFound)
	}
	// Mirror the inspection_evidence_sealed_after_submit trigger.
	if s.Status != StatusInProgress {
		return fmt.Errorf("inspections: insert evidence: inspection_evidence is sealed: session %s is not IN_PROGRESS", s.ID)
	}
	e.CreatedAt = f.stamp()
	f.evidence[e.ID] = *e
	return nil
}

func (f *fakeStore) ListEvidence(_ context.Context, sessionID uuid.UUID, scope Scope) ([]Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || !sessionVisible(s, scope) {
		return nil, fmt.Errorf("inspections: list evidence: %w", ErrSessionNotFound)
	}
	var out []Evidence
	for _, e := range f.evidence {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

func (f *fakeStore) InsertReport(_ context.Context, r *Report) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[r.SessionID]
	if !ok {
		return fmt.Errorf("inspections: insert report for session %s: %w", r.SessionID, ErrSessionNotFound)
	}
	if s.Status != StatusSubmitted {
		return fmt.Errorf("inspections: insert report: session %s is %s, want %s: %w",
			r.SessionID, s.Status, StatusSubmitted, ErrConflict)
	}
	next := 0
	for _, existing := range f.reports {
		if existing.SessionID == r.SessionID && existing.Version > next {
			next = existing.Version
		}
	}
	next++
	if next != r.Version {
		return fmt.Errorf("inspections: insert report: computed version %d does not match declared version %d: %w",
			next, r.Version, ErrConflict)
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	r.CreatedAt = f.stamp()
	f.reports[r.ID] = *r

	stamp := f.stamp()
	s.Status = StatusReportAvailable
	s.ReportedAt = &stamp
	s.UpdatedAt = stamp
	f.sessions[r.SessionID] = s
	f.transitions = append(f.transitions, SessionTransition{
		ID: int64(len(f.transitions) + 1), SessionID: r.SessionID,
		FromStatus: StatusSubmitted, ToStatus: StatusReportAvailable,
		ActorID: r.IssuedBy, Reason: "report issued", CreatedAt: f.stamp(),
	})
	return nil
}

func (f *fakeStore) GetReport(_ context.Context, sessionID uuid.UUID, version *int, scope Scope) (Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok || !sessionVisible(s, scope) {
		return Report{}, fmt.Errorf("inspections: get report for session %s: %w", sessionID, ErrReportNotFound)
	}
	var out *Report
	for i := range f.reports {
		r := f.reports[i]
		if r.SessionID != sessionID {
			continue
		}
		if version != nil && r.Version != *version {
			continue
		}
		if out == nil || r.Version > out.Version {
			rCopy := r
			out = &rCopy
		}
	}
	if out == nil {
		return Report{}, fmt.Errorf("inspections: get report for session %s: %w", sessionID, ErrReportNotFound)
	}
	return *out, nil
}

// capturePublisher records every published event (and the domain it was
// published under) and can be told to fail. The event itself is recorded
// verbatim — the envelope's correlation_id carries the request id, so
// mutating it here would hide regressions.
type capturePublisher struct {
	mu      sync.Mutex
	events  []platform.Event
	domains []string
	fail    error
}

func (p *capturePublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.domains = append(p.domains, domain)
	p.events = append(p.events, e)
	return nil
}

// domainOf returns the domain recorded for the i-th published event.
func (p *capturePublisher) domainOf(t *testing.T, i int) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.domains) {
		t.Fatalf("no domain recorded for event %d", i)
	}
	return p.domains[i]
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

// ofType returns the recorded events of the given type.
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

// newFixture returns a Service over the fake store with a capturing
// publisher and no metrics.
func newFixture() (*Service, *fakeStore, *capturePublisher, *Metrics) {
	store := newFakeStore()
	pub := &capturePublisher{}
	svc := NewService(store, pub, nil)
	return svc, store, pub, nil
}

// staffScope builds a principal scope for a staff member.
func staffScope(principalID uuid.UUID) Scope {
	return Scope{PrincipalID: &principalID}
}

// tenantScope builds a tenant scope.
func tenantScope(tenantID uuid.UUID) Scope {
	return Scope{TenantID: &tenantID}
}

// opScope builds an unrestricted operator scope.
func opScope() Scope {
	return UnrestrictedScope()
}

// validHash derives a deterministic 64-char lowercase hex SHA-256 from a
// seed string (stands in for the media content hash).
func validHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// sessionInStore creates a session through the service (exercising template
// resolution + the created event) and returns it.
func sessionInStore(t *testing.T, store *fakeStore) Session {
	t.Helper()
	return sessionInStoreFor(t, store, uuid.New(), nil)
}

// sessionInStoreFor creates a session for the given inspector and tenant.
func sessionInStoreFor(t *testing.T, store *fakeStore, inspectorID uuid.UUID, tenantID *uuid.UUID) Session {
	t.Helper()
	svc := NewService(store, nil, nil)
	sess := &Session{
		TenantID:     tenantID,
		VehicleClass: ClassSedan,
		InspectorID:  inspectorID,
		Status:       StatusCreated,
	}
	require.NoError(t, svc.CreateSession(context.Background(), sess))
	return *sess
}

// findingInStore appends a finding to an in-progress session through the
// service and returns it.
func findingInStore(t *testing.T, svc *Service, sessionID, actorID uuid.UUID, itemKey string, severity Severity) Finding {
	t.Helper()
	result := ResultPass
	if severity != SeverityGreen {
		result = ResultAttention
	}
	f, err := svc.RecordFinding(context.Background(), sessionID, &Finding{
		ItemKey: itemKey, Result: result, Severity: severity, Notes: "checked on the hoist",
	}, actorID, staffScope(actorID))
	require.NoError(t, err)
	return f
}

// evidenceInStore appends an evidence record to an in-progress session
// through the service and returns it.
func evidenceInStore(t *testing.T, svc *Service, sessionID, actorID uuid.UUID, seed string) Evidence {
	t.Helper()
	e, err := svc.AttachEvidence(context.Background(), sessionID, &Evidence{
		Kind: EvidenceKindPhoto, ContentHash: validHash(seed),
		CapturedAt: time.Now().UTC().Add(-time.Minute), Uploaded: true,
	}, actorID, staffScope(actorID))
	require.NoError(t, err)
	return e
}

// driveToSubmitted runs a session through a complete, valid inspection and
// returns it in SUBMITTED status (all required findings + evidence + RED
// linkage satisfied as applicable).
func driveToSubmitted(t *testing.T, svc *Service, store *fakeStore, sessionID, inspectorID uuid.UUID) Session {
	t.Helper()
	sess, err := svc.Start(context.Background(), sessionID, inspectorID, staffScope(inspectorID))
	require.NoError(t, err)
	template, err := TemplateByID(sess.TemplateID)
	require.NoError(t, err)
	for _, key := range template.RequiredItems() {
		findingInStore(t, svc, sessionID, inspectorID, key, SeverityGreen)
	}
	evidenceInStore(t, svc, sessionID, inspectorID, "primary-evidence")
	submitted, err := svc.Submit(context.Background(), sessionID, inspectorID, staffScope(inspectorID))
	require.NoError(t, err)
	return submitted
}

// --- session creation ---------------------------------------------------

func TestCreateSessionResolvesAndFreezesTemplate(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()

	inspector := uuid.New()
	customer := uuid.New()
	sess := &Session{
		VehicleClass: ClassSedan,
		CustomerID:   &customer,
		InspectorID:  inspector,
	}
	require.NoError(t, svc.CreateSession(context.Background(), sess))

	template, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)
	assert.Equal(t, template.ID, sess.TemplateID)
	assert.Equal(t, template.Name, sess.TemplateName)
	assert.Equal(t, template.Version, sess.TemplateVersion)
	assert.Equal(t, StatusCreated, sess.Status)

	stored, err := store.GetSession(context.Background(), sess.ID, opScope())
	require.NoError(t, err)
	assert.Equal(t, sess.TemplateID, stored.TemplateID)

	events := pub.ofType(t, EventSessionCreated)
	require.Len(t, events, 1)
	assert.Equal(t, sess.ID.String(), events[0].AggregateID)
	assert.Equal(t, inspector.String(), events[0].ActorID)
}

func TestCreateSessionValidation(t *testing.T) {
	t.Parallel()
	svc, _, pub, _ := newFixture()
	ctx := context.Background()

	cases := []struct {
		name string
		sess *Session
	}{
		{"nil session", nil},
		{"missing inspector", &Session{VehicleClass: ClassSedan}},
		{"unknown class", &Session{VehicleClass: "SPACESHIP", InspectorID: uuid.New()}},
		{"nil-uuid vehicle", &Session{VehicleClass: ClassSedan, InspectorID: uuid.New(), VehicleID: &[]uuid.UUID{uuid.Nil}[0]}},
		{"nil-uuid job", &Session{VehicleClass: ClassSedan, InspectorID: uuid.New(), JobID: &[]uuid.UUID{uuid.Nil}[0]}},
		{"nil-uuid customer", &Session{VehicleClass: ClassSedan, InspectorID: uuid.New(), CustomerID: &[]uuid.UUID{uuid.Nil}[0]}},
		{"preset non-created status", &Session{VehicleClass: ClassSedan, InspectorID: uuid.New(), Status: StatusSubmitted}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := svc.CreateSession(ctx, tc.sess)
			require.Error(t, err)
		})
	}
	assert.Zero(t, pub.count(), "failed creations must publish nothing")
}

// --- start ---------------------------------------------------------------

func TestStartGuardsAndTransitions(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	other := uuid.New()
	tenant := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, &tenant)
	ctx := context.Background()

	// A stranger cannot even see the session (fail-closed 404 semantics).
	_, err := svc.Start(ctx, sess.ID, other, staffScope(other))
	require.True(t, errors.Is(err, ErrSessionNotFound), "got %v", err)
	assert.Zero(t, pub.count(), "denied start must publish nothing")

	// A tenant-mate who can READ the session still cannot work it.
	_, err = svc.Start(ctx, sess.ID, other, tenantScope(tenant))
	require.True(t, errors.Is(err, ErrNotInspector), "got %v", err)
	assert.Zero(t, pub.count(), "denied start must publish nothing")

	// The inspector can; a second start is an illegal transition.
	started, err := svc.Start(ctx, sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, started.Status)

	_, err = svc.Start(ctx, sess.ID, inspector, staffScope(inspector))
	require.True(t, errors.Is(err, ErrConflict), "got %v", err)

	// An operator may start a session it does not inspect.
	opSess := sessionInStoreFor(t, store, uuid.New(), nil)
	_, err = svc.Start(ctx, opSess.ID, uuid.New(), opScope())
	require.NoError(t, err, "operators work any visible session")

	// Finding on a CREATED session is refused (must start first).
	fresh := sessionInStoreFor(t, store, inspector, &tenant)
	template, err := TemplateByID(fresh.TemplateID)
	require.NoError(t, err)
	_, err = svc.RecordFinding(ctx, fresh.ID, &Finding{ItemKey: template.Items[0].Key, Result: ResultPass, Severity: SeverityGreen},
		inspector, staffScope(inspector))
	require.True(t, errors.Is(err, ErrSessionSealed), "got %v", err)
}

// --- findings ------------------------------------------------------------

func TestRecordFindingRules(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	ctx := context.Background()

	cases := []struct {
		name    string
		itemKey string
		result  FindingResult
		sev     Severity
		wantErr bool
	}{
		{"pass/green", "brakes", ResultPass, SeverityGreen, false},
		{"pass/amber observation", "brakes", ResultPass, SeverityAmber, false},
		{"fail/amber", "brakes", ResultFail, SeverityAmber, false},
		{"fail/red", "brakes", ResultFail, SeverityRed, false},
		{"n/a/green", "body_panel_condition", ResultNotApplicable, SeverityGreen, false},
		{"unknown item", "jet_engine", ResultPass, SeverityGreen, true},
		{"unknown result", "brakes", "EXCEPTABLE", SeverityGreen, true},
		{"unknown severity", "brakes", ResultPass, "MAUVE", true},
		{"illegal pair pass/red", "brakes", ResultPass, SeverityRed, true},
		{"illegal pair n/a/amber", "body_panel_condition", ResultNotApplicable, SeverityAmber, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := svc.RecordFinding(ctx, sess.ID, &Finding{
				ItemKey: tc.itemKey, Result: tc.result, Severity: tc.sev, Notes: "checked",
			}, inspector, staffScope(inspector))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, sess.ID, f.SessionID)
			require.NotNil(t, f.RecordedBy)
			assert.Equal(t, inspector, *f.RecordedBy)
		})
	}

	long := strings.Repeat("x", MaxNotesLength+1)
	_, err = svc.RecordFinding(ctx, sess.ID, &Finding{ItemKey: "brakes", Result: ResultPass, Severity: SeverityGreen, Notes: long},
		inspector, staffScope(inspector))
	require.Error(t, err, "over-long notes are refused")
}

func TestFindingCorrectionIsANewRecordAndLatestWins(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	ctx := context.Background()

	first := findingInStore(t, svc, sess.ID, inspector, "brakes", SeverityGreen)
	second := findingInStore(t, svc, sess.ID, inspector, "brakes", SeverityAmber)
	require.NotEqual(t, first.ID, second.ID, "corrections are new records, never edits")

	findings, err := svc.Findings(ctx, sess.ID, staffScope(inspector))
	require.NoError(t, err)
	require.Len(t, findings, 2, "both records stay (append-only)")

	selected := SelectLatestFindings(findings)
	require.Len(t, selected, 1)
	assert.Equal(t, second.ID, selected[0].ID, "the newest record per item wins")
	assert.Equal(t, SeverityAmber, selected[0].Severity)
}

// --- evidence ------------------------------------------------------------

func TestAttachEvidenceRules(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	ctx := context.Background()

	finding := findingInStore(t, svc, sess.ID, inspector, "brakes", SeverityGreen)

	cases := []struct {
		name    string
		e       *Evidence
		wantErr bool
	}{
		{"photo with hash", &Evidence{Kind: EvidenceKindPhoto, ContentHash: validHash("p1"), CapturedAt: time.Now().UTC().Add(-time.Minute)}, false},
		{"reading", &Evidence{Kind: EvidenceKindReading, ContentHash: validHash("r1"), CapturedAt: time.Now().UTC().Add(-time.Minute)}, false},
		{"uppercase hash refused", &Evidence{Kind: EvidenceKindPhoto, ContentHash: strings.ToUpper(validHash("p2")), CapturedAt: time.Now().UTC().Add(-time.Minute)}, true},
		{"short hash refused", &Evidence{Kind: EvidenceKindPhoto, ContentHash: "abc123", CapturedAt: time.Now().UTC().Add(-time.Minute)}, true},
		{"unknown kind refused", &Evidence{Kind: "VIDEO", ContentHash: validHash("p3"), CapturedAt: time.Now().UTC().Add(-time.Minute)}, true},
		{"zero captured_at refused", &Evidence{Kind: EvidenceKindPhoto, ContentHash: validHash("p4")}, true},
		{"future captured_at refused", &Evidence{Kind: EvidenceKindPhoto, ContentHash: validHash("p5"), CapturedAt: time.Now().UTC().Add(time.Hour)}, true},
		{"foreign finding linkage refused", &Evidence{Kind: EvidenceKindPhoto, ContentHash: validHash("p6"), CapturedAt: time.Now().UTC().Add(-time.Minute), FindingID: &[]uuid.UUID{uuid.New()}[0]}, true},
		{"own finding linkage ok", &Evidence{Kind: EvidenceKindPhoto, ContentHash: validHash("p7"), CapturedAt: time.Now().UTC().Add(-time.Minute), FindingID: &finding.ID}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := svc.AttachEvidence(ctx, sess.ID, tc.e, inspector, staffScope(inspector))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, sess.ID, e.SessionID)
			require.NotNil(t, e.CapturedBy)
			assert.Equal(t, inspector, *e.CapturedBy)
		})
	}
}

// --- submit --------------------------------------------------------------

func TestSubmitRefusesIncompleteInspections(t *testing.T) {
	t.Parallel()
	inspector := uuid.New()

	t.Run("no evidence at all", func(t *testing.T) {
		t.Parallel()
		svc, store, pub, _ := newFixture()
		sess := sessionInStoreFor(t, store, inspector, nil)
		_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.NoError(t, err)
		template, err := TemplateByID(sess.TemplateID)
		require.NoError(t, err)
		for _, key := range template.RequiredItems() {
			findingInStore(t, svc, sess.ID, inspector, key, SeverityGreen)
		}
		eventsBefore := pub.count()
		_, err = svc.Submit(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.True(t, errors.Is(err, ErrReportIncomplete), "got %v", err)
		assert.Contains(t, err.Error(), "no evidence records", "the missing-evidence issue must be named")
		assert.Equal(t, eventsBefore, pub.count(), "refused submit must publish nothing")
	})

	t.Run("missing required finding", func(t *testing.T) {
		t.Parallel()
		svc, store, _, _ := newFixture()
		sess := sessionInStoreFor(t, store, inspector, nil)
		_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.NoError(t, err)
		evidenceInStore(t, svc, sess.ID, inspector, "ev")
		_, err = svc.Submit(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.True(t, errors.Is(err, ErrReportIncomplete), "got %v", err)
		assert.Contains(t, err.Error(), "has no finding")
	})

	t.Run("RED finding without evidence", func(t *testing.T) {
		t.Parallel()
		svc, store, _, _ := newFixture()
		sess := sessionInStoreFor(t, store, inspector, nil)
		_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.NoError(t, err)
		template, err := TemplateByID(sess.TemplateID)
		require.NoError(t, err)
		for _, key := range template.RequiredItems() {
			if key == "brakes" {
				f, ferr := svc.RecordFinding(context.Background(), sess.ID, &Finding{
					ItemKey: key, Result: ResultFail, Severity: SeverityRed, Notes: "pads gone",
				}, inspector, staffScope(inspector))
				require.NoError(t, ferr)
				_ = f
				continue
			}
			findingInStore(t, svc, sess.ID, inspector, key, SeverityGreen)
		}
		evidenceInStore(t, svc, sess.ID, inspector, "ev")
		_, err = svc.Submit(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.True(t, errors.Is(err, ErrReportIncomplete), "got %v", err)
		assert.Contains(t, err.Error(), "RED finding")
	})

	t.Run("RED finding with linked evidence passes", func(t *testing.T) {
		t.Parallel()
		svc, store, _, _ := newFixture()
		sess := sessionInStoreFor(t, store, inspector, nil)
		_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.NoError(t, err)
		template, err := TemplateByID(sess.TemplateID)
		require.NoError(t, err)
		var redFinding Finding
		for _, key := range template.RequiredItems() {
			if key == "brakes" {
				redFinding, err = svc.RecordFinding(context.Background(), sess.ID, &Finding{
					ItemKey: key, Result: ResultFail, Severity: SeverityRed, Notes: "pads gone",
				}, inspector, staffScope(inspector))
				require.NoError(t, err)
				continue
			}
			findingInStore(t, svc, sess.ID, inspector, key, SeverityGreen)
		}
		evidenceInStore(t, svc, sess.ID, inspector, "ev")
		_, err = svc.AttachEvidence(context.Background(), sess.ID, &Evidence{
			Kind: EvidenceKindPhoto, ContentHash: validHash("red-brakes"),
			CapturedAt: time.Now().UTC().Add(-time.Minute), FindingID: &redFinding.ID,
		}, inspector, staffScope(inspector))
		require.NoError(t, err)
		submitted, err := svc.Submit(context.Background(), sess.ID, inspector, staffScope(inspector))
		require.NoError(t, err)
		assert.Equal(t, StatusSubmitted, submitted.Status)
	})
}

func TestSubmitPublishesCounts(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	driveToSubmitted(t, svc, store, sess.ID, inspector)

	events := pub.ofType(t, EventSessionSubmitted)
	require.Len(t, events, 1)
	var payload SessionSubmittedPayload
	require.NoError(t, unmarshalPayload(events[0].Payload, &payload))
	assert.Equal(t, sess.ID, payload.SessionID)
	assert.Equal(t, inspector, payload.InspectorID)
	assert.Equal(t, 7, payload.FindingsCount, "7 required sedan items")
	assert.Equal(t, 1, payload.EvidenceCount)
}

// --- report assembly ------------------------------------------------------

func TestIssueReportRequiresSubmittedSession(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)

	// CREATED session: the state machine refuses before anything else.
	_, err := svc.IssueReport(context.Background(), sess.ID, inspector, "", staffScope(inspector))
	require.True(t, errors.Is(err, ErrConflict), "got %v", err)

	// IN_PROGRESS session: still refuses.
	_, err = svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	_, err = svc.IssueReport(context.Background(), sess.ID, inspector, "", staffScope(inspector))
	require.True(t, errors.Is(err, ErrConflict), "got %v", err)
}

func TestIssueReportAndVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	svc, store, pub, _ := newFixture()
	inspector := uuid.New()
	vehicle := uuid.New()
	job := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	// Stamp the linkage on the stored session.
	stored := sess
	stored.VehicleID, stored.JobID = &vehicle, &job
	store.mu.Lock()
	store.sessions[sess.ID] = stored
	store.mu.Unlock()

	_ = driveToSubmitted(t, svc, store, sess.ID, inspector)

	report, err := svc.IssueReport(context.Background(), sess.ID, inspector, "issued after re-check", staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, ReportVersionInitial, report.Version)
	require.Len(t, report.ContentHash, 64)
	assert.Equal(t, &vehicle, report.VehicleID)
	assert.Equal(t, &job, report.JobID)

	// Second issuance conflicts: the session is now REPORT_AVAILABLE.
	_, err = svc.IssueReport(context.Background(), sess.ID, inspector, "", staffScope(inspector))
	require.True(t, errors.Is(err, ErrConflict), "got %v", err)

	// The issued event carries the hash so downstream consumers can pin it.
	events := pub.ofType(t, EventReportIssued)
	require.Len(t, events, 1)
	var payload ReportIssuedPayload
	require.NoError(t, unmarshalPayload(events[0].Payload, &payload))
	assert.Equal(t, report.ID, payload.ReportID)
	assert.Equal(t, report.ContentHash, payload.ContentHash)

	// Verification recomputes from the live rows and verifies.
	verification, err := svc.Verify(context.Background(), sess.ID, nil, staffScope(inspector))
	require.NoError(t, err)
	assert.True(t, verification.Verified, "body: %+v", verification)
	assert.True(t, verification.HashMatch)
	assert.True(t, verification.FindingsMatch)
	assert.True(t, verification.EvidenceMatch)
	assert.True(t, verification.TemplateMatch)
	assert.Equal(t, report.ContentHash, verification.RecomputedHash)

	// The report is fetchable with its snapshots decoded.
	fetched, err := svc.GetReport(context.Background(), sess.ID, nil, staffScope(inspector))
	require.NoError(t, err)
	assert.Equal(t, report.ID, fetched.ID)
	assert.Equal(t, report.ContentHash, fetched.ContentHash)
}

func TestIssueReportRefusesWithoutEvidence(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	_, err := svc.Start(context.Background(), sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	template, err := TemplateByID(sess.TemplateID)
	require.NoError(t, err)
	for _, key := range template.RequiredItems() {
		findingInStore(t, svc, sess.ID, inspector, key, SeverityGreen)
	}
	// Corrupt-path submit: the sealed rule is what the service checks, so
	// simulate a session that reached SUBMITTED without evidence by moving
	// it through the store directly (as a broken deployment might).
	require.NoError(t, store.UpdateSessionStatus(context.Background(), sess.ID, StatusInProgress, StatusSubmitted, &inspector, "forced"))

	_, err = svc.IssueReport(context.Background(), sess.ID, inspector, "", staffScope(inspector))
	require.True(t, errors.Is(err, ErrReportIncomplete), "got %v", err)
	assert.Contains(t, err.Error(), "no evidence records")
}

// --- event publishing -----------------------------------------------------

func TestPublishFailureSurfacesAndCounts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	pub := &capturePublisher{fail: errors.New("jetstream unavailable")}
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	svc := NewService(store, pub, metrics)

	err := svc.CreateSession(context.Background(), &Session{VehicleClass: ClassSedan, InspectorID: uuid.New()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jetstream unavailable")

	// The failure counter is observable.
	families, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, mf := range families {
		if mf.GetName() != "inspections_events_published_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["event_type"] == EventSessionCreated && labels["outcome"] == "failure" {
				found = m.GetCounter().GetValue() == 1
			}
		}
	}
	assert.True(t, found, "failure counter for session.created.v1 must be 1")
}

// --- metrics --------------------------------------------------------------

func TestMetricsCountersAndHistograms(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	store := newFakeStore()
	svc := NewService(store, nil, metrics)

	inspector := uuid.New()
	sess := &Session{VehicleClass: ClassSedan, InspectorID: inspector}
	require.NoError(t, svc.CreateSession(context.Background(), sess))
	_ = driveToSubmitted(t, svc, store, sess.ID, inspector)
	_, err := svc.IssueReport(context.Background(), sess.ID, inspector, "", staffScope(inspector))
	require.NoError(t, err)

	families, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, mf := range families {
		name := mf.GetName()
		switch name {
		case "inspections_sessions_created_total", "inspections_sessions_submitted_total", "inspections_reports_issued_total":
			for _, m := range mf.GetMetric() {
				got[name] += m.GetCounter().GetValue()
			}
		case "inspections_report_assembly_seconds":
			for _, m := range mf.GetMetric() {
				got[name] += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	assert.Equal(t, 1.0, got["inspections_sessions_created_total"])
	assert.Equal(t, 1.0, got["inspections_sessions_submitted_total"])
	assert.Equal(t, 1.0, got["inspections_reports_issued_total"])
	assert.Equal(t, 1.0, got["inspections_report_assembly_seconds"], "one assembly observation")
}

func TestNilMetricsAndNilPublisherAreSafe(t *testing.T) {
	t.Parallel()
	svc, store, _, _ := newFixture()
	inspector := uuid.New()
	sess := sessionInStoreFor(t, store, inspector, nil)
	require.NotNil(t, svc)
	// nil publisher + nil metrics: full lifecycle runs without events.
	_ = driveToSubmitted(t, svc, store, sess.ID, inspector)
}

// unmarshalPayload decodes an envelope payload into out.
func unmarshalPayload(raw json.RawMessage, out any) error {
	return json.Unmarshal(raw, out)
}
