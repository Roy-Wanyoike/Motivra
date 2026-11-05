package inspections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Evidence-media storage seam (issue #186). The media retrieval path is
// signed and access-controlled at the HTTP layer (media_http.go); the BYTES
// themselves live behind this interface, so the S3-compatible object store
// (R2/MinIO per the README stack) plugs in without touching the signing,
// scoping or audit machinery. Until such an adapter is wired, the media
// surface stays fail-closed: signatures verify, but no object bytes exist to
// serve (Open answers ErrMediaNotFound).

// MediaObjectRef identifies one stored evidence-media object. It is built
// exclusively from the verified signature grant plus the live evidence row —
// never from raw client input.
type MediaObjectRef struct {
	SessionID   uuid.UUID
	EvidenceID  uuid.UUID
	ContentHash string
	Kind        EvidenceKind
}

// MediaObject is one opened evidence-media object.
type MediaObject struct {
	// Reader streams the object's bytes. The caller closes it.
	Reader io.ReadCloser
	// Size is the byte count for Content-Length; -1 when unknown.
	Size int64
	// ContentType is the stored media type; "" selects the safe default
	// (application/octet-stream, served with nosniff).
	ContentType string
}

// MediaBackend is the persistence + storage seam of the evidence-media
// surface (issue #186): one evidence-row lookup for the signature binding,
// one object-open for the byte path and one audit writer. The audits are a
// hard obligation (ADR-0004): the media endpoints refuse to mint or serve
// when the backend — and with it the audit writer — is absent.
type MediaBackend interface {
	// EvidenceByID returns the evidence row for (sessionID, evidenceID)
	// regardless of requester scope: the fetch route is authenticated by
	// the HMAC signature, not by a bearer token, and the signature was
	// already scope-checked at mint time. Missing rows yield an error
	// wrapping ErrMediaNotFound.
	EvidenceByID(ctx context.Context, sessionID, evidenceID uuid.UUID) (Evidence, error)

	// Open resolves one stored object. Missing objects yield an error
	// wrapping ErrMediaNotFound; any other error is internal.
	Open(ctx context.Context, ref MediaObjectRef) (MediaObject, error)

	// InsertMediaAudit appends one audit_log row (ADR-0004). Media access
	// that cannot be audited does not happen: callers fail the request
	// when this errors.
	InsertMediaAudit(ctx context.Context, audit EvidenceMediaAudit) error
}

// EvidenceMediaAudit is one evidence-media audit entry. The store renders it
// into the platform audit_log (identity chain, ADR-0004); the signing key
// and the signature itself are never part of it.
type EvidenceMediaAudit struct {
	// ActorID is the authenticated principal for a mint, or the Subject
	// bound into the (MAC-covered) signed URL for a fetch.
	ActorID uuid.UUID
	// TenantID is the actor's tenant when the minting scope carried one.
	TenantID *uuid.UUID
	// Action is one of the AuditActionEvidenceMedia* constants.
	Action string
	// SessionID and EvidenceID identify the audited object pair.
	SessionID  uuid.UUID
	EvidenceID uuid.UUID
	// ContentHash pins the object version the audit refers to.
	ContentHash string
	// Audience records the purpose binding of the grant.
	Audience string
	// ExpiresAt is the grant's expiry (mint entries only; zero otherwise).
	ExpiresAt time.Time
	// CreatedAt is populated by the store from the database clock.
	CreatedAt time.Time
}

// auditMetadata renders the audit entry's jsonb metadata. Credential
// material (signing key, signature) is deliberately excluded.
func (a EvidenceMediaAudit) auditMetadata() map[string]any {
	meta := map[string]any{
		"session_id":   a.SessionID.String(),
		"evidence_id":  a.EvidenceID.String(),
		"content_hash": a.ContentHash,
		"audience":     a.Audience,
	}
	if !a.ExpiresAt.IsZero() {
		meta["expires_at"] = a.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return meta
}

// PostgresMediaStore implements MediaBackend on the same PostgreSQL pool as
// the inspections Store: evidence rows and the platform audit_log live in
// the domain's database; object BYTES belong to whatever MediaBackend
// adapter the process wires (no object storage ships in this change — see
// the interface docs).
type PostgresMediaStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresMediaStore satisfies MediaBackend.
var _ MediaBackend = (*PostgresMediaStore)(nil)

// NewPostgresMediaStore returns a MediaBackend backed by pool (the pool must
// already have the inspections migrations applied; audit_log comes from the
// identity chain the gateway/identity applies).
func NewPostgresMediaStore(pool *pgxpool.Pool) *PostgresMediaStore {
	return &PostgresMediaStore{pool: pool}
}

// EvidenceByID implements MediaBackend.
func (s *PostgresMediaStore) EvidenceByID(ctx context.Context, sessionID, evidenceID uuid.UUID) (Evidence, error) {
	row := s.pool.QueryRow(ctx, `
                SELECT `+evidenceColumns+`
                FROM inspection_evidence
                WHERE session_id = $1 AND id = $2`, sessionID, evidenceID)
	var e Evidence
	if err := row.Scan(&e.ID, &e.SessionID, &e.FindingID, &e.Kind, &e.ContentHash, &e.CapturedAt, &e.CapturedBy, &e.Uploaded, &e.Notes, &e.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Evidence{}, fmt.Errorf("inspections: get evidence %s for session %s: %w", evidenceID, sessionID, ErrMediaNotFound)
		}
		return Evidence{}, fmt.Errorf("inspections: get evidence %s: %w", evidenceID, err)
	}
	return e, nil
}

// Open implements MediaBackend. No object-storage adapter ships with issue
// #186 (the seam is the deliverable); until one is wired the honest answer
// for a validly-signed fetch is "no such object".
func (s *PostgresMediaStore) Open(context.Context, MediaObjectRef) (MediaObject, error) {
	return MediaObject{}, fmt.Errorf("inspections: open media object: %w", ErrMediaNotFound)
}

// InsertMediaAudit implements MediaBackend: one append-only audit_log row
// (ADR-0004; the table's own append-only trigger backs the service rule).
func (s *PostgresMediaStore) InsertMediaAudit(ctx context.Context, audit EvidenceMediaAudit) error {
	if audit.Action == "" {
		return fmt.Errorf("inspections: insert media audit: action is required")
	}
	var tenantID any
	if audit.TenantID != nil {
		tenantID = *audit.TenantID
	}
	var actorID any
	if audit.ActorID != uuid.Nil {
		actorID = audit.ActorID
	}
	if _, err := s.pool.Exec(ctx, `
                INSERT INTO audit_log (actor_id, action, object_type, object_id, tenant_id, metadata)
                VALUES ($1, $2, $3, $4, $5, $6)`,
		actorID, audit.Action, AuditObjectTypeEvidence, audit.EvidenceID.String(), tenantID, audit.auditMetadata(),
	); err != nil {
		return fmt.Errorf("inspections: insert media audit: %w", err)
	}
	return nil
}
