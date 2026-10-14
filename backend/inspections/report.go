package inspections

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Report assembly and verification math. Everything in this file is pure:
// the same session, findings and evidence always produce the same hash,
// which is what makes the report verifiable after the fact.
//
// Canonical report input (the exact string hashed):
//
//	line  1: motivra-inspections-report-v1
//	line  2: session_id=<uuid>
//	line  3: version=<decimal>
//	line  4: vehicle_id=<uuid or ->
//	line  5: job_id=<uuid or ->
//	line  6: template_id=<uuid>
//	line  7: template_name=<name>
//	line  8: template_version=<decimal>
//	line  9: inspector_id=<uuid>
//	line 10: submitted_at=<RFC3339Nano, UTC>
//	next n : finding=<id>,<item_key>,<result>,<severity>,<sha256(notes) hex>,<recorded_by uuid or ->
//	         one line per selected finding, ordered by item_key, id
//	next m : evidence=<id>,<kind>,<content_hash>,<finding_id uuid or ->,<uploaded 0|1>
//	         one line per evidence record, ordered by id
//	last  : findings=<n>,evidence=<m>
//
// All lines are joined with "\n" (no trailing newline) and hashed with
// SHA-256; ContentHash is the lowercase hex digest. Notes are hashed, not
// inlined, so free text cannot inject separators into the canonical form.
const reportHashVersion = "motivra-inspections-report-v1"

// ReportVersionInitial is the version of the (single) report a session can
// issue: the state machine moves the session to REPORT_AVAILABLE on
// issuance, so a second report for the same session is impossible; the
// store independently refuses a version mismatch (defense in depth).
const ReportVersionInitial = 1

// ErrReportIncomplete is returned when a session cannot be submitted or a
// report cannot be assembled because the integrity preconditions are not
// met (missing evidence, uncovered required items, RED findings without
// evidence linkage). The HTTP layer translates it into a 422.
var ErrReportIncomplete = errors.New("inspections: session does not satisfy report integrity preconditions")

// hashOfNotes returns the hex SHA-256 of a finding's notes.
func hashOfNotes(notes string) string {
	sum := sha256.Sum256([]byte(notes))
	return hex.EncodeToString(sum[:])
}

// uuidOrDash renders an optional uuid for the canonical form.
func uuidOrDash(id *uuid.UUID) string {
	if id == nil || *id == uuid.Nil {
		return "-"
	}
	return id.String()
}

// SelectLatestFindings reduces a finding list to the selected set used by
// report assembly: the newest record per template item key (created_at,
// then id as the tiebreak). Corrections are new records; this is where the
// newest one wins. The output is sorted by (item_key, id) so the canonical
// form is order-stable.
func SelectLatestFindings(findings []Finding) []Finding {
	latest := make(map[string]Finding, len(findings))
	for _, f := range findings {
		cur, ok := latest[f.ItemKey]
		if !ok || f.CreatedAt.After(cur.CreatedAt) || (f.CreatedAt.Equal(cur.CreatedAt) && f.ID.String() > cur.ID.String()) {
			latest[f.ItemKey] = f
		}
	}
	out := make([]Finding, 0, len(latest))
	for _, f := range findings {
		if sel, ok := latest[f.ItemKey]; ok && sel.ID == f.ID {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ItemKey != out[j].ItemKey {
			return out[i].ItemKey < out[j].ItemKey
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// BuildReportInput renders the canonical report input for a session and
// its selected findings and evidence. version is the report version being
// assembled.
func BuildReportInput(s Session, findings []Finding, evidence []Evidence, version int) string {
	lines := make([]string, 0, 10+len(findings)+len(evidence)+1)
	lines = append(lines,
		reportHashVersion,
		"session_id="+s.ID.String(),
		fmt.Sprintf("version=%d", version),
		"vehicle_id="+uuidOrDash(s.VehicleID),
		"job_id="+uuidOrDash(s.JobID),
		"template_id="+s.TemplateID.String(),
		"template_name="+s.TemplateName,
		fmt.Sprintf("template_version=%d", s.TemplateVersion),
		"inspector_id="+s.InspectorID.String(),
		"submitted_at="+submissionTimestamp(s),
	)
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("finding=%s,%s,%s,%s,%s,%s",
			f.ID, f.ItemKey, f.Result, f.Severity, hashOfNotes(f.Notes), uuidOrDash(f.RecordedBy)))
	}
	for _, e := range evidence {
		uploaded := 0
		if e.Uploaded {
			uploaded = 1
		}
		lines = append(lines, fmt.Sprintf("evidence=%s,%s,%s,%s,%d",
			e.ID, e.Kind, e.ContentHash, uuidOrDash(e.FindingID), uploaded))
	}
	lines = append(lines, fmt.Sprintf("findings=%d,evidence=%d", len(findings), len(evidence)))
	return strings.Join(lines, "\n")
}

// submissionTimestamp renders the session's submitted_at for the canonical
// form. A SUBMITTED-or-later session always carries the stamp; a missing
// stamp renders as the zero time so the input stays total.
func submissionTimestamp(s Session) string {
	if s.SubmittedAt == nil {
		return time.Time{}.UTC().Format(time.RFC3339Nano)
	}
	return s.SubmittedAt.UTC().Format(time.RFC3339Nano)
}

// HashReportInput returns the lowercase hex SHA-256 of the canonical input.
func HashReportInput(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// findingSnapshot and evidenceSnapshot are the JSON wire forms frozen into
// the report record.
type findingSnapshot struct {
	ID         uuid.UUID     `json:"id"`
	ItemKey    string        `json:"item_key"`
	Result     FindingResult `json:"result"`
	Severity   Severity      `json:"severity"`
	Notes      string        `json:"notes"`
	RecordedBy *uuid.UUID    `json:"recorded_by,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
}

type evidenceSnapshot struct {
	ID          uuid.UUID    `json:"id"`
	FindingID   *uuid.UUID   `json:"finding_id,omitempty"`
	Kind        EvidenceKind `json:"kind"`
	ContentHash string       `json:"content_hash"`
	CapturedAt  time.Time    `json:"captured_at"`
	Uploaded    bool         `json:"uploaded"`
}

// marshalFindingsSnapshot renders the frozen findings JSON.
func marshalFindingsSnapshot(findings []Finding) ([]byte, error) {
	out := make([]findingSnapshot, 0, len(findings))
	for _, f := range findings {
		out = append(out, findingSnapshot{
			ID: f.ID, ItemKey: f.ItemKey, Result: f.Result, Severity: f.Severity,
			Notes: f.Notes, RecordedBy: f.RecordedBy, CreatedAt: f.CreatedAt,
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("inspections: marshal findings snapshot: %w", err)
	}
	return raw, nil
}

// marshalEvidenceSnapshot renders the frozen evidence JSON.
func marshalEvidenceSnapshot(evidence []Evidence) ([]byte, error) {
	out := make([]evidenceSnapshot, 0, len(evidence))
	for _, e := range evidence {
		out = append(out, evidenceSnapshot{
			ID: e.ID, FindingID: e.FindingID, Kind: e.Kind, ContentHash: e.ContentHash,
			CapturedAt: e.CapturedAt, Uploaded: e.Uploaded,
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("inspections: marshal evidence snapshot: %w", err)
	}
	return raw, nil
}

// reportIncompleteError builds the 422-grade error naming the failed
// integrity preconditions.
func reportIncompleteError(issues ...string) error {
	return fmt.Errorf("%w: %s", ErrReportIncomplete, strings.Join(issues, "; "))
}

// CheckReportReadiness validates the report-assembly preconditions against
// the session's template, selected findings and evidence:
//
//   - every required template item has at least one finding;
//   - every selected finding's item key is a known template item;
//   - every RED finding is linked to at least one evidence record;
//   - the session carries at least one evidence record (the report must
//     never be assembled with missing evidence hashes).
//
// findings may be the full record set; the latest-per-item selection is
// applied internally. Returns an error wrapping ErrReportIncomplete
// naming every violated precondition.
func CheckReportReadiness(template Template, s Session, findings []Finding, evidence []Evidence) error {
	selected := SelectLatestFindings(findings)

	var issues []string
	covered := make(map[string]bool, len(selected))
	for _, f := range selected {
		if _, ok := template.Item(f.ItemKey); !ok {
			issues = append(issues, fmt.Sprintf("finding item %q is not in template %q", f.ItemKey, template.Name))
			continue
		}
		covered[f.ItemKey] = true
	}
	for _, key := range template.RequiredItems() {
		if !covered[key] {
			issues = append(issues, fmt.Sprintf("required item %q has no finding", key))
		}
	}

	evidenceByFinding := make(map[uuid.UUID]bool, len(evidence))
	for _, e := range evidence {
		if e.FindingID != nil && *e.FindingID != uuid.Nil {
			evidenceByFinding[*e.FindingID] = true
		}
	}
	for _, f := range selected {
		if f.Severity == SeverityRed && !evidenceByFinding[f.ID] {
			issues = append(issues, fmt.Sprintf("RED finding %q (%s) carries no evidence", f.ItemKey, f.ID))
		}
	}
	if len(evidence) == 0 {
		issues = append(issues, "the session carries no evidence records; reports are never assembled without evidence hashes")
	}

	if len(issues) > 0 {
		return reportIncompleteError(issues...)
	}
	return nil
}

// VerifyReport recomputes the report's integrity from the live session,
// findings and evidence rows and compares it with the frozen report
// record. template is the resolved template of the session's snapshot.
// Every mismatch is reported in the result — verification never fails with
// an error for content drift, it reports verified=false.
func VerifyReport(template Template, s Session, findings []Finding, evidence []Evidence, report Report) ReportVerification {
	res := ReportVerification{
		ReportID:   report.ID,
		SessionID:  report.SessionID,
		Version:    report.Version,
		StoredHash: report.ContentHash,
		CheckedAt:  time.Now().UTC(),
	}

	// Template resolution: the session's snapshot (id, name, version) must
	// still resolve to the registered template, so a redefined registry
	// entry cannot silently reinterpret a historical inspection.
	res.TemplateMatch = template.ID == s.TemplateID &&
		template.Name == s.TemplateName &&
		template.Version == s.TemplateVersion

	// Snapshot comparisons: the live sets must still equal what was frozen
	// at assembly time.
	liveFindingsJSON, err := marshalFindingsSnapshot(SelectLatestFindings(findings))
	if err == nil && string(liveFindingsJSON) == string(report.FindingsSnapshot) {
		res.FindingsMatch = true
	}
	liveEvidenceJSON, err := marshalEvidenceSnapshot(evidence)
	if err == nil && string(liveEvidenceJSON) == string(report.EvidenceSnapshot) {
		res.EvidenceMatch = true
	}

	// Hash recomputation from live rows: session fields, selected findings
	// and evidence must regenerate the stored canonical hash.
	input := BuildReportInput(s, SelectLatestFindings(findings), evidence, report.Version)
	res.RecomputedHash = HashReportInput(input)
	res.HashMatch = subtleEqual(res.RecomputedHash, report.ContentHash)

	res.FindingsChecked = len(findings)
	res.EvidenceChecked = len(evidence)
	res.Verified = res.HashMatch && res.FindingsMatch && res.EvidenceMatch && res.TemplateMatch
	return res
}

// subtleEqual is a constant-time string comparison for hash equality.
func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
