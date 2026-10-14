package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// Dispatch decision records (issue #88): every successful scoring call is
// appended to the platform's write-once audit trail with full attribution
// (actor, tenant, inputs hash, chosen technician, score), and a failed
// record fails the request. These tests prove the HTTP contract through
// the real platform middleware chain, exactly as cmd/dispatch mounts it.

// decisionTestSecret signs test tokens for the recorder tests below.
const decisionTestSecret = "dispatch-decision-test-secret-1234"

// fakeDecisionRecorder captures decision records and can be told to fail.
type fakeDecisionRecorder struct {
	fail    bool
	records []DecisionRecord
}

func (r *fakeDecisionRecorder) RecordDecision(_ context.Context, record DecisionRecord) error {
	if r.fail {
		return errors.New("injected recorder failure")
	}
	r.records = append(r.records, record)
	return nil
}

// mintRecorderToken issues a valid HS256 access token with a tenant claim.
func mintRecorderToken(t *testing.T, sub, tenant, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  sub,
		"role": role,
		"iss":  "motivra-identity",
		"aud":  []string{"motivra"},
		"exp":  time.Now().Add(15 * time.Minute).Unix(),
		"iat":  time.Now().Add(-time.Minute).Unix(),
	}
	if tenant != "" {
		claims["tenant_id"] = tenant
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(decisionTestSecret))
	require.NoError(t, err)
	return signed
}

// newRecorderTestAPI wires Routes with a real auth middleware and RBAC
// gate (as cmd/dispatch boots them) plus the given recorder.
func newRecorderTestAPI(recorder DecisionRecorder) http.Handler {
	validator := platform.NewJWTValidator(decisionTestSecret, "motivra-identity", "motivra")
	cfg := platform.LoadForTest()
	srv := platform.NewServer(cfg, platform.NewLogger(cfg.App), platform.WithMiddleware(platform.AuthMiddleware(validator)))
	Routes(srv.Router, func(h http.HandlerFunc) http.HandlerFunc {
		return platform.RequireRole(DispatcherRoles, h)
	}, recorder)
	return srv
}

func decisionBody(jobID string) string {
	body := `{"job_context":{"required_skills":["engine"],"vehicle_make":"Toyota","required_parts":["oil filter"]},"candidates":[` +
		`{"technician_id":"00000000-0000-4000-8000-000000000001","eta_minutes":10,"skills":["engine"],"available":true,"has_equipment":true,"has_required_parts":true,"acceptance_rate":0.9,"completion_rate":0.9,"avg_customer_rating":4.5},` +
		`{"technician_id":"00000000-0000-4000-8000-000000000002","eta_minutes":25,"skills":["engine"],"available":true,"has_equipment":true,"has_required_parts":false,"acceptance_rate":0.8,"completion_rate":0.7,"avg_customer_rating":4.0}]`
	if jobID != "" {
		body += `,"job_id":"` + jobID + `"`
	}
	return body + "}"
}

func TestScoreEndpointRecordsDecision(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)

	actor := uuid.New()
	tenant := uuid.New()
	jobID := uuid.New()
	token := mintRecorderToken(t, actor.String(), tenant.String(), "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(jobID.String()))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	// The response is unchanged by persistence, and the top score on the
	// wire must be the recorded chosen technician/score.
	var resp scoreResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Scores)

	require.Len(t, recorder.records, 1, "exactly one decision record per successful scoring call")
	record := recorder.records[0]
	assert.Equal(t, actor, record.ActorID, "full attribution: the acting principal")
	require.NotNil(t, record.TenantID)
	assert.Equal(t, tenant, *record.TenantID, "full attribution: the tenant scope")
	require.NotNil(t, record.JobID)
	assert.Equal(t, jobID, *record.JobID, "full attribution: the job being staffed")
	assert.NotEqual(t, uuid.Nil, record.DecisionID, "each record carries a fresh decision id")
	assert.Equal(t, resp.Scores[0].TechnicianID, record.ChosenTechnicianID, "the chosen technician is the engine's top score")
	assert.Equal(t, resp.Scores[0].Total, record.ChosenScore)
	assert.Equal(t, 2, record.CandidateCount)
	assert.Equal(t, DefaultWeights(), record.Weights, "applied defaults are recorded when the request omits weights")
	assert.NotEmpty(t, record.InputsHash)

	// The persisted shape: the PostgresDecisionRecorder's metadata maps
	// 1:1 onto what the audit API will read back.
	assert.Equal(t, ActionDispatchScored, "dispatch.decision.scored")
	assert.Equal(t, DecisionObjectType, "dispatch_decision")
}

func TestScoreEndpointDecisionInputsHashIsDeterministic(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)

	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	body := decisionBody("")

	require.Equal(t, http.StatusOK, doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, body).Code)
	require.Equal(t, http.StatusOK, doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, body).Code)
	require.Len(t, recorder.records, 2)
	assert.Equal(t, recorder.records[0].InputsHash, recorder.records[1].InputsHash,
		"identical inputs hash identically — the decision record is replayable")

	// A changed input (different applied weights) changes the digest.
	recorder2 := &fakeDecisionRecorder{}
	h2 := newRecorderTestAPI(recorder2)
	weightsBody := decisionBody("")[:len(decisionBody(""))-1] + `,"weights":{"technical_fit":0.3,"proximity":0.2,"availability":0.15,"equipment":0.15,"parts":0.05,"performance":0.15}}`
	require.Equal(t, http.StatusOK, doBearer(t, h2, http.MethodPost, "/v1/dispatch/score", token, weightsBody).Code)
	require.Len(t, recorder2.records, 1)
	assert.NotEqual(t, recorder.records[0].InputsHash, recorder2.records[0].InputsHash,
		"different applied weights must produce a different inputs hash")

	// Unit-level: same inputs, same hash; any change, different hash.
	cands := []Candidate{{TechnicianID: uuid.New(), ETAMinutes: 10, Available: true}}
	job := JobContext{RequiredSkills: []string{"engine"}}
	w := DefaultWeights()
	assert.Equal(t, InputsHash(job, cands, w), InputsHash(job, cands, w))
	reordered := []Candidate{cands[0], {TechnicianID: uuid.New(), ETAMinutes: 5, Available: true}}
	assert.NotEqual(t, InputsHash(job, cands, w), InputsHash(job, reordered, w),
		"candidate order is part of what was scored")
	assert.NotEqual(t, InputsHash(job, cands, w), InputsHash(JobContext{RequiredSkills: []string{"brakes"}}, cands, w))
}

func TestScoreEndpointFailClosedOnRecorderFailure(t *testing.T) {
	recorder := &fakeDecisionRecorder{fail: true}
	h := newRecorderTestAPI(recorder)

	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(""))
	require.Equal(t, http.StatusInternalServerError, rec.Code, "a failed decision record must fail the request (ADR-0004)")
	require.Contains(t, rec.Body.String(), "internal_error", "the failure detail is not leaked")
	assert.Empty(t, recorder.records)
}

func TestScoreEndpointWithoutRecorderStillScores(t *testing.T) {
	h := newRecorderTestAPI(nil) // deployments without a database
	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(""))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestScoreEndpointJobIDValidation(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)
	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")

	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody("not-a-uuid"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
	assert.Empty(t, recorder.records, "invalid requests record nothing")

	// The zero UUID is rejected: it is not a job attribution.
	rec = doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(uuid.Nil.String()))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	// A personal (tenant-less) principal still records, with a nil tenant.
	rec = doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(""))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, recorder.records, 1)
	assert.Nil(t, recorder.records[0].TenantID, "personal principals record personal decisions")
}

func TestScoreDeniedCallRecordsNothing(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)
	token := mintRecorderToken(t, uuid.New().String(), "", "CUSTOMER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(""))
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, recorder.records, "denied calls record nothing")
}

// auditLogDDL mirrors the identity chain's audit_log table (000004) and
// its append-only trigger (000006). It exists so the dispatch recorder's
// INSERT is proven against the real shape without the dispatch test
// binary importing another domain's migrations. Drift in the identity
// chain is caught by identity's own gated suites.
const auditLogDDL = `
CREATE TABLE audit_log (
    id          bigserial   PRIMARY KEY,
    actor_id    uuid,
    action      text        NOT NULL,
    object_type text        NOT NULL,
    object_id   text        NOT NULL,
    tenant_id   uuid,
    metadata    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION prevent_audit_log_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_mutation();
`

// TestPostgresDecisionRecorderWritesAttributedRow (PG-gated) proves the
// production recorder against a real PostgreSQL: the INSERT lands with
// full attribution, the metadata round-trips as JSONB, and the
// append-only trigger refuses any later mutation of the decision record.
func TestPostgresDecisionRecorderWritesAttributedRow(t *testing.T) {
	pool := pgtest.Pool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS audit_log CASCADE; DROP FUNCTION IF EXISTS prevent_audit_log_mutation();`)
	})
	_, err := pool.Exec(t.Context(), auditLogDDL)
	require.NoError(t, err)

	recorder := NewPostgresDecisionRecorder(pool)
	tenant := uuid.New()
	job := uuid.New()
	record := DecisionRecord{
		DecisionID:         uuid.New(),
		ActorID:            uuid.New(),
		TenantID:           &tenant,
		JobID:              &job,
		InputsHash:         "deadbeef01",
		ChosenTechnicianID: uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		ChosenScore:        0.8123,
		CandidateCount:     3,
		Weights:            DefaultWeights(),
	}
	require.NoError(t, recorder.RecordDecision(t.Context(), record))

	var (
		action     string
		objectType string
		objectID   uuid.UUID
		actorID    uuid.UUID
		rowTenant  uuid.UUID
		metadata   map[string]any
	)
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT action, object_type, object_id, actor_id, tenant_id, metadata FROM audit_log WHERE object_id = $1`,
		record.DecisionID.String(),
	).Scan(&action, &objectType, &objectID, &actorID, &rowTenant, &metadata))
	assert.Equal(t, ActionDispatchScored, action)
	assert.Equal(t, DecisionObjectType, objectType)
	assert.Equal(t, record.DecisionID, objectID)
	assert.Equal(t, record.ActorID, actorID)
	assert.Equal(t, tenant, rowTenant)
	assert.Equal(t, "deadbeef01", metadata["inputs_sha256"])
	assert.Equal(t, record.ChosenTechnicianID.String(), metadata["chosen_technician_id"])
	assert.Equal(t, job.String(), metadata["job_id"])
	assert.Equal(t, float64(3), metadata["candidate_count"])

	// The write-once guarantee holds for decision records too.
	_, err = pool.Exec(t.Context(), `UPDATE audit_log SET action = 'tampered' WHERE action = $1`, ActionDispatchScored)
	require.Error(t, err, "the append-only trigger must refuse mutation of a decision record")
}
