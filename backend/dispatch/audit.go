package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dispatch decision records (issue #88): every successful scoring
// decision is appended to the platform's write-once audit trail
// (audit_log, ADR-0003 append-only rules + ADR-0004 privileged-action
// auditing) with full attribution — the acting principal, their tenant
// scope, the decision inputs hash, the chosen technician and score — so
// the admin audit API (GET /v1/admin/audit, identity domain) can answer
// "why was this job dispatched to this technician" after the fact.
//
// audit_log is the platform-level audit trail whose schema lives in the
// identity migration chain; writers outside identity hold an INSERT-only
// seam and never read other domains' tables. The alternative — a
// dispatch-owned decision table — would fragment the single queryable
// trail the issue requires.

// ActionDispatchScored is the audit action of one scoring decision.
const ActionDispatchScored = "dispatch.decision.scored"

// DecisionObjectType is the audit object_type of one scoring decision.
// The object id is a fresh decision UUID minted per record.
const DecisionObjectType = "dispatch_decision"

// DecisionRecord is one audited scoring outcome.
type DecisionRecord struct {
	// DecisionID is the fresh identifier of this decision (the audit row's
	// object id) — stable, attributable handle for investigations.
	DecisionID uuid.UUID
	// ActorID is the authenticated principal that ran the scoring.
	ActorID uuid.UUID
	// TenantID is the actor's tenant claim (nil for personal principals).
	TenantID *uuid.UUID
	// JobID is the optional job attribution supplied by the dispatch
	// console; nil for what-if scoring with no job context.
	JobID *uuid.UUID
	// InputsHash is the SHA-256 hex digest over the canonical serialization
	// of the exact inputs that were scored (job context, candidate set in
	// request order, and the APPLIED weight set — defaults included when
	// the request omitted weights).
	InputsHash string
	// ChosenTechnicianID and ChosenScore capture the engine's top
	// recommendation: the highest-scoring candidate and their total.
	ChosenTechnicianID uuid.UUID
	ChosenScore        float64
	// CandidateCount is the size of the scored candidate set.
	CandidateCount int
	// Weights are the weights that were applied.
	Weights Weights
}

// DecisionRecorder persists one decision record. Implementations must be
// append-only: no update or delete path exists.
type DecisionRecorder interface {
	RecordDecision(ctx context.Context, record DecisionRecord) error
}

// PostgresDecisionRecorder implements DecisionRecorder on the shared
// audit_log table through a pgx pool.
type PostgresDecisionRecorder struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresDecisionRecorder satisfies
// DecisionRecorder.
var _ DecisionRecorder = (*PostgresDecisionRecorder)(nil)

// NewPostgresDecisionRecorder returns a DecisionRecorder backed by pool.
// The pool must have the identity migration chain applied (the audit_log
// table's owner); cmd/dispatch wires it when MOTIVRA_DATABASE_URL is set.
func NewPostgresDecisionRecorder(pool *pgxpool.Pool) *PostgresDecisionRecorder {
	return &PostgresDecisionRecorder{pool: pool}
}

// RecordDecision appends the decision record. A failure is never
// swallowed: the HTTP layer fails the request so the audit signal cannot
// be lost silently (ADR-0004).
func (r *PostgresDecisionRecorder) RecordDecision(ctx context.Context, record DecisionRecord) error {
	weights := map[string]float64{
		"technical_fit": record.Weights.TechnicalFit,
		"proximity":     record.Weights.Proximity,
		"availability":  record.Weights.Availability,
		"equipment":     record.Weights.Equipment,
		"parts":         record.Weights.Parts,
		"performance":   record.Weights.Performance,
	}
	metadata := map[string]any{
		"inputs_sha256":        record.InputsHash,
		"chosen_technician_id": record.ChosenTechnicianID.String(),
		"chosen_score":         record.ChosenScore,
		"candidate_count":      record.CandidateCount,
		"weights":              weights,
	}
	if record.JobID != nil {
		metadata["job_id"] = record.JobID.String()
	}
	if _, err := r.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, object_type, object_id, tenant_id, metadata)
                 VALUES ($1, $2, $3, $4, $5, $6)`,
		record.ActorID, ActionDispatchScored, DecisionObjectType, record.DecisionID, record.TenantID, metadata,
	); err != nil {
		return fmt.Errorf("insert dispatch decision record: %w", err)
	}
	return nil
}

// scoreInputs is the canonical serialization shape hashed into
// InputsHash. Struct fields serialize in declaration order and Go's
// encoding/json emits stable float encodings, so identical inputs always
// hash identically; any changed input — a different candidate order
// included, because request order is part of what was scored — changes
// the digest.
type scoreInputs struct {
	JobContext JobContext  `json:"job_context"`
	Candidates []Candidate `json:"candidates"`
	Weights    Weights     `json:"weights"`
}

// InputsHash returns the SHA-256 hex digest of the canonical inputs that
// produced a decision. The applied weights are part of the digest, so a
// re-score under different weights is never conflated with the original.
func InputsHash(job JobContext, candidates []Candidate, applied Weights) string {
	b, err := json.Marshal(scoreInputs{JobContext: job, Candidates: candidates, Weights: applied})
	if err != nil { // unreachable for this plain-data shape; stay total
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
