// Package dispatch implements Motivra's explainable dispatch scoring engine
// (issue #19). The engine is a pure, stateless function: given a candidate
// set, a job context and a weight set it always produces the same scores, in
// the same order, with the same human-readable reasons. It never reads the
// clock, draws randomness or hides a penalty: every dimension is a named
// factor with a weight, a normalized value and a reason a dispatcher can
// read aloud.
package dispatch

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Factor names are stable machine identifiers, exposed by the factor
// catalogue (GET /v1/dispatch/factors) and echoed in every score.
const (
	FactorTechnicalFit = "technical_fit"
	FactorProximity    = "proximity"
	FactorAvailability = "availability"
	FactorEquipment    = "equipment"
	FactorParts        = "parts"
	FactorPerformance  = "performance"
)

const (
	// etaScaleMinutes is the half-life of the proximity curve: proximity is
	// 1/(1+ETA/30), so a 30 minute ETA scores 0.5 and a 0 minute ETA 1.0.
	etaScaleMinutes = 30.0

	// declinedLastPenalty halves the availability factor for a technician
	// who declined the previous offer. It is applied openly: the factor
	// carries a normalized value of 0.5 and a reason that says so.
	declinedLastPenalty = 0.5

	// makeMismatchBonus is the technical-fit credit kept when a technician
	// is not rated on the job's vehicle make. Skills still count; the make
	// gap halves the factor instead of zeroing it.
	makeMismatchBonus = 0.5

	// partsPartialCredit is the parts-factor value when a technician does
	// not carry the full required-parts list. With a single boolean the
	// engine cannot distinguish "none" from "some", so missing parts earn
	// partial credit rather than a hard zero; parts can be sourced en
	// route. A true zero requires per-part coverage data (future input).
	partsPartialCredit = 0.5

	// Performance sub-weights: completion rate, average customer rating
	// (of 5) and acceptance rate.
	performanceCompletionWeight = 0.4
	performanceRatingWeight     = 0.4
	performanceAcceptanceWeight = 0.2

	// ratingScale converts AvgCustomerRating (0-5) onto the 0-1 scale.
	ratingScale = 5.0

	// roundUnit rounds factor values and totals to 4 decimal places so the
	// engine output is byte-for-byte deterministic.
	roundUnit = 10000.0

	// defaultTopReasons is the number of reasons ScoreCandidate pre-fills
	// into Score.TopReasons; callers can re-slice with TopReasons.
	defaultTopReasons = 3
)

// Candidate is one technician under consideration for a job. All inputs are
// supplied by the caller (the dispatch console or a future jobs
// integration); the engine owns no storage and fetches nothing.
type Candidate struct {
	TechnicianID      uuid.UUID `json:"technician_id"`
	DistanceKm        float64   `json:"distance_km"`
	ETAMinutes        int       `json:"eta_minutes"`
	Skills            []string  `json:"skills,omitempty"`
	VehicleMakes      []string  `json:"vehicle_makes,omitempty"`
	Available         bool      `json:"available"`
	HasEquipment      bool      `json:"has_equipment"`
	HasRequiredParts  bool      `json:"has_required_parts"`
	CompletedJobs     int       `json:"completed_jobs"`
	AcceptanceRate    float64   `json:"acceptance_rate"`
	CompletionRate    float64   `json:"completion_rate"`
	AvgCustomerRating float64   `json:"avg_customer_rating"`
	DeclinedLast      bool      `json:"declined_last"`
}

// JobContext describes the job being staffed. Empty skill, make and part
// requirements are treated as "no constraint": they never penalize a
// candidate.
type JobContext struct {
	RequiredSkills []string `json:"required_skills"`
	VehicleMake    string   `json:"vehicle_make"`
	RequiredParts  []string `json:"required_parts"`
}

// Weights holds the per-factor weights. They must sum to 1.0 (see Validate)
// so a Score.Total stays on the 0-1 scale and remains comparable across
// jobs. Weight values are non-negative; a weight of 0 keeps the factor
// visible in the output but removes it from the total.
type Weights struct {
	TechnicalFit float64 `json:"technical_fit"`
	Proximity    float64 `json:"proximity"`
	Availability float64 `json:"availability"`
	Equipment    float64 `json:"equipment"`
	Parts        float64 `json:"parts"`
	Performance  float64 `json:"performance"`
}

// DefaultWeights is the shipped weight set: technical fit first (a wrong
// technician is worse than a late one), proximity second, then readiness
// (availability, equipment, parts) and track record. The values sum to 1.0.
func DefaultWeights() Weights {
	return Weights{
		TechnicalFit: 0.30,
		Proximity:    0.20,
		Availability: 0.15,
		Equipment:    0.15,
		Parts:        0.10,
		Performance:  0.10,
	}
}

// weightsSumTolerance is the allowed drift of the weight sum from 1.0,
// absorbing binary floating-point residue only.
const weightsSumTolerance = 1e-9

// Validate reports whether the weight set is usable: no negative weights
// and a sum of 1.0 (within floating-point tolerance).
func (w Weights) Validate() error {
	for _, p := range []struct {
		name  string
		value float64
	}{
		{"technical_fit", w.TechnicalFit},
		{"proximity", w.Proximity},
		{"availability", w.Availability},
		{"equipment", w.Equipment},
		{"parts", w.Parts},
		{"performance", w.Performance},
	} {
		if p.value < 0 {
			return fmt.Errorf("dispatch: weight %s must be non-negative (got %g)", p.name, p.value)
		}
	}
	sum := w.TechnicalFit + w.Proximity + w.Availability + w.Equipment + w.Parts + w.Performance
	if math.Abs(sum-1.0) > weightsSumTolerance {
		return fmt.Errorf("dispatch: weights must sum to 1.0 (got %g)", sum)
	}
	return nil
}

// Factor is one scored dimension. Raw is the un-normalized metric
// (for example the ETA in minutes, or the boolean readiness signals);
// Normalized is the 0-1 value actually weighted; Reason is a
// human-readable explanation that always names its inputs.
type Factor struct {
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	Raw        float64 `json:"raw"`
	Normalized float64 `json:"normalized"`
	Reason     string  `json:"reason"`
}

// Score is the explainable result for one candidate. TopReasons pre-fills
// the default top-3 factors by contribution (see TopReasons).
type Score struct {
	TechnicianID uuid.UUID `json:"technician_id"`
	Total        float64   `json:"total"`
	Factors      []Factor  `json:"factors"`
	TopReasons   []string  `json:"top_reasons"`
}

// FactorInfo is one entry of the static factor catalogue.
type FactorInfo struct {
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	DefaultWeight float64 `json:"default_weight"`
}

// FactorCatalogue returns every factor the engine scores, its semantics and
// its default weight. The catalogue is static: the engine has no hidden
// factors.
func FactorCatalogue() []FactorInfo {
	return []FactorInfo{
		{
			Name:          FactorTechnicalFit,
			Description:   "Skill overlap with the job's required skills, multiplied by the vehicle-make bonus (1.0 when rated on the make, 0.5 otherwise).",
			DefaultWeight: DefaultWeights().TechnicalFit,
		},
		{
			Name:          FactorProximity,
			Description:   "Closeness to the job, 1/(1+ETA/30); scored 0 when the technician is unavailable.",
			DefaultWeight: DefaultWeights().Proximity,
		},
		{
			Name:          FactorAvailability,
			Description:   "1 when available, 0.5 when the technician declined the last offer, 0 when unavailable.",
			DefaultWeight: DefaultWeights().Availability,
		},
		{
			Name:          FactorEquipment,
			Description:   "1 when the required tools are on the van, 0 otherwise.",
			DefaultWeight: DefaultWeights().Equipment,
		},
		{
			Name:          FactorParts,
			Description:   "1 when all required parts are on the van; 0.5 when parts are missing (partial coverage); neutral 1 when the job needs no parts.",
			DefaultWeight: DefaultWeights().Parts,
		},
		{
			Name:          FactorPerformance,
			Description:   "Weighted track record: 0.4 completion rate + 0.4 average customer rating (of 5) + 0.2 acceptance rate.",
			DefaultWeight: DefaultWeights().Performance,
		},
	}
}

// factorResult is the internal output of one factor scorer.
type factorResult struct {
	raw        float64
	normalized float64
	reason     string
}

// ScoreCandidate scores one candidate against a job under the given
// weights. It is deterministic: no randomness, no clock, no I/O. Callers
// should pass weights that satisfy Weights.Validate; the HTTP handler
// enforces this before scoring.
func ScoreCandidate(c Candidate, j JobContext, w Weights) Score {
	technical := scoreTechnicalFit(c, j)
	proximity := scoreProximity(c)
	availability := scoreAvailability(c)
	equipment := scoreEquipment(c)
	parts := scoreParts(c, j)
	performance := scorePerformance(c)

	factors := []Factor{
		{Name: FactorTechnicalFit, Weight: w.TechnicalFit, Raw: technical.raw, Normalized: technical.normalized, Reason: technical.reason},
		{Name: FactorProximity, Weight: w.Proximity, Raw: proximity.raw, Normalized: proximity.normalized, Reason: proximity.reason},
		{Name: FactorAvailability, Weight: w.Availability, Raw: availability.raw, Normalized: availability.normalized, Reason: availability.reason},
		{Name: FactorEquipment, Weight: w.Equipment, Raw: equipment.raw, Normalized: equipment.normalized, Reason: equipment.reason},
		{Name: FactorParts, Weight: w.Parts, Raw: parts.raw, Normalized: parts.normalized, Reason: parts.reason},
		{Name: FactorPerformance, Weight: w.Performance, Raw: performance.raw, Normalized: performance.normalized, Reason: performance.reason},
	}

	total := 0.0
	for _, f := range factors {
		total += f.Weight * f.Normalized
	}
	score := Score{
		TechnicianID: c.TechnicianID,
		Total:        round4(total),
		Factors:      factors,
	}
	score.TopReasons = TopReasons(score, defaultTopReasons)
	return score
}

// ScoreCandidates scores every candidate and returns the results sorted by
// Total descending, with ties broken by ascending TechnicianID so the order
// is fully deterministic even for equal scores.
func ScoreCandidates(candidates []Candidate, j JobContext, w Weights) []Score {
	scores := make([]Score, 0, len(candidates))
	for _, c := range candidates {
		scores = append(scores, ScoreCandidate(c, j, w))
	}
	sort.SliceStable(scores, func(i, k int) bool {
		if scores[i].Total != scores[k].Total {
			return scores[i].Total > scores[k].Total
		}
		return scores[i].TechnicianID.String() < scores[k].TechnicianID.String()
	})
	return scores
}

// TopReasons returns at most n factor explanations, ordered by contribution
// (weight x normalized value) descending, with ties broken by the fixed
// factor order. Factors with zero contribution are skipped: a factor that
// did not move the score is not a reason.
func TopReasons(s Score, n int) []string {
	if n <= 0 || len(s.Factors) == 0 {
		return nil
	}
	contribution := func(f Factor) float64 { return f.Weight * f.Normalized }
	order := make([]int, len(s.Factors))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		if contribution(s.Factors[order[a]]) != contribution(s.Factors[order[b]]) {
			return contribution(s.Factors[order[a]]) > contribution(s.Factors[order[b]])
		}
		return order[a] < order[b]
	})
	reasons := make([]string, 0, n)
	for _, i := range order {
		if len(reasons) == n {
			break
		}
		if contribution(s.Factors[i]) <= 0 {
			continue
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", s.Factors[i].Name, s.Factors[i].Reason))
	}
	return reasons
}

// scoreTechnicalFit computes the skill overlap ratio (matched required
// skills over required skills; 1.0 when the job names no skills) multiplied
// by the make-match bonus (1.0 when the technician is rated on the job's
// vehicle make or the job names no make, 0.5 otherwise).
func scoreTechnicalFit(c Candidate, j JobContext) factorResult {
	matched, missing := splitSkills(j.RequiredSkills, c.Skills)
	ratio := 1.0
	if len(j.RequiredSkills) > 0 {
		ratio = float64(len(matched)) / float64(len(j.RequiredSkills))
	}

	var bonus float64
	var makeNote string
	switch {
	case j.VehicleMake == "":
		bonus, makeNote = 1.0, "no vehicle make constraint"
	case ratedForMake(c.VehicleMakes, j.VehicleMake):
		bonus, makeNote = 1.0, fmt.Sprintf("rated on %s", j.VehicleMake)
	default:
		bonus, makeNote = makeMismatchBonus, fmt.Sprintf("not rated on %s; 0.5 make bonus applied", j.VehicleMake)
	}

	var reason strings.Builder
	if len(j.RequiredSkills) == 0 {
		reason.WriteString("no specific skills required for this job")
	} else {
		fmt.Fprintf(&reason, "matches %d/%d required skills", len(matched), len(j.RequiredSkills))
		if len(matched) > 0 {
			fmt.Fprintf(&reason, " (%s)", strings.Join(matched, ", "))
		}
		if len(missing) > 0 {
			fmt.Fprintf(&reason, "; missing (%s)", strings.Join(missing, ", "))
		}
	}
	fmt.Fprintf(&reason, "; %s", makeNote)

	return factorResult{raw: ratio, normalized: round4(clamp01(ratio * bonus)), reason: reason.String()}
}

// scoreProximity computes 1/(1+ETA/30), or 0 when the technician is
// unavailable. Distance is reported in the reason for transparency but the
// ETA drives the score: arrival time is what the customer experiences.
func scoreProximity(c Candidate) factorResult {
	eta := c.ETAMinutes
	if eta < 0 {
		eta = 0
	}
	if !c.Available {
		return factorResult{
			raw:        float64(eta),
			normalized: 0,
			reason:     fmt.Sprintf("technician unavailable; proximity scored 0 (ETA would be %d min)", eta),
		}
	}
	normalized := 1.0 / (1.0 + float64(eta)/etaScaleMinutes)
	return factorResult{
		raw:        float64(eta),
		normalized: round4(normalized),
		reason:     fmt.Sprintf("ETA %d min, %.1f km away; proximity %.2f", eta, c.DistanceKm, normalized),
	}
}

// scoreAvailability is 1 when available, 0.5 when the technician declined
// the last offer, and 0 when unavailable.
func scoreAvailability(c Candidate) factorResult {
	switch {
	case !c.Available:
		return factorResult{raw: 0, normalized: 0, reason: "technician is not available; availability scored 0"}
	case c.DeclinedLast:
		return factorResult{
			raw:        1,
			normalized: declinedLastPenalty,
			reason:     "technician is available but declined the last dispatch offer (0.5 availability penalty)",
		}
	default:
		return factorResult{raw: 1, normalized: 1, reason: "technician is available with no recent decline"}
	}
}

// scoreEquipment is 1 when the required tools are on the van, 0 otherwise.
func scoreEquipment(c Candidate) factorResult {
	if c.HasEquipment {
		return factorResult{raw: 1, normalized: 1, reason: "required tools and equipment are on the van"}
	}
	return factorResult{raw: 0, normalized: 0, reason: "missing required tools or equipment"}
}

// scoreParts is 1 when the job needs no parts or the van carries them all,
// and 0.5 when parts are missing (partial coverage, sourced en route).
func scoreParts(c Candidate, j JobContext) factorResult {
	switch {
	case len(j.RequiredParts) == 0:
		return factorResult{raw: 1, normalized: 1, reason: "no parts required for this job"}
	case c.HasRequiredParts:
		return factorResult{
			raw:        1,
			normalized: 1,
			reason:     fmt.Sprintf("has all %d required parts (%s)", len(j.RequiredParts), strings.Join(j.RequiredParts, ", ")),
		}
	default:
		return factorResult{
			raw:        0,
			normalized: partsPartialCredit,
			reason:     fmt.Sprintf("missing required parts (%s); partial parts coverage scored 0.5", strings.Join(j.RequiredParts, ", ")),
		}
	}
}

// scorePerformance is the weighted track record: 0.4 completion rate +
// 0.4 average rating (of 5) + 0.2 acceptance rate. Inputs are clamped to
// their documented ranges before composing so a malformed rate can never
// push the factor out of [0,1].
func scorePerformance(c Candidate) factorResult {
	completion := clamp01(c.CompletionRate)
	rating := math.Min(math.Max(c.AvgCustomerRating, 0), ratingScale) / ratingScale
	acceptance := clamp01(c.AcceptanceRate)
	composite := performanceCompletionWeight*completion +
		performanceRatingWeight*rating +
		performanceAcceptanceWeight*acceptance

	reason := fmt.Sprintf("completion %.0f%%, rating %.1f/5, acceptance %.0f%% over %d completed jobs",
		completion*100, c.AvgCustomerRating, acceptance*100, c.CompletedJobs)
	if c.CompletedJobs == 0 {
		reason += " (new technician, no history yet)"
	}
	return factorResult{raw: composite, normalized: round4(clamp01(composite)), reason: reason}
}

// splitSkills returns the required skills the candidate holds and the ones
// they are missing, both in the job's order and phrasing. Matching is
// case-insensitive.
func splitSkills(required, held []string) (matched, missing []string) {
	for _, skill := range required {
		if containsFold(held, skill) {
			matched = append(matched, skill)
			continue
		}
		missing = append(missing, skill)
	}
	return matched, missing
}

// ratedForMake reports whether the technician is rated on the make,
// case-insensitively. An empty make never matches.
func ratedForMake(makes []string, make string) bool {
	if make == "" {
		return false
	}
	return containsFold(makes, make)
}

func containsFold(list []string, needle string) bool {
	for _, item := range list {
		if strings.EqualFold(item, needle) {
			return true
		}
	}
	return false
}

// clamp01 confines x to [0,1]; the <= branch also normalizes negative zero.
func clamp01(x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	return x
}

// round4 rounds to 4 decimal places, the engine's output precision.
func round4(x float64) float64 {
	return math.Round(x*roundUnit) / roundUnit
}
