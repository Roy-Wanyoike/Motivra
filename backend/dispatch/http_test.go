package dispatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// testGate fakes the platform RBAC gate: requests carrying the expected
// X-Test-Role header pass, everyone else gets a 403.
func testGate(role string) func(http.HandlerFunc) http.HandlerFunc {
	return func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Role") != role {
				platform.WriteError(w, platform.ErrForbidden("insufficient role"))
				return
			}
			h(w, r)
		}
	}
}

func newTestRouter(role string) http.Handler {
	r := chi.NewRouter()
	Routes(r, testGate(role))
	return r
}

func scoreBody(candidates int, weights *Weights) string {
	cands := make([]Candidate, candidates)
	for i := range cands {
		cands[i] = Candidate{
			TechnicianID:      uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)),
			ETAMinutes:        10 + i,
			Skills:            []string{"engine"},
			Available:         true,
			HasEquipment:      true,
			HasRequiredParts:  true,
			AcceptanceRate:    0.9,
			CompletionRate:    0.9,
			AvgCustomerRating: 4.5,
		}
	}
	req := scoreRequest{
		JobContext: JobContext{RequiredSkills: []string{"engine"}, VehicleMake: "Toyota", RequiredParts: []string{"oil filter"}},
		Candidates: cands,
		Weights:    weights,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func doJSON(t *testing.T, handler http.Handler, method, path, role, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var parsed map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("response is not JSON: %v\nbody: %s", err, rec.Body.String())
		}
	}
	return rec, parsed
}

func TestScoreEndpointScoresSortedWithDefaults(t *testing.T) {
	handler := newTestRouter("DISPATCHER")
	rec, body := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", scoreBody(5, nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, body, "scores")

	rawScores, ok := body["scores"].([]any)
	require.True(t, ok)
	require.Len(t, rawScores, 5)

	var previous float64 = 1.1
	for _, raw := range rawScores {
		score, ok := raw.(map[string]any)
		require.True(t, ok)
		total := score["total"].(float64)
		assert.LessOrEqual(t, total, previous, "scores must be sorted descending")
		previous = total

		factors, ok := score["factors"].([]any)
		require.True(t, ok)
		assert.Len(t, factors, 6, "every score itemises all six factors")
		topReasons, ok := score["top_reasons"].([]any)
		require.True(t, ok)
		assert.NotEmpty(t, topReasons)
	}

	weights, ok := body["weights"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, DefaultWeights().TechnicalFit, weights["technical_fit"].(float64), 1e-9)
}

func TestScoreEndpointEchoesCustomWeights(t *testing.T) {
	handler := newTestRouter("ADMIN")
	custom := Weights{TechnicalFit: 0.4, Proximity: 0.3, Availability: 0.15, Equipment: 0.05, Parts: 0.05, Performance: 0.05}
	rec, body := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "ADMIN", scoreBody(2, &custom))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	weights := body["weights"].(map[string]any)
	assert.InDelta(t, 0.4, weights["technical_fit"].(float64), 1e-9)
}

func TestScoreEndpointRejectsBadWeights(t *testing.T) {
	handler := newTestRouter("DISPATCHER")
	bad := DefaultWeights()
	bad.Parts = 0.5 // sum exceeds 1.0

	rec, body := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", scoreBody(1, &bad))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "validation_failed", body["code"])

	negative := DefaultWeights()
	negative.Proximity = -0.1
	rec, _ = doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", scoreBody(1, &negative))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestScoreEndpointRejectsEmptyAndOversizedCandidateLists(t *testing.T) {
	handler := newTestRouter("DISPATCHER")

	rec, _ := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", scoreBody(0, nil))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec, _ = doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", scoreBody(maxCandidatesPerRequest+1, nil))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestScoreEndpointValidatesCandidateFields(t *testing.T) {
	handler := newTestRouter("DISPATCHER")

	raw, err := json.Marshal(scoreRequest{
		JobContext: JobContext{RequiredSkills: []string{"engine"}},
		Candidates: []Candidate{{
			TechnicianID:   uuid.Nil,
			ETAMinutes:     -3,
			AcceptanceRate: 1.5,
		}},
	})
	require.NoError(t, err)

	rec, body := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", string(raw))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["code"])
	fieldErrs, ok := body["errors"].([]any)
	require.True(t, ok, "field errors must be returned")
	assert.GreaterOrEqual(t, len(fieldErrs), 3, "technician id, eta and rate issues must all be reported")
}

func TestScoreEndpointRejectsWrongRole(t *testing.T) {
	handler := newTestRouter("DISPATCHER")
	rec, _ := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "MECHANIC", scoreBody(1, nil))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec, _ = doJSON(t, handler, http.MethodGet, "/v1/dispatch/factors", "", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestScoreEndpointRejectsInvalidBody(t *testing.T) {
	handler := newTestRouter("DISPATCHER")
	rec, body := doJSON(t, handler, http.MethodPost, "/v1/dispatch/score", "DISPATCHER", "{not json")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["code"])
}

func TestFactorsEndpoint(t *testing.T) {
	handler := newTestRouter("SUPER_ADMIN")
	rec, body := doJSON(t, handler, http.MethodGet, "/v1/dispatch/factors", "SUPER_ADMIN", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	factors, ok := body["factors"].([]any)
	require.True(t, ok)
	require.Len(t, factors, 6)

	names := make([]string, 0, len(factors))
	for _, raw := range factors {
		f := raw.(map[string]any)
		names = append(names, f["name"].(string))
		assert.NotEmpty(t, f["description"])
		assert.Greater(t, f["default_weight"].(float64), 0.0)
	}
	assert.ElementsMatch(t, []string{
		FactorTechnicalFit, FactorProximity, FactorAvailability,
		FactorEquipment, FactorParts, FactorPerformance,
	}, names)

	weights := body["default_weights"].(map[string]any)
	sum := 0.0
	for _, v := range weights {
		sum += v.(float64)
	}
	assert.InDelta(t, 1.0, sum, 1e-9)
}
