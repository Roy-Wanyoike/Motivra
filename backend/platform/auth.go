package platform

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// AuthMiddleware validates Bearer access tokens and stores the resulting
// Claims in the request context. Requests without a token proceed
// unauthenticated; RequireAuthenticated enforces presence downstream.
func AuthMiddleware(v *JWTValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			claims, err := v.Validate(token)
			if err != nil {
				WriteError(w, ErrUnauthorized("access token is invalid or expired"))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyClaims, claims)))
		})
	}
}

// RequireAuthenticated wraps h so only authenticated requests reach it.
func RequireAuthenticated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ClaimsFromContext(r.Context()); !ok {
			WriteError(w, ErrUnauthorized("authentication required"))
			return
		}
		h(w, r)
	}
}

// RequireRole wraps h so only requests carrying one of the allowed roles
// reach it. Unauthenticated requests get 401; wrong roles get 403.
func RequireRole(roles []string, h http.HandlerFunc) http.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			WriteError(w, ErrUnauthorized("authentication required"))
			return
		}
		if _, ok := allowed[claims.Role]; !ok {
			WriteError(w, ErrForbidden("insufficient role"))
			return
		}
		h(w, r)
	}
}

// TenantScopeFrom returns the tenant identifier for the request: the
// authenticated claims first, otherwise the {tenantID} URL placeholder.
// Domain repositories MUST scope queries with it on multi-tenant tables
// (ADR-0004).
func TenantScopeFrom(r *http.Request) string {
	if claims, ok := ClaimsFromContext(r.Context()); ok && claims.TenantID != "" {
		return claims.TenantID
	}
	return chi.URLParam(r, "tenantID")
}

// bearerToken extracts the Bearer credential from the Authorization header.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
