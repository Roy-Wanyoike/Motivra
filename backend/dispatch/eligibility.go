package dispatch

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Verified-only eligibility (issue #167, graduate-mechanic pipeline
// Phase 3 / #126): before any scoring happens, candidates can be gated on
//
//  1. verification status — only technicians whose identity verification
//     status equals "verified" are eligible (verified_only);
//  2. skills — the technician's skills must intersect the job's required
//     skills (require_skills);
//  3. service area — the technician must serve the job's coarse
//     service-area label (require_service_area).
//
// Design decision (recorded here, in the README and in the decision
// audit): these are HARD GATES applied BEFORE scoring, not weighted
// factors. Issue #126 requires that unverified technicians receive no
// dispatched jobs; a weighted factor could still let an unverified
// technician top the ranking (and be recommended) when peers are scarce,
// so exclusion is the only mechanism that satisfies the requirement. The
// gates are individually optional so a caller can adopt them one at a
// time, and every verdict carries machine-readable facts plus
// human-readable reasons, so an operator can see WHY a technician was or
// was not eligible — explainable by construction, like the rest of the
// engine.
//
// Integration point (issue #166, parallel): the verification-status
// vocabulary is owned by the technician contract
// (contracts/technician/openapi.yaml — unverified | assessment_pending |
// verified). Dispatch does NOT re-validate that enum: any status other
// than the exact string "verified" — including values identity may add
// later, such as a rejected state — is ineligible. The gate therefore
// cannot break when #166 extends its lifecycle, and it fails closed when
// the status is missing entirely.

// VerificationStatusVerified is the only verification status that passes
// the verified-only gate. It must match the technician contract's
// VerificationState enum (contracts/technician/openapi.yaml, issue #166).
const VerificationStatusVerified = "verified"

// EligibilityOptions configures which eligibility gates run for one
// scoring call. Options are request-scoped: an omitted eligibility block
// keeps the pre-#167 behaviour (no gate, every candidate scored) so the
// change is additive for existing callers. Each flag opts one gate in.
type EligibilityOptions struct {
	// VerifiedOnly excludes every candidate whose verification status is
	// not exactly "verified" (issue #126: unverified technicians receive
	// no dispatched jobs).
	VerifiedOnly bool `json:"verified_only"`
	// RequireSkills excludes candidates whose skills do not intersect the
	// job's required skills. A job that names no skills imposes no
	// constraint (matching the technical-fit factor's semantics).
	RequireSkills bool `json:"require_skills"`
	// RequireServiceArea excludes candidates that do not serve the job's
	// coarse service-area label. A job with no service area imposes no
	// constraint.
	RequireServiceArea bool `json:"require_service_area"`
}

// active reports whether any gate is enabled.
func (o EligibilityOptions) active() bool {
	return o.VerifiedOnly || o.RequireSkills || o.RequireServiceArea
}

// EligibilityVerdict is the explainable eligibility outcome for one
// candidate. The boolean fields are the raw facts (independent of which
// gates ran); Reasons lists one explanation per ENABLED gate, in the
// fixed order verification → skills → area, so verdicts are
// byte-for-byte deterministic.
type EligibilityVerdict struct {
	TechnicianID uuid.UUID `json:"technician_id"`
	// Eligible is the combined verdict: true only when the candidate
	// passes every enabled gate. Excluded candidates are never scored.
	Eligible bool `json:"eligible"`
	// Verified is the raw fact: the status equals "verified".
	Verified bool `json:"verified"`
	// SkillsMatch is the raw fact: the candidate's skills intersect the
	// job's required skills (always true when the job names no skills).
	SkillsMatch bool `json:"skills_match"`
	// AreaMatch is the raw fact: the candidate serves the job's service
	// area (always true when the job names no area).
	AreaMatch bool `json:"area_match"`
	// Reasons explains the outcome per enabled gate. Never PII: labels
	// and statuses only.
	Reasons []string `json:"reasons"`
}

// EligibilitySummary is the response/audit shape of one gated scoring
// call: which gates ran, how many candidates were evaluated, and every
// per-candidate verdict in request order (candidate order is part of
// what was scored, mirroring the inputs hash).
type EligibilitySummary struct {
	VerifiedOnly       bool                 `json:"verified_only"`
	RequireSkills      bool                 `json:"require_skills"`
	RequireServiceArea bool                 `json:"require_service_area"`
	EvaluatedCount     int                  `json:"evaluated_count"`
	EligibleCount      int                  `json:"eligible_count"`
	ExcludedCount      int                  `json:"excluded_count"`
	Verdicts           []EligibilityVerdict `json:"verdicts"`
}

// EvaluateEligibility judges every candidate against the job under the
// given options. It is pure: same inputs, same verdicts, no clock, no
// randomness, no I/O. Excluded candidates are identified, never scored.
func EvaluateEligibility(candidates []Candidate, j JobContext, o EligibilityOptions) EligibilitySummary {
	verdicts := make([]EligibilityVerdict, 0, len(candidates))
	summary := EligibilitySummary{
		VerifiedOnly:       o.VerifiedOnly,
		RequireSkills:      o.RequireSkills,
		RequireServiceArea: o.RequireServiceArea,
		EvaluatedCount:     len(candidates),
		Verdicts:           verdicts,
	}
	for _, c := range candidates {
		v := evaluateCandidate(c, j, o)
		summary.Verdicts = append(summary.Verdicts, v)
		if v.Eligible {
			summary.EligibleCount++
		} else {
			summary.ExcludedCount++
		}
	}
	return summary
}

// evaluateCandidate produces one candidate's verdict: the raw facts plus
// one reason per enabled gate, in the fixed order verification → skills
// → area.
func evaluateCandidate(c Candidate, j JobContext, o EligibilityOptions) EligibilityVerdict {
	verified := isVerified(c)
	skillsMatch, skillsReason := skillsVerdict(c, j)
	areaMatch, areaReason := areaVerdict(c, j)

	var reasons []string
	eligible := true
	if o.VerifiedOnly {
		reasons = append(reasons, verificationReason(c, verified))
		eligible = eligible && verified
	}
	if o.RequireSkills {
		reasons = append(reasons, skillsReason)
		eligible = eligible && skillsMatch
	}
	if o.RequireServiceArea {
		reasons = append(reasons, areaReason)
		eligible = eligible && areaMatch
	}

	return EligibilityVerdict{
		TechnicianID: c.TechnicianID,
		Eligible:     eligible,
		Verified:     verified,
		SkillsMatch:  skillsMatch,
		AreaMatch:    areaMatch,
		Reasons:      reasons,
	}
}

// ScoreEligibleCandidates runs the eligibility gate first (issue #167)
// and scores only the candidates that pass, so an excluded technician is
// never scored-and-then-demoted — they simply receive no score. The
// returned scores keep ScoreCandidates' guarantees (sorted by total
// descending, ties broken by ascending technician ID); the summary
// carries the explainable verdicts for every evaluated candidate in
// request order.
func ScoreEligibleCandidates(candidates []Candidate, j JobContext, w Weights, o EligibilityOptions) ([]Score, EligibilitySummary) {
	summary := EvaluateEligibility(candidates, j, o)
	eligible := make([]Candidate, 0, summary.EligibleCount)
	for i, v := range summary.Verdicts {
		if v.Eligible {
			eligible = append(eligible, candidates[i])
		}
	}
	scores := ScoreCandidates(eligible, j, w)
	if scores == nil { // keep the wire shape an array, never null
		scores = []Score{}
	}
	return scores, summary
}

// isVerified reports whether the candidate's verification status is
// exactly "verified". An absent status is NOT verified: the gate fails
// closed (issue #126 — an unverified technician must never be
// dispatchable because a producer forgot the field).
func isVerified(c Candidate) bool {
	return c.VerificationStatus == VerificationStatusVerified
}

// verificationReason explains the verified-only verdict, distinguishing
// a supplied non-verified status from a missing one.
func verificationReason(c Candidate, verified bool) string {
	if verified {
		return fmt.Sprintf("verification status is %s", VerificationStatusVerified)
	}
	if c.VerificationStatus == "" {
		return "verification status not supplied; treated as not verified (fail-closed)"
	}
	return fmt.Sprintf("verification status %q is not verified", c.VerificationStatus)
}

// skillsVerdict evaluates the skills gate: the candidate's skills must
// intersect the job's required skills. A job that names no skills is no
// constraint (always a match), mirroring the technical-fit factor.
func skillsVerdict(c Candidate, j JobContext) (bool, string) {
	if len(j.RequiredSkills) == 0 {
		return true, "no specific skills required for this job"
	}
	matched, missing := splitSkills(j.RequiredSkills, c.Skills)
	if len(matched) == 0 {
		if len(c.Skills) == 0 {
			return false, fmt.Sprintf("technician holds no skills; job requires (%s)", strings.Join(j.RequiredSkills, ", "))
		}
		return false, fmt.Sprintf("no overlap with required skills (%s)", strings.Join(j.RequiredSkills, ", "))
	}
	reason := fmt.Sprintf("skills match job requirements (%s)", strings.Join(matched, ", "))
	if len(missing) > 0 {
		reason += fmt.Sprintf("; missing (%s)", strings.Join(missing, ", "))
	}
	return true, reason
}

// areaVerdict evaluates the service-area gate: the candidate must serve
// the job's coarse service-area label, case-insensitively. A job with no
// service area is no constraint. Labels are coarse areas per
// PII-HANDLING.md — never coordinates or location history.
func areaVerdict(c Candidate, j JobContext) (bool, string) {
	if j.ServiceArea == "" {
		return true, "no service area constraint on this job"
	}
	if len(c.ServiceAreas) == 0 {
		return false, fmt.Sprintf("technician declares no service areas; job requires %q", j.ServiceArea)
	}
	if containsFold(c.ServiceAreas, j.ServiceArea) {
		return true, fmt.Sprintf("serves job service area (%s)", j.ServiceArea)
	}
	return false, fmt.Sprintf("does not serve job service area (%s)", j.ServiceArea)
}
