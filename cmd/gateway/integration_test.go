package main

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
	"github.com/Roy-Wanyoike/Motivra/backend/vehicles"
)

// TestGatewayFullBootRoutingIntegration drives the EXACT production boot —
// every gatewayBootSteps step (config, database, the six per-domain
// migration chains, validator, routes) against a real PostgreSQL — then
// probes one route per /v1 prefix through the composed router, proving
// per-prefix routing reaches each REAL domain handler (issue #202
// acceptance). Skips without TEST_DATABASE_URL exactly like every
// pgtest-gated suite (issue #28, deferral 3), so `go test ./...` compiles
// and passes on machines without a database; local runs against
// `make dev` are the merge gate (docs/RUNBOOK.md §2a).
func TestGatewayFullBootRoutingIntegration(t *testing.T) {
	if pgtest.DatabaseURL() == "" {
		t.Skipf("%s is not set; skipping the gateway full-boot integration test — start the dev stack with `make dev`, then export %s='postgres://motivra:motivra@localhost:5432/motivra?sslmode=disable' (see docs/RUNBOOK.md)", pgtest.EnvDatabaseURL, pgtest.EnvDatabaseURL)
	}

	// Test-env config + the real database URL. The legacy HS256 migration
	// window (ADR-0007 flag) lets the test mint fixed-role tokens for every
	// domain's RBAC gate without staging users through the identity API; the
	// always-on suite pins the same gates.
	cfg := platform.LoadForTest()
	cfg.Database.URL = pgtest.DatabaseURL()
	cfg.JWT.AllowHS256 = true

	logger := platform.NewLogger(cfg.App)
	boot := platform.NewBoot(cfg, logger)
	w := newGatewayWiring(cfg, logger, nil)
	w.boot = boot
	w.srv = newGatewayServer(cfg, logger, boot, &w.validator, nil)

	boot.Wire(t.Context(), gatewayBootSteps(w)...)
	require.Eventually(t, func() bool {
		if boot.Ready() {
			return true
		}
		t.Logf("gateway not ready yet; blockers: %v", boot.PublicReasons())
		return false
	}, 120*time.Second, 250*time.Millisecond,
		"gateway boot (config, database, six migration chains, routes) must complete; blockers: %v", boot.PublicReasons())
	t.Cleanup(func() { _ = boot.RunShutdownHooks(t.Context()) })

	// Post-wiring health posture: liveness 200, readiness honest 200.
	wr := doGateway(t, w.srv, http.MethodGet, "/healthz", "", nil)
	require.Equal(t, http.StatusOK, wr.Code)
	wr = doGateway(t, w.srv, http.MethodGet, "/readyz", "", nil)
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())

	tenant := uuid.New()
	owner := uuid.New()
	tokenFor := func(role string, withTenant bool) string {
		if withTenant {
			return mintGatewayToken(t, uuid.NewString(), role, tenant.String())
		}
		return mintGatewayToken(t, owner.String(), role, "")
	}

	// --- identity: register + profile through the real store and issuer ----
	email := fmt.Sprintf("gateway-e2e-%s@example.com", uuid.NewString()[:8])
	wr = doGateway(t, w.srv, http.MethodPost, "/v1/auth/register", "", []byte(fmt.Sprintf(
		`{"email":%q,"phone":%q,"password":"strong-passphrase","full_name":"Gateway E2E","device_name":"e2e-runner"}`,
		email, uniquePhone(t))))
	require.Equal(t, http.StatusCreated, wr.Code, "body: %s", wr.Body.String())
	access, ok := decodeGatewayBody(t, wr)["access_token"].(string)
	require.True(t, ok, "register must return an access token: %s", wr.Body.String())

	wr = doGateway(t, w.srv, http.MethodGet, "/v1/users/me", access, nil)
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())

	// --- vehicles: registry write + read through the real store ------------
	vin := uniqueVIN(t)
	wr = doGateway(t, w.srv, http.MethodPost, "/v1/vehicles", tokenFor("GARAGE", true), []byte(fmt.Sprintf(
		`{"vin":%q,"make":"Honda","model":"Accord","year_of_manufacture":2020,"plate":%q,"color":"Silver"}`,
		vin, uniquePlate(t))))
	require.Equal(t, http.StatusCreated, wr.Code, "body: %s", wr.Body.String())

	// --- jobs: service request through the real store ----------------------
	wr = doGateway(t, w.srv, http.MethodPost, "/v1/requests", tokenFor("CUSTOMER", false), []byte(
		`{"description":"Engine overheats on the highway","location_lat":-1.286389,"location_lng":36.817223,"address_text":"Nairobi CBD"}`))
	require.Equal(t, http.StatusCreated, wr.Code, "body: %s", wr.Body.String())

	// --- garages: capacity write + read (rides the jobs process, #170) -----
	wr = doGateway(t, w.srv, http.MethodPut, "/v1/garage/capacity", tokenFor("GARAGE", true), []byte(
		`{"bays":4,"opening_time":"08:00","closing_time":"17:00","technician_slots":6}`))
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())
	wr = doGateway(t, w.srv, http.MethodGet, "/v1/garage/capacity", tokenFor("GARAGE", true), nil)
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())
	assert.Equal(t, float64(4), decodeGatewayBody(t, wr)["bays"])

	// --- payments: sandbox initiation through the real ledger --------------
	wr = doGateway(t, w.srv, http.MethodPost, "/v1/payments/intents", tokenFor("GARAGE", true), []byte(
		`{"amount_minor":25000,"currency":"KES","description":"gateway e2e"}`))
	require.Equal(t, http.StatusCreated, wr.Code, "body: %s", wr.Body.String())

	// --- inspections: template registry through the real store -------------
	wr = doGateway(t, w.srv, http.MethodGet, "/v1/inspections/templates", tokenFor("CUSTOMER", false), nil)
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())

	// --- dispatch: real scoring engine (stateless; audit record written) ---
	body := fmt.Sprintf(`{"job_context":{"required_skills":["engine"]},"candidates":[{"technician_id":%q,"eta_minutes":10,"skills":["engine"],"available":true,"has_equipment":true,"has_required_parts":true,"acceptance_rate":0.9,"completion_rate":0.9,"avg_customer_rating":4.5}]}`,
		uuid.New())
	wr = doGateway(t, w.srv, http.MethodPost, "/v1/dispatch/score", tokenFor("DISPATCHER", false), []byte(body))
	require.Equal(t, http.StatusOK, wr.Code, "body: %s", wr.Body.String())
	require.Contains(t, decodeGatewayBody(t, wr), "scores")

	// --- unknown /v1 prefix: the gateway 404 contract ----------------------
	wr = doGateway(t, w.srv, http.MethodGet, "/v1/definitely-not-a-domain", "", nil)
	require.Equal(t, http.StatusNotFound, wr.Code)
	require.Equal(t, "application/problem+json", wr.Header().Get("Content-Type"))
	assert.Equal(t, "not_found", decodeGatewayBody(t, wr)["code"])
}

// uniqueVIN builds a structurally valid, run-unique VIN: a fixed WMI/VDS
// template, a run-unique hex serial (UUID hex is 0-9a-f — never I/O/Q) and
// the NHTSA check digit computed in-fixture. The vehicle is then registered
// through the real route, so backend/vehicles.ValidateVIN has the final
// word on the fixture math.
func uniqueVIN(t *testing.T) string {
	t.Helper()
	serial := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:6]
	body := "1HGCM826" + "0" + "3A" + serial // 17 chars, check-digit slot at index 8
	weights := [17]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}
	charValues := map[byte]int{
		'A': 1, 'B': 2, 'C': 3, 'D': 4, 'E': 5, 'F': 6, 'G': 7, 'H': 8, 'J': 1,
		'K': 2, 'L': 3, 'M': 4, 'N': 5, 'P': 7, 'R': 9, 'S': 2, 'T': 3, 'U': 4,
		'V': 5, 'W': 6, 'X': 7, 'Y': 8, 'Z': 9,
	}
	sum := 0
	for i := 0; i < 17; i++ {
		c := body[i]
		v := int(c - '0')
		if c < '0' || c > '9' {
			v = charValues[c]
		}
		sum += v * weights[i]
	}
	check := byte('0' + sum%11)
	if sum%11 == 10 {
		check = 'X'
	}
	vin := body[:8] + string(check) + body[9:]
	_, err := vehicles.ValidateVIN(vin)
	require.NoError(t, err, "fixture must produce a valid VIN")
	return vin
}

// uniquePhone builds a run-unique E.164 number for the identity fixtures.
func uniquePhone(t *testing.T) string {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	require.NoError(t, err)
	return fmt.Sprintf("+2547%08d", n.Int64())
}

// uniquePlate builds a run-unique Kenyan-style plate for the registry
// fixtures (the registry refuses duplicate plates per tenant).
func uniquePlate(t *testing.T) string {
	t.Helper()
	serial := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	return "GA " + serial[:4] + "A"
}
