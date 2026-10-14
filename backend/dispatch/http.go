package dispatch

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// DispatcherRoles are the RBAC roles allowed to score dispatch candidates
// and read the factor catalogue. The dispatch console acts as DISPATCHER;
// administrators may inspect and override.
var DispatcherRoles = []string{"DISPATCHER", "ADMIN", "SUPER_ADMIN"}

const (
	// maxRequestBytes caps JSON request bodies accepted by the handlers.
	maxRequestBytes int64 = 64 << 10

	// maxCandidatesPerRequest caps one scoring call. Scoring is O(n); the
	// cap keeps worst-case latency and payload size predictable and blocks
	// abuse of the public endpoint.
	maxCandidatesPerRequest = 200

	// maxListEntries caps repeated fields inside a candidate or job context.
	maxListEntries = 50

	// maxStringEntryLength caps a single skill, make or part label.
	maxStringEntryLength = 64
)

// Routes registers the dispatch scoring HTTP API on r:
//
//	POST /v1/dispatch/score    score a candidate set against a job
//	GET  /v1/dispatch/factors  factor catalogue and default weights
//
// require wraps every handler with the platform authentication and RBAC
// gate; pass a function that enforces platform.RequireRole with
// DispatcherRoles. The AuthMiddleware must already be part of the router
// middleware chain so ClaimsFromContext resolves. recorder (optional,
// issue #88) appends one decision record to the audit trail per
// successful scoring call; nil disables persistence (tests, deployments
// without a database — a boot warning is expected there). When a recorder
// IS configured, a failed record fails the request: the audit signal is
// never lost silently (ADR-0004).
func Routes(r chi.Router, require func(http.HandlerFunc) http.HandlerFunc, recorder DecisionRecorder) {
	r.Route("/v1/dispatch", func(r chi.Router) {
		r.Post("/score", require(scoreWithRecorder(recorder)))
		r.Get("/factors", require(handleFactors))
	})
}

// scoreRequest is the POST /v1/dispatch/score body. Weights are optional:
// when omitted the engine applies DefaultWeights. job_id is optional
// attribution (issue #88): when set, the persisted decision record links
// the scoring to the job being staffed.
type scoreRequest struct {
	JobID      *uuid.UUID  `json:"job_id,omitempty"`
	JobContext JobContext  `json:"job_context"`
	Candidates []Candidate `json:"candidates"`
	Weights    *Weights    `json:"weights,omitempty"`
}

// scoreResponse is the POST /v1/dispatch/score reply. The applied weights
// are echoed so every response is self-describing.
type scoreResponse struct {
	Weights Weights `json:"weights"`
	Scores  []Score `json:"scores"`
}

// factorCatalogueResponse is the GET /v1/dispatch/factors reply.
type factorCatalogueResponse struct {
	Factors        []FactorInfo `json:"factors"`
	DefaultWeights Weights      `json:"default_weights"`
}

// handleScore implements POST /v1/dispatch/score with no decision
// persistence (the pre-#88 shape, kept for direct callers and tests).
func handleScore(w http.ResponseWriter, r *http.Request) {
	scoreWithRecorder(nil)(w, r)
}

// scoreWithRecorder returns the POST /v1/dispatch/score handler bound to
// a decision recorder. The computation is the stateless engine; the
// recorder (when configured) appends one decision record — actor, tenant,
// inputs hash, chosen technician, score — and a failed record fails the
// request with a 500 (fail closed, ADR-0004).
func scoreWithRecorder(recorder DecisionRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, resp, err := computeScores(r)
		if err != nil {
			platform.WriteError(w, err)
			return
		}
		if recorder != nil {
			record, recordErr := decisionRecordFromRequest(r, req, resp)
			if recordErr == nil {
				recordErr = recorder.RecordDecision(r.Context(), record)
			}
			if recordErr != nil {
				platform.WriteError(w, fmt.Errorf("record dispatch decision: %w", recordErr))
				return
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// computeScores decodes and validates the scoring request and runs the
// stateless engine, returning the request and the response (whose weights
// are the APPLIED weight set — defaults included when omitted).
func computeScores(r *http.Request) (scoreRequest, scoreResponse, error) {
	req, err := platform.DecodeJSON[scoreRequest](r, maxRequestBytes)
	if err != nil {
		return scoreRequest{}, scoreResponse{}, err
	}
	if req.JobID != nil && *req.JobID == uuid.Nil {
		return scoreRequest{}, scoreResponse{}, platform.ErrValidation(
			"job_id must be a non-zero UUID",
			platform.FieldError{Field: "job_id", Issue: "invalid"},
		)
	}

	weights := DefaultWeights()
	if req.Weights != nil {
		weights = *req.Weights
		if verr := weights.Validate(); verr != nil {
			return scoreRequest{}, scoreResponse{}, platform.ErrValidation(
				verr.Error(),
				platform.FieldError{Field: "weights", Issue: verr.Error()},
			)
		}
	}

	if len(req.Candidates) == 0 {
		return scoreRequest{}, scoreResponse{}, platform.ErrValidation(
			"at least one candidate is required",
			platform.FieldError{Field: "candidates", Issue: "must contain at least one candidate"},
		)
	}
	if len(req.Candidates) > maxCandidatesPerRequest {
		return scoreRequest{}, scoreResponse{}, platform.ErrValidation(
			fmt.Sprintf("at most %d candidates per request", maxCandidatesPerRequest),
			platform.FieldError{Field: "candidates", Issue: fmt.Sprintf("must contain at most %d candidates", maxCandidatesPerRequest)},
		)
	}

	fieldErrs := validateJobContext(req.JobContext)
	fieldErrs = append(fieldErrs, validateCandidates(req.Candidates)...)
	if len(fieldErrs) > 0 {
		return scoreRequest{}, scoreResponse{}, platform.ErrValidation("request contains invalid fields", fieldErrs...)
	}

	return req, scoreResponse{Weights: weights, Scores: ScoreCandidates(req.Candidates, req.JobContext, weights)}, nil
}

// decisionRecordFromRequest assembles the audited decision record: full
// attribution comes from the validated access-token claims (never from
// client input); the chosen technician is the engine's top-ranked score.
func decisionRecordFromRequest(r *http.Request, req scoreRequest, resp scoreResponse) (DecisionRecord, error) {
	claims, ok := platform.ClaimsFromContext(r.Context())
	if !ok {
		return DecisionRecord{}, platform.ErrUnauthorized("authentication required")
	}
	actor, err := uuid.Parse(claims.UserID)
	if err != nil {
		return DecisionRecord{}, platform.ErrUnauthorized("authenticated subject is not a user identifier")
	}
	var tenant *uuid.UUID
	if claims.TenantID != "" {
		parsed, parseErr := uuid.Parse(claims.TenantID)
		if parseErr != nil {
			return DecisionRecord{}, platform.ErrUnauthorized("authenticated tenant claim is not a tenant identifier")
		}
		tenant = &parsed
	}
	chosen := resp.Scores[0] // scores are sorted by total descending; candidates >= 1 is validated
	return DecisionRecord{
		DecisionID:         uuid.New(),
		ActorID:            actor,
		TenantID:           tenant,
		JobID:              req.JobID,
		InputsHash:         InputsHash(req.JobContext, req.Candidates, resp.Weights),
		ChosenTechnicianID: chosen.TechnicianID,
		ChosenScore:        chosen.Total,
		CandidateCount:     len(req.Candidates),
		Weights:            resp.Weights,
	}, nil
}

// handleFactors implements GET /v1/dispatch/factors: the static factor
// catalogue plus the shipped default weights.
func handleFactors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, factorCatalogueResponse{
		Factors:        FactorCatalogue(),
		DefaultWeights: DefaultWeights(),
	})
}

// validateJobContext checks the job context labels and list sizes.
func validateJobContext(j JobContext) []platform.FieldError {
	var errs []platform.FieldError
	errs = append(errs, validateStringList("job_context.required_skills", j.RequiredSkills)...)
	errs = append(errs, validateStringList("job_context.required_parts", j.RequiredParts)...)
	if len(j.VehicleMake) > maxStringEntryLength {
		errs = append(errs, platform.FieldError{
			Field: "job_context.vehicle_make",
			Issue: fmt.Sprintf("must be at most %d characters", maxStringEntryLength),
		})
	}
	return errs
}

// validateCandidates checks per-candidate ranges: non-nil technician id,
// non-negative distance/ETA/counters and rates within their documented
// bounds. Out-of-range values are rejected here rather than silently
// clamped so a broken producer cannot distort a recommendation.
func validateCandidates(candidates []Candidate) []platform.FieldError {
	var errs []platform.FieldError
	for i, c := range candidates {
		field := func(name string) string { return fmt.Sprintf("candidates[%d].%s", i, name) }

		if c.TechnicianID == uuid.Nil {
			errs = append(errs, platform.FieldError{Field: field("technician_id"), Issue: "must be a non-zero UUID"})
		}
		if c.DistanceKm < 0 {
			errs = append(errs, platform.FieldError{Field: field("distance_km"), Issue: "must be >= 0"})
		}
		if c.ETAMinutes < 0 {
			errs = append(errs, platform.FieldError{Field: field("eta_minutes"), Issue: "must be >= 0"})
		}
		if c.CompletedJobs < 0 {
			errs = append(errs, platform.FieldError{Field: field("completed_jobs"), Issue: "must be >= 0"})
		}
		if c.AcceptanceRate < 0 || c.AcceptanceRate > 1 {
			errs = append(errs, platform.FieldError{Field: field("acceptance_rate"), Issue: "must be within [0,1]"})
		}
		if c.CompletionRate < 0 || c.CompletionRate > 1 {
			errs = append(errs, platform.FieldError{Field: field("completion_rate"), Issue: "must be within [0,1]"})
		}
		if c.AvgCustomerRating < 0 || c.AvgCustomerRating > 5 {
			errs = append(errs, platform.FieldError{Field: field("avg_customer_rating"), Issue: "must be within [0,5]"})
		}
		errs = append(errs, validateStringList(field("skills"), c.Skills)...)
		errs = append(errs, validateStringList(field("vehicle_makes"), c.VehicleMakes)...)
	}
	return errs
}

// validateStringList enforces the list-size and label-length caps and
// rejects blank labels.
func validateStringList(field string, list []string) []platform.FieldError {
	var errs []platform.FieldError
	if len(list) > maxListEntries {
		errs = append(errs, platform.FieldError{
			Field: field,
			Issue: fmt.Sprintf("must contain at most %d entries", maxListEntries),
		})
	}
	for _, item := range list {
		if item == "" {
			errs = append(errs, platform.FieldError{Field: field, Issue: "entries must not be blank"})
			break
		}
		if len(item) > maxStringEntryLength {
			errs = append(errs, platform.FieldError{
				Field: field,
				Issue: fmt.Sprintf("entries must be at most %d characters", maxStringEntryLength),
			})
			break
		}
	}
	return errs
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
