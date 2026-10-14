package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Audit actions emitted by the identity service. Object types are "user"
// and "session"; object ids are the corresponding UUIDs, except collection
// reads (issue #88) whose object id is the literal "collection".
const (
	ActionUserRegistered   = "identity.user.registered"
	ActionUserLogin        = "identity.user.login"
	ActionSessionRefreshed = "identity.session.refreshed"
	ActionSessionRevoked   = "identity.session.revoked"
	ActionRoleGranted      = "identity.role.granted"

	// Session management API (issue #87): revoked_all fires once per
	// revoke-all request with the count in its metadata; single revocations
	// through the API reuse ActionSessionRevoked with a via marker.
	ActionSessionRevokedAll = "identity.session.revoked_all"

	// Password lifecycle (issue #86): the breach-check note is the fail-open
	// record required when the ADR-0004 breached-password check could not
	// complete; password_changed covers the authenticated change flow.
	ActionPasswordChanged        = "identity.user.password_changed"
	ActionBreachCheckUnavailable = "identity.password.breach_check_unavailable"

	// Verification flow (issue #86): issued fires for every single-use
	// token created at registration, verified for a consumed token, and
	// resent for a re-issuance through the resend endpoint.
	ActionVerificationIssued   = "identity.verification.issued"
	ActionVerificationVerified = "identity.verification.verified"
	ActionVerificationResent   = "identity.verification.resent"

	// Privileged admin reads (issue #88): every page of the account
	// directory an operator pulls is itself a security-relevant read
	// (ADR-0004 "privileged reads of personal data are audited" posture).
	// The object is the queried collection, not a single user row; the
	// page shape rides the metadata.
	ActionUsersListed = "identity.users.listed"
)

// AuditRecord is the read-side projection of one audit_log row (issue #88
// admin audit API). It mirrors AuditEntry plus the row's identity and
// timestamps: ID is the append-only sequence number and CreatedAt the
// server-side write time — neither is client-controllable.
type AuditRecord struct {
	ID         int64
	ActorID    *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   string
	TenantID   *uuid.UUID
	Metadata   map[string]any
	CreatedAt  time.Time
}

// Pagination bounds for the audit read API (mirrors the admin user listing
// so the admin surface keeps one pagination contract).
const (
	defaultAuditListLimit = 20
	maxAuditListLimit     = 100
)

// AuditFilter selects one page of audit records. The attribute fields are
// exact-match constraints; zero values mean "no constraint". TenantID is
// NOT a query parameter for most callers: the HTTP layer derives it from
// the validated access-token claims — an ADMIN whose claim carries a
// tenant is always filtered to it (a client cannot widen its scope), while
// SUPER_ADMIN may narrow the platform-wide trail with an explicit filter.
// A tenant-less ADMIN and a SUPER_ADMIN with no filter read the
// platform-wide trail, the same by-design operator surface as
// GET /v1/admin/users.
type AuditFilter struct {
	ActorID    *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   string
	TenantID   *uuid.UUID
	Since      *time.Time // created_at >= Since
	Until      *time.Time // created_at <  Until
	Limit      int
	Offset     int
}

// clamp clamps the pagination fields to the service defaults and caps.
func (f *AuditFilter) clamp() {
	if f.Limit <= 0 {
		f.Limit = defaultAuditListLimit
	}
	if f.Limit > maxAuditListLimit {
		f.Limit = maxAuditListLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// ListAudit returns the filtered audit page for the admin audit API
// (issue #88). The filter arrives fully scoped: the HTTP layer derives the
// tenant constraint from the validated claims, and this method adds no
// scope of its own — the store's query is the enforcement point.
func (s *Service) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditRecord, error) {
	filter.clamp()
	records, err := s.store.ListAudit(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	return records, nil
}

// ListUsersForAdmin returns the directory page AND audits the privileged
// read (issue #88: admin user reads are audited). The audit row attributes
// the acting admin, their tenant scope and the exact page shape; a failed
// audit record fails the request, so the read signal can never be lost
// silently (ADR-0004).
func (s *Service) ListUsersForAdmin(ctx context.Context, actorID uuid.UUID, tenantID *uuid.UUID, limit, offset int) ([]User, error) {
	users, err := s.ListUsers(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	var actor *uuid.UUID
	if actorID != (uuid.UUID{}) {
		id := actorID
		actor = &id
	}
	entry := AuditEntry{
		ActorID:    actor,
		Action:     ActionUsersListed,
		ObjectType: "user",
		ObjectID:   "collection",
		TenantID:   tenantID,
		Metadata: map[string]any{
			"limit":    clampLimit(limit),
			"offset":   clampOffset(offset),
			"returned": len(users),
		},
	}
	if err := RecordAudit(ctx, s.store, entry); err != nil {
		return nil, fmt.Errorf("audit %s: %w", ActionUsersListed, err)
	}
	return users, nil
}

// AuditEntry is one append-only audit record (ADR-0003 append-only rules,
// ADR-0004 privileged-action auditing). ActorID and TenantID are nil for
// system-originated or personal (tenant-less) entries.
type AuditEntry struct {
	ActorID    *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   string
	TenantID   *uuid.UUID
	Metadata   map[string]any
}

// RecordAudit appends entry to the audit trail via store. Audit failures
// are never swallowed: the caller must fail the request so that security
// signals cannot be lost silently.
func RecordAudit(ctx context.Context, store SessionStore, entry AuditEntry) error {
	if entry.Action == "" {
		return platform.ErrValidation("audit action is required",
			platform.FieldError{Field: "action", Issue: "required"})
	}
	if entry.ObjectType == "" || entry.ObjectID == "" {
		return platform.ErrValidation("audit object type and id are required",
			platform.FieldError{Field: "object_type", Issue: "required"},
			platform.FieldError{Field: "object_id", Issue: "required"})
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if err := store.InsertAudit(ctx, entry); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}
