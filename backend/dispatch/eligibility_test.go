package dispatch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// Verified-only eligibility with skills + service-area matching (issue
// #167, Phase 3 of the graduate-mechanic pipeline #126).
//
// The tests below cover the acceptance matrix (verified/unverified ×
// skills match/no match × area match/no match), the #126 negative test
// (an unverified technician can never be the dispatched recommendation),
// the HTTP contract (gate before scoring, verdicts in the response) and
// the decision-audit integration (verdicts ride the decision record's
// metadata).

// eligID derives a deterministic technician id from a label.
func eligID(label string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("eligibility:"+label))
}

// eligCandidate builds a candidate with the eligibility inputs set.
func eligCandidate(label, status string, skills, areas []string) Candidate {
	return Candidate{
		TechnicianID:       eligID(label),
		ETAMinutes:         10,
		Skills:             skills,
		Available:          true,
		HasEquipment:       true,
		HasRequiredParts:   true,
		AcceptanceRate:     0.9,
		CompletionRate:     0.9,
		AvgCustomerRating:  4.5,
		VerificationStatus: status,
		ServiceAreas:       areas,
	}
}

// allGates is the full posture: every gate enabled.
var allGates = EligibilityOptions{VerifiedOnly: true, RequireSkills: true, RequireServiceArea: true}

// TestEvaluateEligibilityMatrix walks the acceptance matrix: verification
// (verified / unverified / pending / missing / unknown-future) × skills
// (match / partial / none / unconstrained) × service area (match / miss /
// none declared / unconstrained), including the gate-off behaviour.
func TestEvaluateEligibilityMatrix(t *testing.T) {
	job := func(skills []string, area string) JobContext {
		return JobContext{RequiredSkills: skills, ServiceArea: area}
	}

	tests := []struct {
		name          string
		options       EligibilityOptions
		candidate     Candidate
		job           JobContext
		wantEligible  bool
		wantVerified  bool
		wantSkills    bool
		wantArea      bool
		wantReasons   []string // exact reasons when non-nil, substrings otherwise
		reasonSubstrs []string
		wantNoReasons bool // gates all disabled: no reasons at all
	}{
		{
			name:         "verified with matching skills and area is eligible",
			options:      allGates,
			candidate:    eligCandidate("ok", "verified", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: true, wantVerified: true, wantSkills: true, wantArea: true,
			wantReasons: []string{
				"verification status is verified",
				"skills match job requirements (engine)",
				"serves job service area (westlands)",
			},
		},
		{
			name:         "unverified technician is excluded before scoring",
			options:      allGates,
			candidate:    eligCandidate("unv", "unverified", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: false, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{`verification status "unverified" is not verified`},
		},
		{
			name:         "assessment_pending technician is excluded",
			options:      allGates,
			candidate:    eligCandidate("pend", "assessment_pending", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: false, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{`verification status "assessment_pending" is not verified`},
		},
		{
			name:         "missing verification status fails closed",
			options:      allGates,
			candidate:    eligCandidate("nostatus", "", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: false, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{"verification status not supplied; treated as not verified (fail-closed)"},
		},
		{
			name:         "unknown future verification status fails closed",
			options:      allGates,
			candidate:    eligCandidate("future", "rejected", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: false, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{`verification status "rejected" is not verified`},
		},
		{
			name:         "verification match is case-sensitive to the contract value",
			options:      allGates,
			candidate:    eligCandidate("cased", "Verified", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: false, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{`verification status "Verified" is not verified`},
		},
		{
			name:         "skills with no overlap are excluded under require_skills",
			options:      allGates,
			candidate:    eligCandidate("noskill", "verified", []string{"tyres"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: true, wantSkills: false, wantArea: true,
			reasonSubstrs: []string{"no overlap with required skills (engine)"},
		},
		{
			name:         "candidate with no skills at all is excluded under require_skills",
			options:      allGates,
			candidate:    eligCandidate("noskills", "verified", nil, []string{"westlands"}),
			job:          job([]string{"engine", "brakes"}, "westlands"),
			wantEligible: false, wantVerified: true, wantSkills: false, wantArea: true,
			reasonSubstrs: []string{"technician holds no skills; job requires (engine, brakes)"},
		},
		{
			name:         "partial skills overlap passes the gate (intersection semantics)",
			options:      allGates,
			candidate:    eligCandidate("partial", "verified", []string{"engine", "electrical"}, []string{"westlands"}),
			job:          job([]string{"engine", "brakes"}, "westlands"),
			wantEligible: true, wantVerified: true, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{"skills match job requirements (engine)", "missing (brakes)"},
		},
		{
			name:         "job without required skills imposes no skills constraint",
			options:      allGates,
			candidate:    eligCandidate("nojobskills", "verified", nil, []string{"westlands"}),
			job:          job(nil, "westlands"),
			wantEligible: true, wantVerified: true, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{"no specific skills required for this job"},
		},
		{
			name:         "technician outside the job service area is excluded",
			options:      allGates,
			candidate:    eligCandidate("elsewhere", "verified", []string{"engine"}, []string{"kilimani"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: true, wantSkills: true, wantArea: false,
			reasonSubstrs: []string{`does not serve job service area (westlands)`},
		},
		{
			name:         "technician with no service areas is excluded when the job has one",
			options:      allGates,
			candidate:    eligCandidate("noareas", "verified", []string{"engine"}, nil),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: false, wantVerified: true, wantSkills: true, wantArea: false,
			reasonSubstrs: []string{`technician declares no service areas; job requires "westlands"`},
		},
		{
			name:         "job without a service area imposes no area constraint",
			options:      allGates,
			candidate:    eligCandidate("nojobarea", "verified", []string{"engine"}, nil),
			job:          job([]string{"engine"}, ""),
			wantEligible: true, wantVerified: true, wantSkills: true, wantArea: true,
			reasonSubstrs: []string{"no service area constraint on this job"},
		},
		{
			name:         "skills and area labels match case-insensitively",
			options:      allGates,
			candidate:    eligCandidate("casefold", "verified", []string{"ENGINE"}, []string{"WESTLANDS"}),
			job:          job([]string{"engine"}, "Westlands"),
			wantEligible: true, wantVerified: true, wantSkills: true, wantArea: true,
		},
		{
			name:         "disabled gates keep an unverified candidate eligible but report the fact",
			options:      EligibilityOptions{RequireSkills: true, RequireServiceArea: true},
			candidate:    eligCandidate("gateoff", "unverified", []string{"engine"}, []string{"westlands"}),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: true, wantVerified: false, wantSkills: true, wantArea: true,
		},
		{
			name:         "no gates at all: every candidate is eligible with no reasons",
			options:      EligibilityOptions{},
			candidate:    eligCandidate("ungated", "unverified", nil, nil),
			job:          job([]string{"engine"}, "westlands"),
			wantEligible: true, wantVerified: false, wantSkills: false, wantArea: false,
			wantNoReasons: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateEligibility([]Candidate{tc.candidate}, tc.job, tc.options)
			require.Len(t, got.Verdicts, 1)
			v := got.Verdicts[0]
			assert.Equal(t, tc.wantEligible, v.Eligible, "eligible verdict")
			assert.Equal(t, tc.wantVerified, v.Verified, "verified fact")
			assert.Equal(t, tc.wantSkills, v.SkillsMatch, "skills fact")
			assert.Equal(t, tc.wantArea, v.AreaMatch, "area fact")
			assert.Equal(t, tc.candidate.TechnicianID, v.TechnicianID)

			if tc.wantReasons != nil {
				assert.Equal(t, tc.wantReasons, v.Reasons, "reasons must be exact and in gate order")
			}
			if tc.wantNoReasons {
				assert.Empty(t, v.Reasons, "disabled gates produce no reasons")
			}
			for _, substr := range tc.reasonSubstrs {
				assert.Contains(t, strings.Join(v.Reasons, " | "), substr, "reasons must explain the verdict")
			}

			assert.Equal(t, 1, got.EvaluatedCount)
			if tc.wantEligible {
				assert.Equal(t, 1, got.EligibleCount)
				assert.Equal(t, 0, got.ExcludedCount)
			} else {
				assert.Equal(t, 0, got.EligibleCount)
				assert.Equal(t, 1, got.ExcludedCount)
			}
		})
	}
}

// TestEvaluateEligibilitySummaryCountsAndOrder pins the summary invariants:
// verdicts stay in request order (candidate order is part of what was
// scored) and the counts add up.
func TestEvaluateEligibilitySummaryCountsAndOrder(t *testing.T) {
	candidates := []Candidate{
		eligCandidate("a", "verified", []string{"engine"}, []string{"westlands"}),
		eligCandidate("b", "unverified", []string{"engine"}, []string{"westlands"}),
		eligCandidate("c", "verified", []string{"tyres"}, []string{"kilimani"}),
		eligCandidate("d", "verified", []string{"engine"}, []string{"westlands"}),
	}
	job := JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"}

	summary := EvaluateEligibility(candidates, job, allGates)
	assert.Equal(t, 4, summary.EvaluatedCount)
	assert.Equal(t, 2, summary.EligibleCount)
	assert.Equal(t, 2, summary.ExcludedCount)
	assert.Equal(t, allGates.VerifiedOnly, summary.VerifiedOnly)
	assert.Equal(t, allGates.RequireSkills, summary.RequireSkills)
	assert.Equal(t, allGates.RequireServiceArea, summary.RequireServiceArea)

	wantOrder := []uuid.UUID{eligID("a"), eligID("b"), eligID("c"), eligID("d")}
	gotOrder := make([]uuid.UUID, 0, len(summary.Verdicts))
	for _, v := range summary.Verdicts {
		gotOrder = append(gotOrder, v.TechnicianID)
	}
	assert.Equal(t, wantOrder, gotOrder, "verdicts must stay in request order")
	assert.False(t, summary.Verdicts[1].Eligible, "unverified candidate excluded")
	assert.False(t, summary.Verdicts[2].Eligible, "wrong-skills + wrong-area candidate excluded")
}

// TestScoreEligibleCandidatesExcludesBeforeScoring proves the core
// mechanism: excluded candidates receive NO score (never scored-and-then-
// demoted), while eligible candidates score exactly as they would without
// the gate.
func TestScoreEligibleCandidatesExcludesBeforeScoring(t *testing.T) {
	job := JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"}
	verified := eligCandidate("chosen", "verified", []string{"engine"}, []string{"westlands"})
	verified.ETAMinutes = 20 // eligible but slower
	unverified := eligCandidate("fast", "unverified", []string{"engine"}, []string{"westlands"})
	unverified.ETAMinutes = 1 // would win on proximity if it were scored
	candidates := []Candidate{verified, unverified}
	weights := DefaultWeights()

	scores, summary := ScoreEligibleCandidates(candidates, job, weights, allGates)

	require.Len(t, scores, 1, "only the eligible candidate is scored")
	assert.Equal(t, verified.TechnicianID, scores[0].TechnicianID)
	assert.Equal(t, 1, summary.EligibleCount)
	assert.Equal(t, 1, summary.ExcludedCount)

	// The gate must not change how an eligible candidate scores.
	ungated := ScoreCandidates([]Candidate{verified}, job, weights)
	require.Len(t, ungated, 1)
	assert.Equal(t, ungated[0].Total, scores[0].Total, "eligible candidate's total is untouched by the gate")
	assert.Equal(t, ungated[0].Factors, scores[0].Factors)
}

// TestUnverifiedTechnicianNeverChosen is the #126 negative test: an
// unverified technician — however perfect on paper — can never be the
// engine's recommendation, and a set with no eligible candidate yields no
// recommendation at all.
func TestUnverifiedTechnicianNeverChosen(t *testing.T) {
	job := JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"}

	superstar := eligCandidate("superstar", "unverified", []string{"engine"}, []string{"westlands"})
	superstar.ETAMinutes = 0
	superstar.CompletedJobs = 500
	superstar.AcceptanceRate = 1
	superstar.CompletionRate = 1
	superstar.AvgCustomerRating = 5
	mediocre := eligCandidate("mediocre", "verified", []string{"engine"}, []string{"westlands"})
	mediocre.ETAMinutes = 40
	mediocre.AvgCustomerRating = 3

	scores, summary := ScoreEligibleCandidates([]Candidate{superstar, mediocre}, job, DefaultWeights(), allGates)
	require.NotEmpty(t, scores)
	assert.Equal(t, mediocre.TechnicianID, scores[0].TechnicianID,
		"the recommendation must be the verified technician, never the unverified one")
	for _, s := range scores {
		assert.NotEqual(t, superstar.TechnicianID, s.TechnicianID,
			"an unverified technician must never appear in the scores")
	}
	assert.Equal(t, 1, summary.EligibleCount)

	// Nobody eligible: no recommendation is produced at all.
	nobody, summary := ScoreEligibleCandidates([]Candidate{superstar}, job, DefaultWeights(), allGates)
	assert.Empty(t, nobody)
	require.NotNil(t, nobody, "scores must serialize as an empty array, not null")
	assert.Equal(t, 0, summary.EligibleCount)
	assert.Equal(t, 1, summary.ExcludedCount)
}

// TestEvaluateEligibilityDeterministic pins the engine's determinism
// guarantee for the gate: identical inputs produce byte-identical
// summaries.
func TestEvaluateEligibilityDeterministic(t *testing.T) {
	candidates := []Candidate{
		eligCandidate("d1", "verified", []string{"engine"}, []string{"westlands"}),
		eligCandidate("d2", "unverified", nil, nil),
	}
	job := JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"}

	first, err := json.Marshal(EvaluateEligibility(candidates, job, allGates))
	require.NoError(t, err)
	second, err := json.Marshal(EvaluateEligibility(candidates, job, allGates))
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
	assert.Equal(t, string(first), string(second), "byte-for-byte identical output")
}

// eligibilityHTTPBody marshals a score request with the eligibility gate.
func eligibilityHTTPBody(t *testing.T, req scoreRequest) string {
	t.Helper()
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	return string(raw)
}

// TestScoreEndpointEligibilityGate runs the gate end-to-end through the
// HTTP handler: excluded candidates get verdicts with reasons, eligible
// ones get scores, and the summary counts line up.
func TestScoreEndpointEligibilityGate(t *testing.T) {
	handler := newTestRouter("DISPATCHER")

	verified := eligCandidate("http-verified", "verified", []string{"engine"}, []string{"westlands"})
	verified2 := eligCandidate("http-verified-2", "verified", []string{"engine", "brakes"}, []string{"WESTLANDS"})
	unverified := eligCandidate("http-unverified", "unverified", []string{"engine"}, []string{"westlands"})
	unverified.ETAMinutes = 1 // would win without the gate

	body := eligibilityHTTPBody(t, scoreRequest{
		JobContext:  JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"},
		Candidates:  []Candidate{verified, unverified, verified2},
		Eligibility: &allGates,
	})
	rec, parsed := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rawScores, ok := parsed["scores"].([]any)
	require.True(t, ok)
	require.Len(t, rawScores, 2, "only eligible candidates are scored")
	scoredIDs := map[string]bool{}
	for _, raw := range rawScores {
		scoredIDs[raw.(map[string]any)["technician_id"].(string)] = true
	}
	assert.True(t, scoredIDs[verified.TechnicianID.String()])
	assert.True(t, scoredIDs[verified2.TechnicianID.String()])
	assert.False(t, scoredIDs[unverified.TechnicianID.String()],
		"the unverified technician must never be scored (issue #126)")

	summary, ok := parsed["eligibility"].(map[string]any)
	require.True(t, ok, "the response must carry the eligibility summary")
	assert.Equal(t, float64(3), summary["evaluated_count"])
	assert.Equal(t, float64(2), summary["eligible_count"])
	assert.Equal(t, float64(1), summary["excluded_count"])
	assert.Equal(t, true, summary["verified_only"])

	verdicts, ok := summary["verdicts"].([]any)
	require.True(t, ok)
	require.Len(t, verdicts, 3)
	excludedReasons := ""
	for _, raw := range verdicts {
		v := raw.(map[string]any)
		if v["technician_id"].(string) == unverified.TechnicianID.String() {
			assert.Equal(t, false, v["eligible"])
			for _, r := range v["reasons"].([]any) {
				excludedReasons += r.(string) + " | "
			}
		}
	}
	assert.Contains(t, excludedReasons, "is not verified",
		"the verdict must explain WHY the technician was excluded")
}

// TestScoreEndpointWithoutEligibilityKeepsLegacyShape pins backward
// compatibility: a request without the eligibility block produces the
// pre-#167 response exactly (no eligibility key, every candidate scored,
// unverified statuses ignored).
func TestScoreEndpointWithoutEligibilityKeepsLegacyShape(t *testing.T) {
	handler := newTestRouter("DISPATCHER")
	legacy := scoreBody(2, nil)
	rec, parsed := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", legacy)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, parsed, "eligibility", "legacy responses must not gain the eligibility block")
	rawScores, ok := parsed["scores"].([]any)
	require.True(t, ok)
	assert.Len(t, rawScores, 2, "without the gate every candidate is scored")
}

// TestScoreEndpointEligibilityValidation covers the new input bounds:
// oversized verification statuses, oversized area labels and area-list
// caps are rejected with 422 field errors.
func TestScoreEndpointEligibilityValidation(t *testing.T) {
	handler := newTestRouter("DISPATCHER")

	tests := []struct {
		name       string
		candidate  Candidate
		jobService string
		wantField  string
	}{
		{
			name:      "verification status over 64 characters",
			candidate: eligCandidate("longstatus", strings.Repeat("x", maxStringEntryLength+1), []string{"engine"}, nil),
			wantField: "candidates[0].verification_status",
		},
		{
			name:       "service area over 120 characters",
			candidate:  eligCandidate("ok", "verified", []string{"engine"}, nil),
			jobService: strings.Repeat("y", maxServiceAreaLength+1),
			wantField:  "job_context.service_area",
		},
		{
			name: "service area label over 120 characters",
			candidate: Candidate{
				TechnicianID: eligID("longarea"),
				ETAMinutes:   5,
				Available:    true,
				ServiceAreas: []string{strings.Repeat("z", maxServiceAreaLength+1)},
			},
			wantField: "candidates[0].service_areas",
		},
		{
			name: "blank service area label",
			candidate: Candidate{
				TechnicianID: eligID("blankarea"),
				ETAMinutes:   5,
				Available:    true,
				ServiceAreas: []string{""},
			},
			wantField: "candidates[0].service_areas",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.candidate
			c.Available = true
			body := eligibilityHTTPBody(t, scoreRequest{
				JobContext:  JobContext{RequiredSkills: []string{"engine"}, ServiceArea: tc.jobService},
				Candidates:  []Candidate{c},
				Eligibility: &allGates,
			})
			rec, parsed := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Equal(t, "validation_failed", parsed["code"])
			fieldErrs, ok := parsed["errors"].([]any)
			require.True(t, ok)
			fields := make([]string, 0, len(fieldErrs))
			for _, raw := range fieldErrs {
				fields = append(fields, raw.(map[string]any)["field"].(string))
			}
			assert.Contains(t, fields, tc.wantField)
		})
	}

	// The service-areas list cap mirrors the other label lists (50).
	tooMany := eligCandidate("manyareas", "verified", []string{"engine"}, nil)
	for i := 0; i < maxListEntries+1; i++ {
		tooMany.ServiceAreas = append(tooMany.ServiceAreas, fmt.Sprintf("area-%d", i))
	}
	body := eligibilityHTTPBody(t, scoreRequest{
		JobContext:  JobContext{RequiredSkills: []string{"engine"}},
		Candidates:  []Candidate{tooMany},
		Eligibility: &allGates,
	})
	rec, parsed := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", parsed["code"])
}

// TestScoreEndpointDecisionRecordsEligibilityVerdicts proves the audit
// integration: a gated call persists the eligibility verdicts in the
// decision record, and the chosen technician is the top ELIGIBLE score.
func TestScoreEndpointDecisionRecordsEligibilityVerdicts(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)

	verified := eligCandidate("audit-verified", "verified", []string{"engine"}, []string{"westlands"})
	verified.ETAMinutes = 15
	unverified := eligCandidate("audit-unverified", "unverified", []string{"engine"}, []string{"westlands"})
	unverified.ETAMinutes = 1

	body := eligibilityHTTPBody(t, scoreRequest{
		JobContext:  JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"},
		Candidates:  []Candidate{verified, unverified},
		Eligibility: &allGates,
	})
	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp scoreResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Scores)

	require.Len(t, recorder.records, 1)
	record := recorder.records[0]
	require.NotNil(t, record.Eligibility, "gated calls must persist the eligibility summary")
	assert.Equal(t, allGates, EligibilityOptions{
		VerifiedOnly:       record.Eligibility.VerifiedOnly,
		RequireSkills:      record.Eligibility.RequireSkills,
		RequireServiceArea: record.Eligibility.RequireServiceArea,
	})
	assert.Equal(t, 2, record.Eligibility.EvaluatedCount)
	assert.Equal(t, 1, record.Eligibility.EligibleCount)
	assert.Equal(t, 1, record.Eligibility.ExcludedCount)
	require.Len(t, record.Eligibility.Verdicts, 2)

	// The chosen technician is the top ELIGIBLE score — never the excluded
	// unverified one, even though it would have scored highest.
	assert.Equal(t, resp.Scores[0].TechnicianID, record.ChosenTechnicianID)
	assert.Equal(t, verified.TechnicianID, record.ChosenTechnicianID)
	assert.NotEqual(t, unverified.TechnicianID, record.ChosenTechnicianID)
}

// TestScoreEndpointDecisionRecordsZeroEligible pins the nobody-eligible
// path: the request still succeeds (a decision "no recommendation" is a
// decision), the audit record is still written with the verdicts and no
// chosen technician.
func TestScoreEndpointDecisionRecordsZeroEligible(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)

	unverified := eligCandidate("only", "unverified", []string{"engine"}, []string{"westlands"})
	body := eligibilityHTTPBody(t, scoreRequest{
		JobContext:  JobContext{RequiredSkills: []string{"engine"}, ServiceArea: "westlands"},
		Candidates:  []Candidate{unverified},
		Eligibility: &allGates,
	})
	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"scores":[]`, "empty eligible set serializes as an empty array")

	require.Len(t, recorder.records, 1, "the decision is still audited")
	record := recorder.records[0]
	require.NotNil(t, record.Eligibility)
	assert.Equal(t, 0, record.Eligibility.EligibleCount)
	assert.Equal(t, uuid.Nil, record.ChosenTechnicianID, "no eligible candidate means no recommendation")
	assert.Equal(t, 0.0, record.ChosenScore)
}

// TestScoreEndpointLegacyDecisionHasNoEligibilityMetadata pins that
// ungated calls keep their audit metadata shape (no eligibility key), so
// existing audit consumers see no change.
func TestScoreEndpointLegacyDecisionHasNoEligibilityMetadata(t *testing.T) {
	recorder := &fakeDecisionRecorder{}
	h := newRecorderTestAPI(recorder)
	token := mintRecorderToken(t, uuid.New().String(), "", "DISPATCHER")
	rec := doBearer(t, h, http.MethodPost, "/v1/dispatch/score", token, decisionBody(""))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, recorder.records, 1)
	assert.Nil(t, recorder.records[0].Eligibility, "ungated calls record no eligibility summary")
}

// TestPostgresDecisionRecorderPersistsEligibilityVerdicts (PG-gated)
// proves the eligibility metadata round-trips through the real audit_log
// insert as JSONB, next to the existing decision fields.
func TestPostgresDecisionRecorderPersistsEligibilityVerdicts(t *testing.T) {
	pool := pgtest.Pool(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(t.Context(), `DROP TABLE IF EXISTS audit_log CASCADE; DROP FUNCTION IF EXISTS prevent_audit_log_mutation();`)
	})
	_, err := pool.Exec(t.Context(), auditLogDDL)
	require.NoError(t, err)

	recorder := NewPostgresDecisionRecorder(pool)
	excluded := eligID("pg-excluded")
	record := DecisionRecord{
		DecisionID:         uuid.New(),
		ActorID:            uuid.New(),
		InputsHash:         "deadbeef02",
		ChosenTechnicianID: eligID("pg-chosen"),
		ChosenScore:        0.5,
		CandidateCount:     2,
		Weights:            DefaultWeights(),
		Eligibility: &EligibilitySummary{
			VerifiedOnly:       true,
			RequireSkills:      true,
			RequireServiceArea: false,
			EvaluatedCount:     2,
			EligibleCount:      1,
			ExcludedCount:      1,
			Verdicts: []EligibilityVerdict{
				{
					TechnicianID: eligID("pg-chosen"),
					Eligible:     true,
					Verified:     true,
					SkillsMatch:  true,
					AreaMatch:    true,
					Reasons:      []string{"verification status is verified", "skills match job requirements (engine)"},
				},
				{
					TechnicianID: excluded,
					Eligible:     false,
					Verified:     false,
					SkillsMatch:  true,
					AreaMatch:    true,
					Reasons:      []string{`verification status "unverified" is not verified`},
				},
			},
		},
	}
	require.NoError(t, recorder.RecordDecision(t.Context(), record))

	var metadata map[string]any
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT metadata FROM audit_log WHERE object_id = $1`, record.DecisionID.String(),
	).Scan(&metadata))

	raw, err := json.Marshal(metadata["eligibility"])
	require.NoError(t, err, "the eligibility summary must persist inside the audit metadata")
	var got EligibilitySummary
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, *record.Eligibility, got, "verdicts round-trip losslessly")
}

// BenchmarkEligibilityGate measures the gate's cost at the request cap
// (200 candidates, ADR-0005 dispatch decision latency p95 < 1.5 s): the
// gate adds a single linear pass over the candidate set — microseconds,
// not a regression against the budget.
func BenchmarkEligibilityGate(b *testing.B) {
	candidates := make([]Candidate, maxCandidatesPerRequest)
	for i := range candidates {
		candidates[i] = eligCandidate(fmt.Sprintf("bench-%d", i), "verified",
			[]string{"engine", "brakes", "electrical"}, []string{"westlands", "kilimani"})
		candidates[i].CompletedJobs = 100
		candidates[i].AcceptanceRate = 0.9
		candidates[i].CompletionRate = 0.9
		candidates[i].AvgCustomerRating = 4.5
	}
	job := JobContext{
		RequiredSkills: []string{"engine", "brakes"},
		VehicleMake:    "Toyota",
		RequiredParts:  []string{"oil filter"},
		ServiceArea:    "westlands",
	}
	weights := DefaultWeights()

	b.Run("ungated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ScoreCandidates(candidates, job, weights)
		}
	})
	b.Run("gated", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ScoreEligibleCandidates(candidates, job, weights, allGates)
		}
	})
}

// TestEligibilityGatePerformanceSanity guards the dispatch-path SLO
// (ADR-0005: dispatch decision latency p95 < 1.5 s) with a functional
// assertion: the gate is linear in candidates and labels and bounded by
// the request caps. The benchmark below measures the constant.
func TestEligibilityGatePerformanceSanity(t *testing.T) {
	if testing.Short() {
		t.Skip("performance sanity skipped in -short mode")
	}
	candidates := make([]Candidate, maxCandidatesPerRequest)
	for i := range candidates {
		candidates[i] = eligCandidate(fmt.Sprintf("perf-%d", i), "verified",
			[]string{"engine", "brakes", "electrical"}, []string{"westlands", "kilimani"})
	}
	job := JobContext{RequiredSkills: []string{"engine", "brakes"}, ServiceArea: "westlands"}

	// A full-cap request must stay orders of magnitude inside the 1.5 s
	// budget; a 10 s ceiling would already be absurd, 100 ms is generous
	// for 200 candidates on any CI runner.
	scores, summary := ScoreEligibleCandidates(candidates, job, DefaultWeights(), allGates)
	assert.Len(t, scores, maxCandidatesPerRequest)
	assert.Equal(t, maxCandidatesPerRequest, summary.EligibleCount)
}
