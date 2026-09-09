package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Audit actions emitted by the identity service. Object types are "user"
// and "session"; object ids are the corresponding UUIDs.
const (
	ActionUserRegistered   = "identity.user.registered"
	ActionUserLogin        = "identity.user.login"
	ActionSessionRefreshed = "identity.session.refreshed"
	ActionSessionRevoked   = "identity.session.revoked"
	ActionRoleGranted      = "identity.role.granted"
)

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
