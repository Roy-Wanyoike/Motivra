// Package inspections implements the Motivra Inspection Platform (issue
// #61, Wave 3 A09+A10): a template registry per vehicle class,
// template-driven inspection sessions moving through a four-state machine,
// GREEN/AMBER/RED findings, evidence provenance records (content hashes
// only — no media storage) and append-only verifiable reports.
package inspections

import (
	"time"

	"github.com/google/uuid"
)

// VehicleClass is the vehicle classification a template is written for.
// It mirrors the inspection_sessions CHECK constraint in the migrations
// chain.
type VehicleClass string

// The known vehicle classes.
const (
	ClassMotorcycle VehicleClass = "MOTORCYCLE"
	ClassSedan      VehicleClass = "SEDAN"
	ClassSUV        VehicleClass = "SUV"
	ClassPickup     VehicleClass = "PICKUP"
	ClassTruck      VehicleClass = "TRUCK"
	ClassBus        VehicleClass = "BUS"
)

// allVehicleClasses is the authoritative enumeration, in registry order.
var allVehicleClasses = []VehicleClass{
	ClassMotorcycle,
	ClassSedan,
	ClassSUV,
	ClassPickup,
	ClassTruck,
	ClassBus,
}

// AllVehicleClasses returns every known vehicle class. Exported for
// validation in the HTTP layer and for exhaustive tests.
func AllVehicleClasses() []VehicleClass {
	out := make([]VehicleClass, len(allVehicleClasses))
	copy(out, allVehicleClasses)
	return out
}

// IsVehicleClass reports whether class is one of the known values.
func IsVehicleClass(class VehicleClass) bool {
	for _, c := range allVehicleClasses {
		if c == class {
			return true
		}
	}
	return false
}

// FindingResult is the recorded outcome for one template item. It mirrors
// the inspection_findings CHECK constraint in the migrations chain.
type FindingResult string

// The known finding results.
const (
	ResultPass          FindingResult = "PASS"
	ResultFail          FindingResult = "FAIL"
	ResultAttention     FindingResult = "ATTENTION"
	ResultNotApplicable FindingResult = "NOT_APPLICABLE"
)

// AllFindingResults returns every known finding result.
func AllFindingResults() []FindingResult {
	return []FindingResult{ResultPass, ResultFail, ResultAttention, ResultNotApplicable}
}

// Severity is the traffic-light rating attached to a finding: GREEN is
// healthy, AMBER needs monitoring or near-term attention, RED is unsafe or
// unacceptable. It mirrors the inspection_findings CHECK constraint.
type Severity string

// The known severities.
const (
	SeverityGreen Severity = "GREEN"
	SeverityAmber Severity = "AMBER"
	SeverityRed   Severity = "RED"
)

// AllSeverities returns every known severity.
func AllSeverities() []Severity {
	return []Severity{SeverityGreen, SeverityAmber, SeverityRed}
}

// EvidenceKind classifies the media an evidence record certifies. Actual
// upload/storage plumbing is out of scope (issue #61): the record carries
// the content hash and provenance metadata only.
type EvidenceKind string

// The known evidence kinds.
const (
	EvidenceKindPhoto    EvidenceKind = "PHOTO"
	EvidenceKindReading  EvidenceKind = "READING"
	EvidenceKindDocument EvidenceKind = "DOCUMENT"
)

// AllEvidenceKinds returns every known evidence kind.
func AllEvidenceKinds() []EvidenceKind {
	return []EvidenceKind{EvidenceKindPhoto, EvidenceKindReading, EvidenceKindDocument}
}

// IsEvidenceKind reports whether kind is one of the known values.
func IsEvidenceKind(kind EvidenceKind) bool {
	for _, k := range AllEvidenceKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// TemplateItem is one checklist line of an inspection template. Key is the
// stable machine identifier findings reference; Label is the human text.
type TemplateItem struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// Template is one registered inspection template. System templates are
// owned by the code registry (templates.go) — never seeded as demo data —
// and carry a deterministic identifier so sessions and reports can snapshot
// exactly what an inspection was performed against.
type Template struct {
	ID           uuid.UUID      `json:"id"`
	VehicleClass VehicleClass   `json:"vehicle_class"`
	Name         string         `json:"name"`
	Version      int            `json:"version"`
	Items        []TemplateItem `json:"items"`
	Active       bool           `json:"active"`
}

// Item returns the template item with the given key.
func (t Template) Item(key string) (TemplateItem, bool) {
	for _, item := range t.Items {
		if item.Key == key {
			return item, true
		}
	}
	return TemplateItem{}, false
}

// RequiredItems returns the keys of every required template item.
func (t Template) RequiredItems() []string {
	var keys []string
	for _, item := range t.Items {
		if item.Required {
			keys = append(keys, item.Key)
		}
	}
	return keys
}

// Session is one template-driven inspection walk. It is the aggregate
// root: findings, evidence, reports and the transition audit all belong to
// exactly one session.
type Session struct {
	ID uuid.UUID
	// TenantID is the owning organization (from the creator's claims) when
	// the inspection was performed under a tenant; nil otherwise.
	TenantID *uuid.UUID
	// VehicleID links the inspection to its subject. Cross-domain reference
	// (vehicles) held by identifier only — no cross-domain FK (ADR-0003).
	VehicleID *uuid.UUID
	// JobID links the inspection to a jobs-domain job when the inspection
	// was produced inside a job workflow. Cross-domain reference, no FK.
	JobID *uuid.UUID
	// CustomerID is the vehicle owner's user id when known; it is the read
	// path by which the owner sees their inspection and report.
	CustomerID *uuid.UUID
	// VehicleClass selects the template at creation and is stored with the
	// session.
	VehicleClass VehicleClass
	// Template identity snapshot: resolved from the registry at creation
	// and frozen, so later registry changes never reinterpret a session.
	TemplateID      uuid.UUID
	TemplateName    string
	TemplateVersion int
	// InspectorID is the principal performing the inspection; it is always
	// the authenticated creator (never client-asserted).
	InspectorID uuid.UUID
	// Status is one of the Status* constants on the state machine.
	Status      Status
	StartedAt   *time.Time
	SubmittedAt *time.Time
	ReportedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Finding is one recorded item result. Rows are append-only: a corrected
// finding for the same item is a NEW record and the latest record per item
// wins at report assembly. After the session leaves IN_PROGRESS the
// findings are sealed — the database itself refuses further inserts.
type Finding struct {
	ID         uuid.UUID
	SessionID  uuid.UUID
	ItemKey    string
	Result     FindingResult
	Severity   Severity
	Notes      string
	RecordedBy *uuid.UUID
	CreatedAt  time.Time
}

// Evidence is one provenance record: a content hash certifying that a
// piece of media existed at captured_at under captured_by, with the
// client-attested upload state. Rows are append-only and sealed on submit.
type Evidence struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	// FindingID optionally ties the evidence to a specific finding (e.g. a
	// photo proving a RED brake finding).
	FindingID *uuid.UUID
	Kind      EvidenceKind
	// ContentHash is the lowercase hex SHA-256 of the media bytes.
	ContentHash string
	CapturedAt  time.Time
	CapturedBy  *uuid.UUID
	Uploaded    bool
	Notes       string
	CreatedAt   time.Time
}

// Report is the issued, verifiable inspection report. Assembly snapshots
// the selected findings and evidence hashes, hashes the canonical form
// into ContentHash and freezes everything. The table is append-only.
type Report struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	// Version is the report's version within its session. A session is
	// reported exactly once today (the state machine moves it to
	// REPORT_AVAILABLE on issuance); a corrected inspection is a NEW
	// session, mirroring the vehicles corrections-as-new-records precedent.
	Version         int
	VehicleID       *uuid.UUID
	JobID           *uuid.UUID
	InspectorID     uuid.UUID
	TemplateID      uuid.UUID
	TemplateName    string
	TemplateVersion int
	// FindingsSnapshot and EvidenceSnapshot are the JSON snapshots frozen
	// at assembly time.
	FindingsSnapshot []byte
	EvidenceSnapshot []byte
	FindingsCount    int
	EvidenceCount    int
	// ContentHash is the lowercase hex SHA-256 of the canonical report
	// input (report.go documents the exact algorithm).
	ContentHash string
	IssuedBy    *uuid.UUID
	Notes       string
	CreatedAt   time.Time
}

// ReportVerification is the outcome of recomputing a report's integrity
// from the live session, findings and evidence rows.
type ReportVerification struct {
	ReportID  uuid.UUID `json:"report_id"`
	SessionID uuid.UUID `json:"session_id"`
	Version   int       `json:"version"`
	// Verified is true only when every check below passed.
	Verified bool `json:"verified"`
	// HashMatch reports whether the recomputed canonical hash equals the
	// stored content_hash.
	HashMatch bool `json:"hash_match"`
	// FindingsMatch reports whether the live selected findings still equal
	// the report's frozen snapshot.
	FindingsMatch bool `json:"findings_match"`
	// EvidenceMatch reports whether the live evidence set still equals the
	// report's frozen snapshot.
	EvidenceMatch bool `json:"evidence_match"`
	// TemplateMatch reports whether the session's template snapshot still
	// resolves to the registered template (id, name, version).
	TemplateMatch bool `json:"template_match"`
	// FindingsChecked and EvidenceChecked count the live rows the
	// recomputation consumed.
	FindingsChecked int       `json:"findings_checked"`
	EvidenceChecked int       `json:"evidence_checked"`
	StoredHash      string    `json:"stored_hash"`
	RecomputedHash  string    `json:"recomputed_hash"`
	CheckedAt       time.Time `json:"checked_at"`
}

// SessionTransition is one row of the append-only session transition audit
// log. Rows are never updated or deleted.
type SessionTransition struct {
	ID         int64
	SessionID  uuid.UUID
	FromStatus Status
	ToStatus   Status
	ActorID    *uuid.UUID
	Reason     string
	CreatedAt  time.Time
}
