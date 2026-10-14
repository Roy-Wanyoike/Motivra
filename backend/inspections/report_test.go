package inspections

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureForReport builds a sedan session with two findings (one corrected
// item), one evidence record and the resolved template — the minimal
// complete material for assembly math.
func fixtureForReport(t *testing.T) (Template, Session, []Finding, []Evidence) {
	t.Helper()
	inspector := uuid.New()
	vehicle := uuid.New()
	job := uuid.New()
	tenant := uuid.New()
	submitted := time.Now().UTC().Add(-time.Hour)

	template, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)

	sess := Session{
		ID:              uuid.New(),
		TenantID:        &tenant,
		VehicleID:       &vehicle,
		JobID:           &job,
		VehicleClass:    ClassSedan,
		TemplateID:      template.ID,
		TemplateName:    template.Name,
		TemplateVersion: template.Version,
		InspectorID:     inspector,
		Status:          StatusSubmitted,
		SubmittedAt:     &submitted,
	}

	firstPass := Finding{
		ID: uuid.New(), SessionID: sess.ID, ItemKey: "brakes",
		Result: ResultPass, Severity: SeverityGreen, Notes: "first read",
		RecordedBy: &inspector, CreatedAt: time.Now().UTC().Add(-30 * time.Minute),
	}
	correction := Finding{
		ID: uuid.New(), SessionID: sess.ID, ItemKey: "brakes",
		Result: ResultFail, Severity: SeverityAmber, Notes: "pads near limit",
		RecordedBy: &inspector, CreatedAt: time.Now().UTC().Add(-20 * time.Minute),
	}
	tyres := Finding{
		ID: uuid.New(), SessionID: sess.ID, ItemKey: "tyres",
		Result: ResultPass, Severity: SeverityGreen, Notes: "within spec",
		RecordedBy: &inspector, CreatedAt: time.Now().UTC().Add(-25 * time.Minute),
	}
	findings := []Finding{firstPass, correction, tyres}
	// Cover the remaining required items so the fixture satisfies every
	// report-integrity precondition.
	covered := map[string]bool{"brakes": true, "tyres": true}
	for _, key := range template.RequiredItems() {
		if covered[key] {
			continue
		}
		findings = append(findings, Finding{
			ID: uuid.New(), SessionID: sess.ID, ItemKey: key,
			Result: ResultPass, Severity: SeverityGreen, Notes: "within spec",
			RecordedBy: &inspector, CreatedAt: time.Now().UTC().Add(-26 * time.Minute),
		})
	}

	evidence := []Evidence{
		{
			ID: uuid.New(), SessionID: sess.ID, Kind: EvidenceKindPhoto,
			ContentHash: validHash("brake-photo"), CapturedAt: time.Now().UTC().Add(-28 * time.Minute),
			CapturedBy: &inspector, Uploaded: true,
		},
		{
			ID: uuid.New(), SessionID: sess.ID, Kind: EvidenceKindReading,
			ContentHash: validHash("tread-reading"), CapturedAt: time.Now().UTC().Add(-24 * time.Minute),
			CapturedBy: &inspector, FindingID: &correction.ID,
		},
	}
	return template, sess, findings, evidence
}

// TestSelectLatestFindingsNewestPerItemWins covers the corrections rule:
// the newest record per item key wins (created_at, then id as tiebreak),
// output is stable-sorted, and unselected records disappear.
func TestSelectLatestFindingsNewestPerItemWins(t *testing.T) {
	t.Parallel()
	_, _, findings, _ := fixtureForReport(t)

	selected := SelectLatestFindings(findings)
	require.Len(t, selected, 7, "one selection per template item; brakes collapsed to its newest record")

	byKey := map[string]Finding{}
	for _, f := range selected {
		byKey[f.ItemKey] = f
	}
	assert.Equal(t, ResultFail, byKey["brakes"].Result, "the corrected (newest) brakes record wins")
	assert.Equal(t, SeverityAmber, byKey["brakes"].Severity)
	assert.Equal(t, ResultPass, byKey["tyres"].Result)

	// Order stability: fully-shuffled input yields the identical selection.
	reversed := make([]Finding, len(findings))
	for i, f := range findings {
		reversed[len(findings)-1-i] = f
	}
	assert.Equal(t, selected, SelectLatestFindings(reversed))
}

// TestBuildReportInputIsDeterministicAndInjectableSafe pins that the same
// material always produces the same canonical string and hash, that notes
// containing canonical separators are hashed (cannot inject lines), and
// that different notes change the hash.
func TestBuildReportInputIsDeterministicAndInjectableSafe(t *testing.T) {
	t.Parallel()
	template, sess, findings, evidence := fixtureForReport(t)
	selected := SelectLatestFindings(findings)

	input1 := BuildReportInput(sess, selected, evidence, ReportVersionInitial)
	input2 := BuildReportInput(sess, selected, evidence, ReportVersionInitial)
	assert.Equal(t, input1, input2)
	assert.Equal(t, HashReportInput(input1), HashReportInput(input2))

	lines := strings.Split(input1, "\n")
	assert.Equal(t, reportHashVersion, lines[0])
	assert.True(t, strings.HasPrefix(lines[len(lines)-1], "findings=7,evidence=2"))

	// Separator injection: notes with \n and , are hashed, never inlined,
	// so they cannot forge an extra canonical line.
	tampered := make([]Finding, len(selected))
	for i, f := range selected {
		fCopy := f
		if f.ItemKey == "tyres" {
			fCopy.Notes = "typical\nfinding=brakes,FAIL,RED,0\n"
		}
		tampered[i] = fCopy
	}
	tamperedInput := BuildReportInput(sess, SelectLatestFindings(tampered), evidence, ReportVersionInitial)
	assert.NotEqual(t, input1, tamperedInput)
	assert.Equal(t, "findings=7,evidence=2", strings.Split(tamperedInput, "\n")[len(strings.Split(tamperedInput, "\n"))-1],
		"the injected text must not change the trailing counts line")
	assert.Equal(t, len(lines), len(strings.Split(tamperedInput, "\n")), "note text cannot add canonical lines")

	// Different template version changes the hash.
	other := sess
	other.TemplateVersion = template.Version + 1
	assert.NotEqual(t, HashReportInput(input1),
		HashReportInput(BuildReportInput(other, selected, evidence, ReportVersionInitial)))
}

// TestCheckReportReadinessRefusals covers every refused shape: no evidence
// at all, missing required finding, unknown item, RED finding without
// evidence — and the passing shape.
func TestCheckReportReadinessRefusals(t *testing.T) {
	t.Parallel()
	template, sess, findings, evidence := fixtureForReport(t)
	selected := SelectLatestFindings(findings)

	require.NoError(t, CheckReportReadiness(template, sess, findings, evidence),
		"the fixture satisfies every precondition")

	// No evidence at all.
	err := CheckReportReadiness(template, sess, findings, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "no evidence records"))

	// Drop the tyres finding: a required item becomes uncovered.
	withoutTyres := selected[:1]
	err = CheckReportReadiness(template, sess, withoutTyres, evidence)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), `"tyres" has no finding`))

	// Unknown item key.
	rogue := Finding{ID: uuid.New(), ItemKey: "jet_engine", Result: ResultPass, Severity: SeverityGreen}
	err = CheckReportReadiness(template, sess, append(append([]Finding{}, selected...), rogue), evidence)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), `not in template`))

	// RED finding without linked evidence (recorded later than the fixture
	// rows, so it becomes the selected engine_leaks record).
	red := Finding{ID: uuid.New(), ItemKey: "engine_leaks", Result: ResultFail, Severity: SeverityRed, CreatedAt: time.Now().UTC()}
	err = CheckReportReadiness(template, sess, append(append([]Finding{}, selected...), red), evidence)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "carries no evidence"))
}

// TestCheckReportReadinessEvaluatesOnlySelectedFindings pins that a
// superseded RED finding (corrected to GREEN) does not block assembly.
func TestCheckReportReadinessEvaluatesOnlySelectedFindings(t *testing.T) {
	t.Parallel()
	template, err := ActiveTemplateForClass(ClassSedan)
	require.NoError(t, err)
	inspector := uuid.New()
	sess := Session{
		ID: uuid.New(), VehicleClass: ClassSedan, TemplateID: template.ID,
		TemplateName: template.Name, TemplateVersion: template.Version,
		InspectorID: inspector, Status: StatusSubmitted,
	}

	redAt := time.Now().UTC().Add(-2 * time.Hour)
	greenAt := time.Now().UTC().Add(-time.Hour)
	findings := []Finding{
		{ID: uuid.New(), ItemKey: "brakes", Result: ResultFail, Severity: SeverityRed, CreatedAt: redAt, RecordedBy: &inspector},
		{ID: uuid.New(), ItemKey: "brakes", Result: ResultPass, Severity: SeverityGreen, CreatedAt: greenAt, RecordedBy: &inspector},
	}
	// Add findings for the other required items so coverage is complete.
	for _, key := range template.RequiredItems() {
		if key == "brakes" {
			continue
		}
		findings = append(findings, Finding{ID: uuid.New(), ItemKey: key, Result: ResultPass, Severity: SeverityGreen, CreatedAt: greenAt, RecordedBy: &inspector})
	}
	evidence := []Evidence{{ID: uuid.New(), SessionID: sess.ID, Kind: EvidenceKindPhoto, ContentHash: validHash("ev"), CapturedAt: redAt}}

	require.NoError(t, CheckReportReadiness(template, sess, findings, evidence),
		"the RED finding was superseded by a GREEN correction; only the selection is evaluated")
}

// TestVerifyReportDetectsTampering proves the verification math flags
// every tampering vector: edited finding, added finding, removed evidence,
// mutated session linkage, swapped template snapshot.
func TestVerifyReportDetectsTampering(t *testing.T) {
	t.Parallel()
	template, sess, findings, evidence := fixtureForReport(t)
	selected := SelectLatestFindings(findings)

	report := Report{
		ID:               uuid.New(),
		SessionID:        sess.ID,
		Version:          ReportVersionInitial,
		VehicleID:        sess.VehicleID,
		JobID:            sess.JobID,
		InspectorID:      sess.InspectorID,
		TemplateID:       sess.TemplateID,
		TemplateName:     sess.TemplateName,
		TemplateVersion:  sess.TemplateVersion,
		FindingsSnapshot: mustMarshalFindings(t, selected),
		EvidenceSnapshot: mustMarshalEvidence(t, evidence),
		FindingsCount:    len(selected),
		EvidenceCount:    len(evidence),
		ContentHash:      HashReportInput(BuildReportInput(sess, selected, evidence, ReportVersionInitial)),
	}

	// Baseline verifies.
	res := VerifyReport(template, sess, findings, evidence, report)
	require.True(t, res.Verified, "body: %+v", res)

	// Tamper 1: the live, selected finding's severity changed after
	// issuance (bypassing the append-only triggers).
	tamperedFindings := append([]Finding{}, findings...)
	tamperedFindings[1].Severity = SeverityRed // the latest brakes record is the selected one
	res = VerifyReport(template, sess, tamperedFindings, evidence, report)
	assert.False(t, res.Verified)
	assert.False(t, res.HashMatch)
	assert.False(t, res.FindingsMatch)

	// Tamper 1b: a superseded (non-selected) record changes — the frozen
	// selection is unaffected, so the report still verifies. This pins
	// that verification evaluates the selection, not the whole log.
	tamperedFindings = append([]Finding{}, findings...)
	tamperedFindings[0].Severity = SeverityRed
	res = VerifyReport(template, sess, tamperedFindings, evidence, report)
	assert.True(t, res.Verified, "a superseded record's edit cannot touch the frozen selection")

	// Tamper 2: an extra finding appeared (a record the report never
	// froze).
	extra := append(append([]Finding{}, findings...), Finding{ID: uuid.New(), ItemKey: "tyres", Result: ResultFail, Severity: SeverityRed, CreatedAt: time.Now()})
	res = VerifyReport(template, sess, extra, evidence, report)
	assert.False(t, res.Verified, "a late-recorded finding invalidates the frozen selection")

	// Tamper 3: an evidence record vanished.
	res = VerifyReport(template, sess, findings, evidence[:1], report)
	assert.False(t, res.Verified)
	assert.False(t, res.EvidenceMatch)
	assert.False(t, res.HashMatch)

	// Tamper 4: the session's linkage mutated after issuance.
	mutated := sess
	other := uuid.New()
	mutated.JobID = &other
	res = VerifyReport(template, mutated, findings, evidence, report)
	assert.False(t, res.Verified)
	assert.False(t, res.HashMatch)

	// Tamper 5: the template snapshot no longer resolves (registry
	// drift). The hash is unchanged — it covers the session's frozen
	// snapshot, not the live registry — but TemplateMatch refuses to
	// bless a report whose template was redefined.
	res = VerifyReport(template, sess, findings, evidence, report)
	require.True(t, res.Verified)
	otherTemplate := template
	otherTemplate.Version = template.Version + 1
	res = VerifyReport(otherTemplate, sess, findings, evidence, report)
	assert.False(t, res.Verified)
	assert.False(t, res.TemplateMatch)
	assert.True(t, res.HashMatch, "the canonical hash covers the session snapshot, which is unchanged")

	// Tamper 6: the stored hash itself was edited.
	edited := report
	edited.ContentHash = validHash("edited")
	res = VerifyReport(template, sess, findings, evidence, edited)
	assert.False(t, res.Verified)
	assert.False(t, res.HashMatch)
	assert.True(t, res.FindingsMatch && res.EvidenceMatch && res.TemplateMatch,
		"only the hash check may fail for a stored-hash edit")
}

func mustMarshalFindings(t *testing.T, findings []Finding) []byte {
	t.Helper()
	raw, err := marshalFindingsSnapshot(findings)
	require.NoError(t, err)
	return raw
}

func mustMarshalEvidence(t *testing.T, evidence []Evidence) []byte {
	t.Helper()
	raw, err := marshalEvidenceSnapshot(evidence)
	require.NoError(t, err)
	return raw
}
