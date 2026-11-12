package identity

import (
        "encoding/json"
        "fmt"
        "net/http"
        "net/http/httptest"
        "strings"
        "testing"

        "github.com/google/uuid"
        "github.com/stretchr/testify/require"

        "github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newVerificationFixture registers one account per role it is handed and
// returns the handler chain plus the IDs and tokens of the principals the
// tests need: an operator (role+token), the technician under assessment and
// the full test API.
type verificationFixture struct {
        handler     http.Handler
        svc         *Service
        store       *fakeStore
        operatorID  uuid.UUID
        operatorTok string
        techID      uuid.UUID
        techTok     string
}

// newVerificationFixture wires the real middleware chain (as cmd/identity
// boots it) over the in-memory store, then creates an operator with the
// given role and a plain technician.
func newVerificationFixture(t *testing.T, operatorRole Role) *verificationFixture {
        t.Helper()
        svc, store := newTestService()
        validator := platform.NewJWTValidator(testSecret, testIssuer, testAudience)
        cfg := platform.LoadForTest()
        srv := platform.NewServer(cfg, platform.NewLogger(cfg.App), platform.WithMiddleware(platform.AuthMiddleware(validator)))
        Routes(srv.Router, svc, nil)

        registerAccount := func(email string, roles ...Role) (uuid.UUID, string) {
                rec := postJSON(t, srv, "/v1/auth/register",
                        fmt.Sprintf(`{"email":%q,"password":"strong-passphrase","full_name":"Tester"}`, email))
                require.Equal(t, http.StatusCreated, rec.Code, "register %s", email)
                var tokens AccessResponse
                require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tokens))
                user, err := store.GetUserByEmail(t.Context(), email)
                require.NoError(t, err)
                for _, role := range roles {
                        if role == RoleCustomer {
                                continue // registration already grants CUSTOMER
                        }
                        require.NoError(t, store.AssignRole(t.Context(), user.ID, role, nil))
                }
                user, err = store.GetUserByEmail(t.Context(), email)
                require.NoError(t, err)
                issued, err := newTestIssuer().Issue(t.Context(), svc.store, user, "console", "127.0.0.1")
                require.NoError(t, err)
                return user.ID, issued.AccessToken
        }

        operatorID, operatorTok := registerAccount("operator@verify.test", operatorRole)
        techID, techTok := registerAccount("tech@verify.test", RoleTechnician)
        return &verificationFixture{
                handler: srv, svc: svc, store: store,
                operatorID: operatorID, operatorTok: operatorTok,
                techID: techID, techTok: techTok,
        }
}

// approveBody renders an approve request body for the fixture's technician.
func approveBody(extra string) string {
        if extra == "" {
                return `{"reason":"passed the practical assessment"}`
        }
        return extra
}

// postAuthed sends a JSON POST with a Bearer token.
func postAuthed(t *testing.T, handler http.Handler, path, accessToken, body string) *httptest.ResponseRecorder {
        t.Helper()
        req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Authorization", "Bearer "+accessToken)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        return rec
}

// ---------------------------------------------------------------------------
// Service-level tests
// ---------------------------------------------------------------------------

// TestServiceApproveVerificationRecordsTrail proves the happy path of issue
// #166: an ADMIN approve appends one decision record, stamps the
// technician's status to verified, and writes one audit entry attributed to
// the acting staff principal — all observable through the fake store.
func TestServiceApproveVerificationRecordsTrail(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        rec, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin},
                fx.techID,
                VerificationDecision{
                        Status: VerificationStatusVerified,
                        Reason: "passed the practical assessment",
                })
        require.NoError(t, err)
        require.Equal(t, VerificationStatusVerified, rec.Status)
        require.Equal(t, fx.operatorID, rec.ActorID)

        user, err := fx.store.GetUser(t.Context(), fx.techID)
        require.NoError(t, err)
        require.Equal(t, VerificationStatusVerified, user.VerificationStatus)

        records := fx.store.technicianVerificationsFor(fx.techID)
        require.Len(t, records, 1, "exactly one append-only decision record")

        entries := fx.store.auditsByAction(ActionTechnicianVerificationApproved)
        require.Len(t, entries, 1)
        require.NotNil(t, entries[0].ActorID)
        require.Equal(t, fx.operatorID, *entries[0].ActorID)
        require.Equal(t, AuditObjectTypeTechnicianVerification, entries[0].ObjectType)
        require.Equal(t, rec.ID.String(), entries[0].ObjectID)
        require.Equal(t, fx.techID.String(), entries[0].Metadata["technician_id"])
        require.Equal(t, VerificationStatusVerified, entries[0].Metadata["status"])
        require.Nil(t, entries[0].Metadata["previous_status"], "never-assessed renders previous_status null")
        require.Equal(t, true, entries[0].Metadata["dispatch_eligible"])
}

// TestServiceRejectVerificationRequiresReason is table-driven over the
// decision validation rules: rejections need a reason, both decisions are
// length-capped, and only approve/reject are decidable.
func TestServiceRejectVerificationRequiresReason(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)
        longReason := strings.Repeat("r", MaxVerificationReasonLength+1)

        cases := []struct {
                name      string
                decision  VerificationDecision
                wantCode  int
                wantField string
        }{
                {
                        name:      "reject without reason",
                        decision:  VerificationDecision{Status: VerificationStatusRejected},
                        wantCode:  http.StatusUnprocessableEntity,
                        wantField: "reason",
                },
                {
                        name:      "reject with blank reason",
                        decision:  VerificationDecision{Status: VerificationStatusRejected, Reason: "   "},
                        wantCode:  http.StatusUnprocessableEntity,
                        wantField: "reason",
                },
                {
                        name:      "reason beyond the length cap",
                        decision:  VerificationDecision{Status: VerificationStatusRejected, Reason: longReason},
                        wantCode:  http.StatusUnprocessableEntity,
                        wantField: "reason",
                },
                {
                        name:      "approve is decidable without a reason",
                        decision:  VerificationDecision{Status: VerificationStatusVerified},
                        wantCode:  0,
                        wantField: "",
                },
                {
                        name:      "lifecycle states outside approve/reject are not decidable",
                        decision:  VerificationDecision{Status: VerificationStatusPending},
                        wantCode:  http.StatusUnprocessableEntity,
                        wantField: "status",
                },
                {
                        name:      "unknown lifecycle values are not decidable",
                        decision:  VerificationDecision{Status: "assessment_pending"},
                        wantCode:  http.StatusUnprocessableEntity,
                        wantField: "status",
                },
        }
        for _, tc := range cases {
                t.Run(tc.name, func(t *testing.T) {
                        _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID, tc.decision)
                        if tc.wantCode == 0 {
                                require.NoError(t, err)
                                return
                        }
                        perr := appErr(t, err)
                        require.Equal(t, tc.wantCode, perr.Status)
                        found := false
                        for _, fe := range perr.FieldErrors {
                                if fe.Field == tc.wantField {
                                        found = true
                                }
                        }
                        require.True(t, found, "expected a %s field error, got %+v", tc.wantField, perr.FieldErrors)
                })
        }
}

// TestServiceEvidenceReferenceValidation bounds the credential-evidence
// reference list: at most 20 handles, each 1-128 characters.
func TestServiceEvidenceReferenceValidation(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        refs := make([]string, MaxVerificationEvidenceRefs+1)
        for i := range refs {
                refs[i] = fmt.Sprintf("evidence-handle-%02d", i)
        }
        _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{Status: VerificationStatusVerified, EvidenceRefs: refs})
        require.Equal(t, http.StatusUnprocessableEntity, appErr(t, err).Status)

        _, err = fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{Status: VerificationStatusVerified, EvidenceRefs: []string{"   "}})
        require.Equal(t, http.StatusUnprocessableEntity, appErr(t, err).Status)

        _, err = fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{Status: VerificationStatusVerified, EvidenceRefs: []string{strings.Repeat("x", MaxVerificationRefLength+1)}})
        require.Equal(t, http.StatusUnprocessableEntity, appErr(t, err).Status)

        rec, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{
                        Status:       VerificationStatusVerified,
                        EvidenceRefs: []string{"inspection-evidence-1", "credential-scan-handle"},
                })
        require.NoError(t, err)
        require.Equal(t, []string{"inspection-evidence-1", "credential-scan-handle"}, rec.EvidenceRefs)
}

// TestServiceVerificationDenialsAudited is table-driven over every
// non-verifier role (the issue's negative test): the denial is 403, an
// identity.technician.verification.denied audit row lands with the actor
// and target, and NOTHING else changes — no record, no status stamp.
func TestServiceVerificationDenialsAudited(t *testing.T) {
        deniedRoles := []Role{
                RoleCustomer, RoleTechnician, RoleDispatcher, RoleGarage,
                RoleFleetAdmin, RoleSupport, RoleFinance,
        }
        for _, role := range deniedRoles {
                t.Run(string(role), func(t *testing.T) {
                        fx := newVerificationFixture(t, role)

                        for _, status := range []string{VerificationStatusVerified, VerificationStatusRejected} {
                                _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                                        VerificationActor{ID: fx.operatorID, Role: role}, fx.techID,
                                        VerificationDecision{Status: status, Reason: "self-recorded"})
                                require.Equal(t, http.StatusForbidden, appErr(t, err).Status, "status %s", status)
                        }

                        entries := fx.store.auditsByAction(ActionTechnicianVerificationDenied)
                        require.Len(t, entries, 2, "both refused attempts are audited")
                        for _, entry := range entries {
                                require.Equal(t, fx.techID.String(), entry.ObjectID)
                                require.Equal(t, VerificationStatusAuditObjectType, entry.ObjectType)
                                require.Equal(t, string(role), entry.Metadata["actor_role"])
                        }

                        require.Empty(t, fx.store.technicianVerificationsFor(fx.techID), "no decision record")
                        user, err := fx.store.GetUser(t.Context(), fx.techID)
                        require.NoError(t, err)
                        require.Empty(t, user.VerificationStatus, "no status stamp")
                })
        }
}

// TestServiceSelfVerificationDenied pins the separation-of-duties rule: an
// actor may never decide their own status, even with a verifier role.
func TestServiceSelfVerificationDenied(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.operatorID,
                VerificationDecision{Status: VerificationStatusVerified, Reason: "verifying myself"})
        require.Equal(t, http.StatusForbidden, appErr(t, err).Status)

        entries := fx.store.auditsByAction(ActionTechnicianVerificationDenied)
        require.Len(t, entries, 1)
        require.Equal(t, fx.operatorID.String(), entries[0].ObjectID)
        require.Empty(t, fx.store.technicianVerificationsFor(fx.operatorID))
}

// TestServiceVerificationTargetMustBeTechnician keeps the surface honest:
// unknown ids and accounts without the TECHNICIAN role read as 404
// "technician not found" — the endpoint manages technician verification
// only, and no account-state detail is disclosed.
func TestServiceVerificationTargetMustBeTechnician(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        // A second account that is neither the actor nor a technician.
        rec := postJSON(t, fx.handler, "/v1/auth/register",
                `{"email":"customer@verify.test","password":"strong-passphrase","full_name":"Customer"}`)
        require.Equal(t, http.StatusCreated, rec.Code)
        customer, err := fx.store.GetUserByEmail(t.Context(), "customer@verify.test")
        require.NoError(t, err)

        cases := []struct {
                name string
                id   uuid.UUID
        }{
                {name: "unknown id", id: uuid.New()},
                {name: "non-technician account", id: customer.ID},
        }
        for _, tc := range cases {
                t.Run(tc.name, func(t *testing.T) {
                        _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, tc.id,
                                VerificationDecision{Status: VerificationStatusVerified})
                        perr := appErr(t, err)
                        require.Equal(t, http.StatusNotFound, perr.Status)
                        require.Equal(t, "technician not found", perr.Detail)
                })
        }
}

// TestServiceVerificationTrailAppends proves the append-only semantics:
// approve → reject → approve each append a NEW record (never mutate), the
// current status always equals the newest record, and every change carries
// its own audit action.
func TestServiceVerificationTrailAppends(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)
        actor := VerificationActor{ID: fx.operatorID, Role: RoleAdmin}

        steps := []struct {
                status string
                action string
        }{
                {VerificationStatusVerified, ActionTechnicianVerificationApproved},
                {VerificationStatusRejected, ActionTechnicianVerificationRejected},
                {VerificationStatusVerified, ActionTechnicianVerificationApproved},
        }
        for i, step := range steps {
                rec, err := fx.svc.SetTechnicianVerificationStatus(t.Context(), actor, fx.techID,
                        VerificationDecision{Status: step.status, Reason: fmt.Sprintf("decision %d", i+1)})
                require.NoError(t, err)

                records := fx.store.technicianVerificationsFor(fx.techID)
                require.Len(t, records, i+1, "decision %d appends a new record", i+1)
                require.Equal(t, rec.ID, records[len(records)-1].ID)

                user, err := fx.store.GetUser(t.Context(), fx.techID)
                require.NoError(t, err)
                require.Equal(t, step.status, user.VerificationStatus)

                status, latest, err := fx.svc.TechnicianVerificationStatus(t.Context(), fx.techID)
                require.NoError(t, err)
                require.Equal(t, step.status, status, "current status follows the newest record")
                require.NotNil(t, latest)
                require.Equal(t, step.status, latest.Status)
        }

        require.Len(t, fx.store.auditsByAction(ActionTechnicianVerificationApproved), 2)
        require.Len(t, fx.store.auditsByAction(ActionTechnicianVerificationRejected), 1)
}

// TestServiceVerificationAuditMetadataRedacted pins the PII discipline:
// the decision record stores the staff-authored reason, while the audit
// metadata carries only the redacted projection (emails and phone numbers
// must not accumulate in the trail, docs/PII-HANDLING.md).
func TestServiceVerificationAuditMetadataRedacted(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        reason := "contacted grad.mech@example.com on +254700000123; passed rubric"
        rec, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{Status: VerificationStatusRejected, Reason: reason})
        require.NoError(t, err)
        require.Equal(t, reason, rec.Reason, "the record keeps the staff-authored reason")

        entry := fx.store.auditsByAction(ActionTechnicianVerificationRejected)[0]
        redacted := entry.Metadata["reason"].(string)
        require.NotContains(t, redacted, "grad.mech@example.com")
        require.NotContains(t, redacted, "+254700000123")
}

// TestServiceVerificationAuditFailureFailsRequest is the fail-closed seam
// test (issue #166: "audit write failure fails the request"): with the
// audit write failing, the decision is refused and NOTHING persists — no
// record, no status stamp, no audit row.
func TestServiceVerificationAuditFailureFailsRequest(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)
        fx.store.failAudit = true

        _, err := fx.svc.SetTechnicianVerificationStatus(t.Context(),
                VerificationActor{ID: fx.operatorID, Role: RoleAdmin}, fx.techID,
                VerificationDecision{Status: VerificationStatusVerified, Reason: "passed"})
        // The transactional seam failure renders as an opaque 500 (the cause
        // never leaks to the client); the assertions below prove nothing was
        // committed — the fail-closed guarantee is about atomicity.
        perr := appErr(t, err)
        require.Equal(t, http.StatusInternalServerError, perr.Status)

        require.Empty(t, fx.store.technicianVerificationsFor(fx.techID))
        user, err := fx.store.GetUser(t.Context(), fx.techID)
        require.NoError(t, err)
        require.Empty(t, user.VerificationStatus)
        require.Empty(t, fx.store.auditsByAction(ActionTechnicianVerificationApproved),
                "no verification audit row either")
}

// ---------------------------------------------------------------------------
// HTTP-level tests (real middleware chain, table-driven RBAC)
// ---------------------------------------------------------------------------

// TestTechnicianVerificationEndpointsRBAC drives the approve endpoint
// through the real middleware for every fixed role: only ADMIN and
// SUPER_ADMIN reach 201; every other authenticated role gets 403 WITH a
// denial audit row (the issue's negative requirement); unauthenticated
// requests get 401 with no audit noise.
func TestTechnicianVerificationEndpointsRBAC(t *testing.T) {
        type roleCase struct {
                role  Role
                token func(fx *verificationFixture) string
                want  int
        }
        cases := []roleCase{
                {RoleAdmin, func(fx *verificationFixture) string { return fx.operatorTok }, http.StatusCreated},
                {RoleSuperAdmin, func(fx *verificationFixture) string { return fx.operatorTok }, http.StatusCreated},
                {RoleTechnician, func(fx *verificationFixture) string { return fx.techTok }, http.StatusForbidden},
        }
        // The remaining staff roles reuse the same fixture shape via their own
        // operator token.
        for _, role := range []Role{RoleCustomer, RoleDispatcher, RoleGarage, RoleFleetAdmin, RoleSupport, RoleFinance} {
                cases = append(cases, roleCase{role, func(fx *verificationFixture) string { return fx.operatorTok }, http.StatusForbidden})
        }

        for _, tc := range cases {
                t.Run(string(tc.role), func(t *testing.T) {
                        fx := newVerificationFixture(t, tc.role)
                        path := fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID)
                        rec := postAuthed(t, fx.handler, path, tc.token(fx), approveBody(""))
                        require.Equal(t, tc.want, rec.Code, rec.Body.String())

                        if tc.want == http.StatusForbidden {
                                require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
                                entries := fx.store.auditsByAction(ActionTechnicianVerificationDenied)
                                require.Len(t, entries, 1, "refused attempt is audited")
                                require.Equal(t, fx.techID.String(), entries[0].ObjectID)
                                require.Empty(t, fx.store.technicianVerificationsFor(fx.techID))
                        }
                        if tc.want == http.StatusCreated {
                                require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
                                var record technicianVerificationResponse
                                require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &record))
                                require.Equal(t, VerificationStatusVerified, record.Status)
                                require.Equal(t, fx.techID, record.TechnicianID)
                                require.Equal(t, fx.operatorID, record.ActorID)
                        }
                })
        }

        // Unauthenticated: 401, and no audit row exists to attribute.
        fx := newVerificationFixture(t, RoleAdmin)
        path := fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID)
        rec := postAuthed(t, fx.handler, path, "", approveBody(""))
        require.Equal(t, http.StatusUnauthorized, rec.Code)
        require.Empty(t, fx.store.auditsByAction(ActionTechnicianVerificationDenied))
}

// TestTechnicianVerificationDecisionHTTP pins the HTTP-side validation and
// the reject endpoint's reason requirement, plus the record wire shape.
func TestTechnicianVerificationDecisionHTTP(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)
        templateID := uuid.New()
        sessionID := uuid.New()

        cases := []struct {
                name string
                path string
                body string
                want int
        }{
                {
                        name: "reject without reason",
                        path: fmt.Sprintf("/v1/admin/technicians/%s/verification/reject", fx.techID),
                        body: `{}`,
                        want: http.StatusUnprocessableEntity,
                },
                {
                        name: "malformed technician id",
                        path: "/v1/admin/technicians/not-a-uuid/verification/approve",
                        body: approveBody(""),
                        want: http.StatusUnprocessableEntity,
                },
                {
                        name: "malformed assessment template id",
                        path: fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID),
                        body: `{"reason":"ok","assessment_template_id":"nope"}`,
                        want: http.StatusUnprocessableEntity,
                },
                {
                        name: "malformed assessment session id",
                        path: fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID),
                        body: `{"reason":"ok","assessment_session_id":"nope"}`,
                        want: http.StatusUnprocessableEntity,
                },
                {
                        name: "unknown technician",
                        path: fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", uuid.New()),
                        body: approveBody(""),
                        want: http.StatusNotFound,
                },
                {
                        name: "reject with reason stores references",
                        path: fmt.Sprintf("/v1/admin/technicians/%s/verification/reject", fx.techID),
                        body: fmt.Sprintf(`{"reason":"rubric not met","assessment_template_id":%q,"assessment_session_id":%q,"evidence_refs":["handle-1"]}`, templateID, sessionID),
                        want: http.StatusCreated,
                },
        }
        for _, tc := range cases {
                t.Run(tc.name, func(t *testing.T) {
                        rec := postAuthed(t, fx.handler, tc.path, fx.operatorTok, tc.body)
                        require.Equal(t, tc.want, rec.Code, rec.Body.String())
                        if tc.want != http.StatusCreated {
                                require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
                                return
                        }
                        var record technicianVerificationResponse
                        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &record))
                        require.Equal(t, VerificationStatusRejected, record.Status)
                        require.Equal(t, "rubric not met", record.Reason)
                        require.NotNil(t, record.AssessmentTemplateID)
                        require.Equal(t, templateID, *record.AssessmentTemplateID)
                        require.NotNil(t, record.AssessmentSessionID)
                        require.Equal(t, sessionID, *record.AssessmentSessionID)
                        require.Equal(t, []string{"handle-1"}, record.EvidenceRefs)
                })
        }
}

// TestTechnicianVerificationStatusRead covers the technician's own read:
// unverified with nulls before any decision, the newest record after one,
// and only ever the caller's own row.
func TestTechnicianVerificationStatusRead(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        rec := getAuthed(t, fx.handler, "/v1/technician/verification-status", fx.techTok)
        require.Equal(t, http.StatusOK, rec.Code)
        var status verificationStatusResponse
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
        require.Equal(t, VerificationStatusUnverified, status.Status)
        require.Nil(t, status.UpdatedAt, "never-assessed renders updated_at null")
        require.Nil(t, status.Verification, "never-assessed renders verification null")
        require.Equal(t, fx.techID, status.TechnicianID)

        // Approve, then read again: the record projects with its contract keys.
        path := fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID)
        rec = postAuthed(t, fx.handler, path, fx.operatorTok, approveBody(""))
        require.Equal(t, http.StatusCreated, rec.Code)

        rec = getAuthed(t, fx.handler, "/v1/technician/verification-status", fx.techTok)
        require.Equal(t, http.StatusOK, rec.Code)
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
        require.Equal(t, VerificationStatusVerified, status.Status)
        require.NotNil(t, status.UpdatedAt)
        require.NotNil(t, status.Verification)
        require.Equal(t, VerificationStatusVerified, status.Verification.Status)

        // Contract pin: the exact wire keys of the status read model (issue #166
        // contract test requirement — handlers must match the published shape).
        var raw map[string]any
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
        for _, key := range []string{"technician_id", "status", "updated_at", "verification"} {
                require.Contains(t, raw, key)
        }
        recordShape, ok := raw["verification"].(map[string]any)
        require.True(t, ok)
        for _, key := range []string{
                "id", "technician_id", "status", "reason", "actor_id",
                "assessment_template_id", "assessment_session_id", "evidence_refs", "created_at",
        } {
                require.Contains(t, recordShape, key)
        }
}

// TestVerificationStatusProfileReadModel proves the status rides the
// technician profile read models: /v1/users/me (self) and
// /v1/admin/users (operator oversight, the "list pending verifications"
// read surface) expose verification_status — null before, enum after.
func TestVerificationStatusProfileReadModel(t *testing.T) {
        fx := newVerificationFixture(t, RoleAdmin)

        rec := getAuthed(t, fx.handler, "/v1/users/me", fx.techTok)
        require.Equal(t, http.StatusOK, rec.Code)
        var me userResponse
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
        require.Nil(t, me.VerificationStatus, "null while never assessed")

        // A raw key pin: the field is present (null), not omitted.
        var rawMe map[string]any
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rawMe))
        require.Contains(t, rawMe, "verification_status")

        path := fmt.Sprintf("/v1/admin/technicians/%s/verification/approve", fx.techID)
        rec = postAuthed(t, fx.handler, path, fx.operatorTok, approveBody(""))
        require.Equal(t, http.StatusCreated, rec.Code)

        rec = getAuthed(t, fx.handler, "/v1/users/me", fx.techTok)
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
        require.NotNil(t, me.VerificationStatus)
        require.Equal(t, VerificationStatusVerified, *me.VerificationStatus)

        rec = getAuthed(t, fx.handler, "/v1/admin/users", fx.operatorTok)
        require.Equal(t, http.StatusOK, rec.Code)
        var page userListResponse
        require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
        require.Len(t, page.Users, 2)
        for _, u := range page.Users {
                if u.ID == fx.techID {
                        require.NotNil(t, u.VerificationStatus)
                        require.Equal(t, VerificationStatusVerified, *u.VerificationStatus)
                } else {
                        require.Nil(t, u.VerificationStatus, "non-technicians stay null")
                }
        }
}
