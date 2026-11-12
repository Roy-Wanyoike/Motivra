package inspections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Authored template lifecycle (issue #184): the per-tenant authoring
// surface on top of the code-owned system registry (templates.go). Where
// the registry holds product content compiled into every release, authored
// templates are tenant-owned checklists that move draft → published →
// retired:
//
//   - draft     content editable (authoring surface), invisible to
//     non-author roles at the HTTP layer;
//   - published hash-pinned — content_hash freezes the SHA-256 of the
//     canonical content JSON at publish time; content, name and
//     hash are immutable from then on (DB guard trigger +
//     service-level guards);
//   - retired   readable for historical reference, unusable for new
//     sessions, terminal (no resurrection).
//
// Discipline (mirrors verification.go, ADR-0004):
//   - publish and retire write their platform audit_log row inside the
//     same transaction as the state change — an audit failure fails the
//     request and leaves no unaudited transition behind;
//   - exactly one published version per (tenant_id, name) — publishing
//     against a live published version conflicts until it is retired.
const (
	// EventAuthoredTemplatePublished is published as
	// motivra.inspections.authored_template.published.v1.
	EventAuthoredTemplatePublished = "authored_template.published.v1"
	// EventAuthoredTemplateRetired is published as
	// motivra.inspections.authored_template.retired.v1.
	EventAuthoredTemplateRetired = "authored_template.retired.v1"
)

// Audit actions and object type for authored-template transitions.
const (
	AuditActionTemplatePublished = "inspections.authored_template.published"
	AuditActionTemplateRetired   = "inspections.authored_template.retired"
	AuditObjectTypeTemplate      = "inspection_authored_template"
)

// AuthoredTemplateStatus is the lifecycle state of an authored template.
type AuthoredTemplateStatus string

// The known authored-template statuses.
const (
	AuthoredDraft     AuthoredTemplateStatus = "draft"
	AuthoredPublished AuthoredTemplateStatus = "published"
	AuthoredRetired   AuthoredTemplateStatus = "retired"
)

// IsAuthoredTemplateStatus reports whether status is a known lifecycle state.
func IsAuthoredTemplateStatus(status AuthoredTemplateStatus) bool {
	return status == AuthoredDraft || status == AuthoredPublished || status == AuthoredRetired
}

// Authored template content limits: a checklist is a bounded JSON document.
const (
	maxTemplateNameLength = 200
	maxTemplateItems      = 200
	maxTemplateJSONBytes  = 256 << 10 // 256 KiB of canonical content
)

// AuthoredTemplate is one tenant-owned inspection checklist.
type AuthoredTemplate struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
	Status   AuthoredTemplateStatus
	Content  json.RawMessage
	// ContentHash is the hex SHA-256 of the canonical content JSON, set
	// at publish and never changed afterwards (hash pinning).
	ContentHash string
	CreatedBy   uuid.UUID
	PublishedAt *time.Time
	RetiredAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AuthoredTemplateInput is the validated create/update payload.
type AuthoredTemplateInput struct {
	Name    string
	Content json.RawMessage
}

// Errors surfaced by the authored-template lifecycle. HTTP mapping lives in
// templates_lifecycle_http.go.
var (
	// ErrAuthoredTemplateNotFound is returned when a template does not
	// exist or is invisible to the caller's scope (no existence leak).
	ErrAuthoredTemplateNotFound = errors.New("inspections: authored template not found")
	// ErrTemplateNameTaken is returned when publishing (or creating under
	// the published unique index) collides with a live published version
	// of the same (tenant_id, name).
	ErrTemplateNameTaken = errors.New("inspections: a published template with this name already exists")
)

// canonicalTemplateContent decodes and re-encodes the submitted content so
// the pinned hash is computed over a canonical form (sorted object keys,
// no insignificant whitespace) rather than the client's byte layout.
func canonicalTemplateContent(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, invalidInput("authored template: content is required")
	}
	if len(raw) > maxTemplateJSONBytes {
		return nil, invalidInput("authored template: content exceeds %d bytes", maxTemplateJSONBytes)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, invalidInput("authored template: content is not valid JSON")
	}
	// A checklist is an object with an items array — anything else is a
	// contract mismatch worth refusing early (the hash pins structure).
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, invalidInput("authored template: content must be a JSON object")
	}
	items, ok := obj["items"].([]any)
	if !ok {
		return nil, invalidInput("authored template: content.items must be an array")
	}
	if len(items) == 0 || len(items) > maxTemplateItems {
		return nil, invalidInput("authored template: content.items must hold between 1 and %d entries", maxTemplateItems)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, invalidInput("authored template: content is not canonicalizable JSON")
	}
	return json.RawMessage(canonical), nil
}

// hashTemplateContent pins the canonical content: hex SHA-256.
func hashTemplateContent(canonical json.RawMessage) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// validateAuthoredTemplateInput applies the create/update rules.
func validateAuthoredTemplateInput(in AuthoredTemplateInput) (json.RawMessage, error) {
	if len(in.Name) == 0 || len(in.Name) > maxTemplateNameLength {
		return nil, invalidInput("authored template: name must hold between 1 and %d characters", maxTemplateNameLength)
	}
	return canonicalTemplateContent(in.Content)
}

// authoredTemplateAuditEntry builds the audit_log entry written atomically
// with a publish/retire transition.
func authoredTemplateAuditEntry(t AuthoredTemplate, actorID uuid.UUID, action string) AuthoredTemplateAudit {
	meta := map[string]any{
		"name":     t.Name,
		"status":   string(t.Status),
		"template": t.ID.String(),
	}
	if t.ContentHash != "" {
		meta["content_hash"] = t.ContentHash
	}
	return AuthoredTemplateAudit{
		ActorID:    actorID,
		TenantID:   &t.TenantID,
		Action:     action,
		ObjectType: AuditObjectTypeTemplate,
		ObjectID:   t.ID,
		Metadata:   meta,
	}
}

// CreateAuthoredTemplate inserts a new draft owned by the caller's tenant.
// The author identity comes exclusively from the verified token.
func (s *Service) CreateAuthoredTemplate(ctx context.Context, authorID uuid.UUID, tenantID uuid.UUID, in AuthoredTemplateInput, scope Scope) (AuthoredTemplate, error) {
	canonical, err := validateAuthoredTemplateInput(in)
	if err != nil {
		return AuthoredTemplate{}, err
	}
	if !scope.Unrestricted() {
		// A tenant-scoped author may only create inside their own tenant;
		// an operator (unrestricted) may author for any tenant, so the
		// caller supplies the tenant explicitly.
		if scope.TenantID == nil || *scope.TenantID != tenantID {
			return AuthoredTemplate{}, fmt.Errorf("inspections: create authored template: tenant mismatch: %w", ErrAuthoredTemplateForbidden)
		}
	}
	t := AuthoredTemplate{
		TenantID:  tenantID,
		Name:      in.Name,
		Status:    AuthoredDraft,
		Content:   canonical,
		CreatedBy: authorID,
	}
	if err := s.store.CreateAuthoredTemplate(ctx, &t); err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: create authored template: %w", err)
	}
	return t, nil
}

// ErrAuthoredTemplateForbidden is returned when the caller's scope does not
// cover the target tenant.
var ErrAuthoredTemplateForbidden = errors.New("inspections: authored template outside caller scope")

// UpdateAuthoredTemplateDraft edits name/content while the template is a
// draft. Published and retired templates are immutable (hash pinning).
func (s *Service) UpdateAuthoredTemplateDraft(ctx context.Context, templateID uuid.UUID, in AuthoredTemplateInput, scope Scope) (AuthoredTemplate, error) {
	canonical, err := validateAuthoredTemplateInput(in)
	if err != nil {
		return AuthoredTemplate{}, err
	}
	current, err := s.store.GetAuthoredTemplate(ctx, templateID, scope)
	if err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: update authored template: %w", err)
	}
	if current.Status != AuthoredDraft {
		return AuthoredTemplate{}, fmt.Errorf(
			"inspections: update authored template: template is %s; published templates are hash-pinned and immutable: %w",
			current.Status, ErrConflict)
	}
	current.Name = in.Name
	current.Content = canonical
	if err := s.store.UpdateAuthoredTemplateDraft(ctx, &current); err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: update authored template: %w", err)
	}
	return current, nil
}

// PublishAuthoredTemplate pins the draft: computes the canonical content
// hash, freezes the row and writes the audit entry in one transaction.
func (s *Service) PublishAuthoredTemplate(ctx context.Context, templateID uuid.UUID, actorID uuid.UUID, scope Scope) (AuthoredTemplate, error) {
	current, err := s.store.GetAuthoredTemplate(ctx, templateID, scope)
	if err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: publish authored template: %w", err)
	}
	if current.Status != AuthoredDraft {
		return AuthoredTemplate{}, fmt.Errorf(
			"inspections: publish authored template: template is %s, want %s: %w",
			current.Status, AuthoredDraft, ErrConflict)
	}
	current.ContentHash = hashTemplateContent(current.Content)
	audit := authoredTemplateAuditEntry(current, actorID, AuditActionTemplatePublished)
	if err := s.store.TransitionAuthoredTemplate(ctx, templateID, AuthoredPublished, &current, &audit); err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: publish authored template: %w", err)
	}
	if err := s.publish(ctx, EventAuthoredTemplatePublished, current.ID, &actorID, tenantString(&current.TenantID), AuthoredTemplatePayload{
		TemplateID:  current.ID,
		TenantID:    current.TenantID,
		Name:        current.Name,
		ContentHash: current.ContentHash,
		PublishedBy: actorID,
	}); err != nil {
		return AuthoredTemplate{}, err
	}
	return current, nil
}

// RetireAuthoredTemplate moves a published template to retired (terminal).
// The hash stays pinned; historical reports keep their references valid.
func (s *Service) RetireAuthoredTemplate(ctx context.Context, templateID uuid.UUID, actorID uuid.UUID, scope Scope) (AuthoredTemplate, error) {
	current, err := s.store.GetAuthoredTemplate(ctx, templateID, scope)
	if err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: retire authored template: %w", err)
	}
	if current.Status != AuthoredPublished {
		return AuthoredTemplate{}, fmt.Errorf(
			"inspections: retire authored template: template is %s, want %s: %w",
			current.Status, AuthoredPublished, ErrConflict)
	}
	audit := authoredTemplateAuditEntry(current, actorID, AuditActionTemplateRetired)
	if err := s.store.TransitionAuthoredTemplate(ctx, templateID, AuthoredRetired, &current, &audit); err != nil {
		return AuthoredTemplate{}, fmt.Errorf("inspections: retire authored template: %w", err)
	}
	if err := s.publish(ctx, EventAuthoredTemplateRetired, current.ID, &actorID, tenantString(&current.TenantID), AuthoredTemplatePayload{
		TemplateID:  current.ID,
		TenantID:    current.TenantID,
		Name:        current.Name,
		ContentHash: current.ContentHash,
		RetiredBy:   actorID,
	}); err != nil {
		return AuthoredTemplate{}, err
	}
	return current, nil
}

// AuthoredTemplatePayload is the payload of authored_template.published.v1
// and authored_template.retired.v1.
type AuthoredTemplatePayload struct {
	TemplateID  uuid.UUID `json:"template_id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Name        string    `json:"name"`
	ContentHash string    `json:"content_hash"`
	PublishedBy uuid.UUID `json:"published_by,omitempty"`
	RetiredBy   uuid.UUID `json:"retired_by,omitempty"`
}

// AuthoredTemplateAudit is the audit_log entry written atomically with a
// publish/retire transition.
type AuthoredTemplateAudit struct {
	ActorID    uuid.UUID
	TenantID   *uuid.UUID
	Action     string
	ObjectType string
	ObjectID   uuid.UUID
	Metadata   map[string]any
}

// ListAuthoredTemplates returns one offset page, newest first. Status
// filtering is explicit; an empty filter defaults to published (the only
// status non-author roles may see).
func (s *Service) ListAuthoredTemplates(ctx context.Context, scope Scope, status *AuthoredTemplateStatus, limit, offset int) ([]AuthoredTemplate, error) {
	if status != nil && !IsAuthoredTemplateStatus(*status) {
		return nil, invalidInput("list authored templates: status %q is not a known template status", *status)
	}
	return s.store.ListAuthoredTemplates(ctx, scope, status, limit, offset)
}

// GetAuthoredTemplate returns one template visible to scope. Drafts are
// authoring content — the HTTP layer refuses them to non-author roles.
func (s *Service) GetAuthoredTemplate(ctx context.Context, templateID uuid.UUID, scope Scope) (AuthoredTemplate, error) {
	return s.store.GetAuthoredTemplate(ctx, templateID, scope)
}
