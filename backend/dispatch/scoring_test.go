package dispatch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jobFixture is a fully-specified job context used across the tests.
func jobFixture() JobContext {
	return JobContext{
		RequiredSkills: []string{"engine", "brakes"},
		VehicleMake:    "Toyota",
		RequiredParts:  []string{"brake pads", "rotor"},
	}
}

// candidateFixture returns a perfect candidate for jobFixture: every skill,
// the right make, available, equipped, stocked and with a flawless record.
func candidateFixture(id string) Candidate {
	return Candidate{
		TechnicianID:      uuid.MustParse(id),
		DistanceKm:        4.2,
		ETAMinutes:        12,
		Skills:            []string{"engine", "brakes", "electrical"},
		VehicleMakes:      []string{"Toyota", "Nissan"},
		Available:         true,
		HasEquipment:      true,
		HasRequiredParts:  true,
		CompletedJobs:     214,
		AcceptanceRate:    0.92,
		CompletionRate:    0.98,
		AvgCustomerRating: 4.8,
	}
}

func factorByName(t *testing.T, s Score, name string) Factor {
	t.Helper()
	for _, f := range s.Factors {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("score has no factor named %q", name)
	return Factor{}
}

func TestDefaultWeightsSumToOne(t *testing.T) {
	w := DefaultWeights()
	require.NoError(t, w.Validate(), "shipped weights must validate")

	sum := w.TechnicalFit + w.Proximity + w.Availability + w.Equipment + w.Parts + w.Performance
	assert.InDelta(t, 1.0, sum, 1e-9, "default weights must sum to 1.0")

	for _, f := range FactorCatalogue() {
		assert.Equal(t, 6, len(FactorCatalogue()), "catalogue must list exactly six factors")
		assert.NotEmpty(t, f.Description, "catalogue factor %s needs a description", f.Name)
	}
}

func TestWeightsValidate(t *testing.T) {
	tests := []struct {
		name    string
		weights Weights
		wantErr bool
	}{
		{"default weights", DefaultWeights(), false},
		{"custom valid split", Weights{TechnicalFit: 0.5, Proximity: 0.5}, false},
		{"all zero", Weights{}, true},
		{"under sum", Weights{TechnicalFit: 0.4, Proximity: 0.4}, true},
		{"over sum", Weights{TechnicalFit: 1.2}, true},
		{"negative weight", Weights{TechnicalFit: 1.5, Proximity: -0.5}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.weights.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestScoreCandidateIsDeterministic(t *testing.T) {
	c := candidateFixture("11111111-1111-4111-8111-111111111111")
	c.DeclinedLast = true
	j := jobFixture()

	first := ScoreCandidate(c, j, DefaultWeights())
	for i := 0; i < 25; i++ {
		again := ScoreCandidate(c, j, DefaultWeights())
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("ScoreCandidate is not deterministic: run %d differs\nfirst: %+v\nagain: %+v", i, first, again)
		}
	}

	list := []Candidate{c, candidateFixture("22222222-2222-4222-8222-222222222222")}
	sortedFirst := ScoreCandidates(list, j, DefaultWeights())
	sortedAgain := ScoreCandidates(list, j, DefaultWeights())
	if !reflect.DeepEqual(sortedFirst, sortedAgain) {
		t.Fatalf("ScoreCandidates is not deterministic across runs")
	}
}

func TestFactorNormalizationBounds(t *testing.T) {
	// Property-style sweep over the extremes of every boolean and numeric
	// input; every normalized factor value and the total must stay in [0,1].
	rates := []float64{-1, 0, 0.37, 1, 5}
	etas := []int{-5, 0, 12, 30, 240}
	booleans := []bool{false, true}

	for _, eta := range etas {
		for _, available := range booleans {
			for _, equipment := range booleans {
				for _, parts := range booleans {
					for _, declined := range booleans {
						for _, rate := range rates {
							c := Candidate{
								TechnicianID:      uuid.MustParse("33333333-3333-4333-8333-333333333333"),
								DistanceKm:        rate * 10,
								ETAMinutes:        eta,
								Skills:            []string{"engine"},
								VehicleMakes:      []string{"Toyota"},
								Available:         available,
								HasEquipment:      equipment,
								HasRequiredParts:  parts,
								CompletedJobs:     10,
								AcceptanceRate:    rate,
								CompletionRate:    rate,
								AvgCustomerRating: rate,
								DeclinedLast:      declined,
							}
							s := ScoreCandidate(c, jobFixture(), DefaultWeights())
							assert.GreaterOrEqual(t, s.Total, 0.0, "total below zero for %+v", c)
							assert.LessOrEqual(t, s.Total, 1.0, "total above one for %+v", c)
							for _, f := range s.Factors {
								assert.GreaterOrEqual(t, f.Normalized, 0.0, "factor %s below zero for %+v", f.Name, c)
								assert.LessOrEqual(t, f.Normalized, 1.0, "factor %s above one for %+v", f.Name, c)
							}
						}
					}
				}
			}
		}
	}
}

func TestZeroSkillCandidate(t *testing.T) {
	c := candidateFixture("44444444-4444-4444-8444-444444444444")
	c.Skills = nil

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	technical := factorByName(t, s, FactorTechnicalFit)
	assert.Equal(t, 0.0, technical.Normalized, "no matching skills must zero the technical fit")
	assert.Equal(t, 0.0, technical.Raw)
	assert.Contains(t, technical.Reason, "0/2")
	assert.Contains(t, technical.Reason, "missing")

	// The job names no skills: the factor is neutral, not zero.
	emptyJob := JobContext{VehicleMake: "Toyota"}
	s = ScoreCandidate(c, emptyJob, DefaultWeights())
	technical = factorByName(t, s, FactorTechnicalFit)
	assert.Equal(t, 1.0, technical.Normalized)
	assert.Contains(t, technical.Reason, "no specific skills required")
}

func TestPerfectCandidateScoresHigh(t *testing.T) {
	c := candidateFixture("55555555-5555-4555-8555-555555555555")
	c.ETAMinutes = 0
	c.AcceptanceRate = 1
	c.CompletionRate = 1
	c.AvgCustomerRating = 5

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	assert.InDelta(t, 1.0, s.Total, 1e-9, "a perfect candidate must score 1.0")
	for _, f := range s.Factors {
		assert.InDelta(t, 1.0, f.Normalized, 1e-9, "factor %s must be 1.0 for a perfect candidate", f.Name)
	}
	require.Len(t, s.TopReasons, 3)
}

func TestProximityFormula(t *testing.T) {
	tests := []struct {
		name string
		eta  int
		want float64
	}{
		{"no wait", 0, 1.0},
		{"half-life", 30, 0.5},
		{"twice half-life", 90, 0.25},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := candidateFixture("66666666-6666-4666-8666-666666666666")
			c.ETAMinutes = tc.eta
			s := ScoreCandidate(c, jobFixture(), DefaultWeights())
			proximity := factorByName(t, s, FactorProximity)
			assert.InDelta(t, tc.want, proximity.Normalized, 1e-9)
			assert.Equal(t, float64(tc.eta), proximity.Raw, "raw carries the ETA in minutes")
		})
	}
}

func TestUnavailableCandidateHasZeroProximityAndAvailability(t *testing.T) {
	c := candidateFixture("77777777-7777-4777-8777-777777777777")
	c.Available = false
	c.ETAMinutes = 5

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	assert.Equal(t, 0.0, factorByName(t, s, FactorProximity).Normalized)
	assert.Equal(t, 0.0, factorByName(t, s, FactorAvailability).Normalized)
	proximity := factorByName(t, s, FactorProximity)
	assert.Contains(t, proximity.Reason, "unavailable")
}

func TestDeclinedLastPenalty(t *testing.T) {
	c := candidateFixture("88888888-8888-4888-8888-888888888888")
	c.DeclinedLast = true

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	availability := factorByName(t, s, FactorAvailability)
	assert.Equal(t, 0.5, availability.Normalized)
	assert.Contains(t, availability.Reason, "0.5 availability penalty")
}

func TestPartialPartsScoresHalf(t *testing.T) {
	partsJob := JobContext{RequiredParts: []string{"brake pads", "rotor"}}

	stocked := candidateFixture("99999999-9999-4999-8999-999999999999")
	s := ScoreCandidate(stocked, partsJob, DefaultWeights())
	parts := factorByName(t, s, FactorParts)
	assert.Equal(t, 1.0, parts.Normalized)
	assert.Contains(t, parts.Reason, "2 required parts")

	// Missing the full parts list is partial coverage: 0.5, with a reason.
	short := candidateFixture("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	short.HasRequiredParts = false
	s = ScoreCandidate(short, partsJob, DefaultWeights())
	parts = factorByName(t, s, FactorParts)
	assert.Equal(t, 0.5, parts.Normalized)
	assert.Equal(t, 0.0, parts.Raw, "raw carries the boolean has-all-parts signal")
	assert.Contains(t, parts.Reason, "missing required parts")

	// No parts required: neutral for everyone.
	noPartsJob := JobContext{RequiredSkills: []string{"engine"}}
	s = ScoreCandidate(short, noPartsJob, DefaultWeights())
	assert.Equal(t, 1.0, factorByName(t, s, FactorParts).Normalized)
}

func TestMakeMatchBonus(t *testing.T) {
	job := JobContext{RequiredSkills: []string{"engine"}, VehicleMake: "Toyota"}

	rated := candidateFixture("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	rated.Skills = []string{"engine"}
	s := ScoreCandidate(rated, job, DefaultWeights())
	assert.Equal(t, 1.0, factorByName(t, s, FactorTechnicalFit).Normalized)

	unrated := rated
	unrated.VehicleMakes = []string{"Ford"}
	s = ScoreCandidate(unrated, job, DefaultWeights())
	technical := factorByName(t, s, FactorTechnicalFit)
	assert.Equal(t, 0.5, technical.Normalized, "full skill overlap with make mismatch scores 0.5")
	assert.Contains(t, technical.Reason, "not rated on Toyota")
	assert.Contains(t, technical.Reason, "0.5 make bonus")

	// Matching is case-insensitive in both directions.
	lazy := rated
	lazy.Skills = []string{"ENGINE"}
	lazy.VehicleMakes = []string{"toyota"}
	s = ScoreCandidate(lazy, job, DefaultWeights())
	assert.Equal(t, 1.0, factorByName(t, s, FactorTechnicalFit).Normalized)
}

func TestPerformanceComposite(t *testing.T) {
	c := candidateFixture("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	c.AcceptanceRate = 0.90
	c.CompletionRate = 0.80
	c.AvgCustomerRating = 4.5
	c.CompletedJobs = 120

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	performance := factorByName(t, s, FactorPerformance)
	want := 0.4*0.80 + 0.4*(4.5/5.0) + 0.2*0.90
	assert.InDelta(t, want, performance.Normalized, 1e-9)
	assert.Contains(t, performance.Reason, "120 completed jobs")

	// Inputs outside their documented ranges are clamped, never propagated.
	extreme := c
	extreme.AcceptanceRate = 3
	extreme.CompletionRate = -2
	extreme.AvgCustomerRating = 99
	s = ScoreCandidate(extreme, jobFixture(), DefaultWeights())
	assert.LessOrEqual(t, factorByName(t, s, FactorPerformance).Normalized, 1.0)
}

func TestReasonsAreNonEmptyAndSpecific(t *testing.T) {
	c := candidateFixture("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	c.Skills = []string{"engine"}
	c.HasRequiredParts = false
	c.DeclinedLast = true

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	reasons := map[string]string{}
	for _, f := range s.Factors {
		require.NotEmpty(t, f.Reason, "factor %s needs a reason", f.Name)
		reasons[f.Name] = f.Reason
	}
	assert.Contains(t, reasons[FactorTechnicalFit], "1/2")
	assert.Contains(t, reasons[FactorTechnicalFit], "engine")
	assert.Contains(t, reasons[FactorTechnicalFit], "brakes")
	assert.Contains(t, reasons[FactorProximity], "ETA 12 min")
	assert.Contains(t, reasons[FactorProximity], "4.2 km")
	assert.Contains(t, reasons[FactorAvailability], "declined the last dispatch offer")
	assert.Contains(t, reasons[FactorParts], "brake pads")
	assert.Contains(t, reasons[FactorParts], "rotor")
	assert.Contains(t, reasons[FactorPerformance], "214 completed jobs")

	for _, reason := range s.TopReasons {
		require.NotEmpty(t, reason)
	}
}

func TestTotalIsWeightedSumOfFactors(t *testing.T) {
	c := candidateFixture("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	c.DeclinedLast = true
	c.HasRequiredParts = false

	s := ScoreCandidate(c, jobFixture(), DefaultWeights())
	sum := 0.0
	for _, f := range s.Factors {
		sum += f.Weight * f.Normalized
	}
	assert.InDelta(t, sum, s.Total, 1e-4, "total must equal the weighted factor sum, rounded to 4 decimals")
	assert.Equal(t, round4(sum), s.Total)
}

func TestScoreCandidatesSortedAndStableTies(t *testing.T) {
	j := jobFixture()
	// Three distinct scores plus an engineered tie (identical stats,
	// different IDs): the tie must resolve by ascending technician ID.
	slow := candidateFixture("11111111-1111-4111-8111-111111111111")
	slow.ETAMinutes = 120
	slow.CompletionRate = 0.5
	mid := candidateFixture("22222222-2222-4222-8222-222222222222")
	mid.ETAMinutes = 60
	tieLow := candidateFixture("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	tieHigh := candidateFixture("cccccccc-cccc-4ccc-8ccc-cccccccccccc")

	scores := ScoreCandidates([]Candidate{tieHigh, slow, tieLow, mid}, j, DefaultWeights())
	require.Len(t, scores, 4)
	for i := 1; i < len(scores); i++ {
		assert.GreaterOrEqual(t, scores[i-1].Total, scores[i].Total, "output must be sorted by total descending")
	}
	assert.Equal(t, tieLow.TechnicianID, scores[0].TechnicianID)
	assert.Equal(t, tieHigh.TechnicianID, scores[1].TechnicianID, "equal totals tie-break by ascending technician ID")

	again := ScoreCandidates([]Candidate{tieHigh, slow, tieLow, mid}, j, DefaultWeights())
	if !reflect.DeepEqual(scores, again) {
		t.Fatalf("ScoreCandidates ordering is not stable across runs")
	}
}

func TestTopReasons(t *testing.T) {
	c := candidateFixture("ffffffff-ffff-4fff-8fff-ffffffffffff")
	c.Skills = []string{"engine"}
	s := ScoreCandidate(c, jobFixture(), DefaultWeights())

	// contribution = weight x normalized; recompute the expected order.
	type contrib struct {
		name  string
		value float64
	}
	var expected []contrib
	for _, f := range s.Factors {
		expected = append(expected, contrib{f.Name, f.Weight * f.Normalized})
	}
	for i := 1; i < len(expected); i++ {
		for k := 0; k < i; k++ {
			if expected[k].value < expected[i].value {
				expected[k], expected[i] = expected[i], expected[k]
			}
		}
	}

	got := TopReasons(s, 2)
	require.Len(t, got, 2)
	for i := range got {
		assert.True(t, strings.HasPrefix(got[i], expected[i].name+":"),
			"top reason %d should start with %s, got %q", i, expected[i].name, got[i])
	}

	all := TopReasons(s, 10)
	require.LessOrEqual(t, len(all), 6)
	assert.Equal(t, TopReasons(s, len(s.Factors)), all, "asking for more than there is returns every qualifying reason")

	assert.Empty(t, TopReasons(s, 0))
	assert.Empty(t, TopReasons(s, -3))

	// Zero-contribution factors are never reasons: drop every weight but
	// proximity (scored 0 here via unavailability) and the list is empty.
	unavailable := c
	unavailable.Available = false
	proximityOnly := Weights{Proximity: 1.0}
	require.NoError(t, proximityOnly.Validate())
	s = ScoreCandidate(unavailable, jobFixture(), proximityOnly)
	assert.Empty(t, TopReasons(s, 5), "a zero-contribution factor must not surface as a reason")
}

func TestScoreIsNilSafeOnEmptyCandidates(t *testing.T) {
	scores := ScoreCandidates(nil, jobFixture(), DefaultWeights())
	assert.NotNil(t, scores)
	assert.Empty(t, scores)
}

func TestRealisticRankingNeverNearestOnly(t *testing.T) {
	// The directive: never nearest-only. A nearby but unqualified
	// technician must not outrank a slightly farther, fully qualified one.
	j := jobFixture()
	nearButWrong := candidateFixture("11111111-1111-4111-8111-111111111111")
	nearButWrong.ETAMinutes = 5
	nearButWrong.Skills = []string{"bodywork"}
	nearButWrong.VehicleMakes = []string{"Mazda"}
	nearButWrong.HasEquipment = false

	rightButFarther := candidateFixture("22222222-2222-4222-8222-222222222222")
	rightButFarther.ETAMinutes = 25

	scores := ScoreCandidates([]Candidate{nearButWrong, rightButFarther}, j, DefaultWeights())
	require.Len(t, scores, 2)
	assert.Equal(t, rightButFarther.TechnicianID, scores[0].TechnicianID,
		"qualified technician must outrank the merely nearest one")
	assert.Greater(t, scores[0].Total, scores[1].Total)
}
