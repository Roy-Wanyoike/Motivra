package inspections

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Authored template lifecycle HTTP surface (issue #184):
//
//	POST   /v1/inspections/authored-templates                     create draft
//	GET    /v1/inspections/authored-templates?status=&limit=&offset=   list (non-authors default to published)
//	GET    /v1/inspections/authored-templates/{templateID}        get (drafts: author roles only)
//	PATCH  /v1/inspections/authored-templates/{templateID}        edit draft (drafts only)
//	POST   /v1/inspections/authored-templates/{templateID}/publish   hash-pin + freeze
//	POST   /v1/inspections/authored-templates/{templateID}/retire    terminal retirement
//
// Author roles: ADMIN, SUPER_ADMIN (partner template authoring is a staff
// surface; GARAGE authors arrive with the garage console integration).
// Every authenticated principal may read published templates of their
// tenant scope; drafts and retired rows are author-role reads.
//
// RegisterAuthoredTemplateRoutes is called from Routes; it lives here so
// the surface stays one reviewable unit.
func RegisterAuthoredTemplateRoutes(r chi.Router, svc *Service, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	requireAuthor := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(platform.RequireRole(AuthoredTemplateAuthorRoles, h))
	}
	r.Route("/v1/inspections/authored-templates", func(r chi.Router) {
		r.Post("/", requireAuthor(svc.handleCreateAuthoredTemplate))
		r.Get("/", requireAuth(svc.handleListAuthoredTemplates))
		r.Route("/{templateID}", func(r chi.Router) {
			r.Get("/", requireAuth(svc.handleGetAuthoredTemplate))
			r.Patch("/", requireAuthor(svc.handleUpdateAuthoredTemplateDraft))
			r.Post("/publish", requireAuthor(svc.handlePublishAuthoredTemplate))
			r.Post("/retire", requireAuthor(svc.handleRetireAuthoredTemplate))
		})
	})
}

// AuthoredTemplateAuthorRoles are the roles allowed to author, edit,
// publish and retire templates (issue #184).
var AuthoredTemplateAuthorRoles = []string{RoleAdmin, RoleSuperAdmin}

// authoredTemplateRequest is the create/update body.
type authoredTemplateRequest struct {
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`
}

// authoredTemplateResponse is the wire form. Content is returned verbatim
// (canonical form as stored) so consumers hash-verify against content_hash.
type authoredTemplateResponse struct {
	ID          uuid.UUID       `json:"id"`
	TenantID    uuid.UUID       `json:"tenant_id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Content     json.RawMessage `json:"content"`
	ContentHash string          `json:"content_hash,omitempty"`
	CreatedBy   uuid.UUID       `json:"created_by"`
	PublishedAt *time.Time      `json:"published_at,omitempty"`
	RetiredAt   *time.Time      `json:"retired_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func newAuthoredTemplateResponse(t AuthoredTemplate) authoredTemplateResponse {
	return authoredTemplateResponse{
		ID:          t.ID,
		TenantID:    t.TenantID,
		Name:        t.Name,
		Status:      string(t.Status),
		Content:     t.Content,
		ContentHash: t.ContentHash,
		CreatedBy:   t.CreatedBy,
		PublishedAt: t.PublishedAt,
		RetiredAt:   t.RetiredAt,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

// authorIdentity resolves the caller: (role, userID, scope). All handler
// paths need the scope; write paths additionally need the user id as the
// audit actor — identity comes exclusively from the verified token.
func authorIdentity(r *http.Request) (platform.Claims, Scope, error) {
	claims, present := platform.ClaimsFromContext(r.Context())
	if !present {
		return platform.Claims{}, Scope{}, platform.ErrUnauthorized("authentication required")
	}
	scope, err := ScopeFromClaims(claims)
	if err != nil {
		return platform.Claims{}, Scope{}, err
	}
	return claims, scope, nil
}

func (s *Service) handleCreateAuthoredTemplate(w http.ResponseWriter, r *http.Request) {
	claims, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	authorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	// Tenant ownership: tenant-scoped authors create in their own tenant;
	// operators name the tenant explicitly in the body.
	tenantID, err := uuid.Parse(claims.TenantID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated tenant claim is not a tenant identifier"))
		return
	}
	req, err := platform.DecodeJSON[authoredTemplateRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	t, err := s.CreateAuthoredTemplate(r.Context(), authorID, tenantID, AuthoredTemplateInput{
		Name:    strings.TrimSpace(req.Name),
		Content: req.Content,
	}, scope)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAuthoredTemplateResponse(t))
}

func (s *Service) handleUpdateAuthoredTemplateDraft(w http.ResponseWriter, r *http.Request) {
	_, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	templateID, err := uuidFromRequest(r, "templateID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	req, err := platform.DecodeJSON[authoredTemplateRequest](r, maxRequestBytes)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	t, err := s.UpdateAuthoredTemplateDraft(r.Context(), templateID, AuthoredTemplateInput{
		Name:    strings.TrimSpace(req.Name),
		Content: req.Content,
	}, scope)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthoredTemplateResponse(t))
}

func (s *Service) handlePublishAuthoredTemplate(w http.ResponseWriter, r *http.Request) {
	claims, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	templateID, err := uuidFromRequest(r, "templateID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	t, err := s.PublishAuthoredTemplate(r.Context(), templateID, actorID, scope)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthoredTemplateResponse(t))
}

func (s *Service) handleRetireAuthoredTemplate(w http.ResponseWriter, r *http.Request) {
	claims, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	actorID, err := uuid.Parse(claims.UserID)
	if err != nil {
		platform.WriteError(w, platform.ErrUnauthorized("authenticated subject is not a user identifier"))
		return
	}
	templateID, err := uuidFromRequest(r, "templateID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	t, err := s.RetireAuthoredTemplate(r.Context(), templateID, actorID, scope)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthoredTemplateResponse(t))
}

func (s *Service) handleListAuthoredTemplates(w http.ResponseWriter, r *http.Request) {
	claims, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	statusFilter, err := authoredTemplateStatusFilter(r, claims)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	limit, offset, err := listPagination(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	templates, err := s.ListAuthoredTemplates(r.Context(), scope, statusFilter, limit, offset)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	out := make([]authoredTemplateResponse, 0, len(templates))
	for _, t := range templates {
		out = append(out, newAuthoredTemplateResponse(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleGetAuthoredTemplate(w http.ResponseWriter, r *http.Request) {
	claims, scope, err := authorIdentity(r)
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	templateID, err := uuidFromRequest(r, "templateID")
	if err != nil {
		platform.WriteError(w, err)
		return
	}
	t, err := s.GetAuthoredTemplate(r.Context(), templateID, scope)
	if err != nil {
		writeAuthoredTemplateError(w, err)
		return
	}
	// Drafts are authoring content: visible to author roles only, in any
	// scope. Published and retired templates read like every template.
	if t.Status == AuthoredDraft && !isAuthorRole(claims.Role) {
		platform.WriteError(w, platform.ErrNotFound("authored template not found"))
		return
	}
	writeJSON(w, http.StatusOK, newAuthoredTemplateResponse(t))
}

// isAuthorRole reports whether role may author templates.
func isAuthorRole(role string) bool {
	for _, r := range AuthoredTemplateAuthorRoles {
		if r == role {
			return true
		}
	}
	return false
}

// authoredTemplateStatusFilter resolves the ?status= parameter. Non-author
// roles are pinned to published: drafts and retired rows are authoring
// surface, not consumer surface. Authors may filter freely; an empty
// filter lists every status they can see.
func authoredTemplateStatusFilter(r *http.Request, claims platform.Claims) (*AuthoredTemplateStatus, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("status"))
	if raw == "" {
		if !isAuthorRole(claims.Role) {
			published := AuthoredPublished
			return &published, nil
		}
		return nil, nil
	}
	status := AuthoredTemplateStatus(strings.ToLower(raw))
	if !IsAuthoredTemplateStatus(status) {
		return nil, platform.ErrValidation("status must be one of draft, published, retired")
	}
	if !isAuthorRole(claims.Role) && status != AuthoredPublished {
		return nil, platform.ErrForbidden("only published templates are visible to this role")
	}
	return &status, nil
}

// writeAuthoredTemplateError maps authored-template domain errors onto
// platform API errors. Unknown errors fall through to platform.WriteError
// (500 without internals).
func writeAuthoredTemplateError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrAuthoredTemplateNotFound):
		platform.WriteError(w, platform.ErrNotFound("authored template not found"))
	case errors.Is(err, ErrAuthoredTemplateForbidden):
		platform.WriteError(w, platform.ErrForbidden("authored template outside caller scope"))
	case errors.Is(err, ErrTemplateNameTaken):
		platform.WriteError(w, platform.ErrConflict("a published template with this name already exists"))
	case errors.Is(err, ErrConflict):
		platform.WriteError(w, platform.ErrConflict(err.Error()))
	case errors.Is(err, ErrInvalidInput):
		platform.WriteError(w, platform.ErrValidation(err.Error()))
	default:
		platform.WriteError(w, err)
	}
}
