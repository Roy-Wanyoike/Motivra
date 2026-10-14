package platform

import (
	"context"
	"net/http"
	"strings"
)

// AuthMiddleware validates Bearer access tokens and stores the resulting
// Claims in the request context. Requests without a token proceed
// unauthenticated; RequireAuthenticated enforces presence downstream.
// Valid claims also enrich the request logger with tenant_id so every
// subsequent log line carries the tenant boundary (ADR-0005 correlation,
// observability issue #84).
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
			ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
			if claims.TenantID != "" {
				EnrichRequestLogger(ctx, "tenant_id", claims.TenantID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
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

// TenantScopeFrom returns the tenant identifier for the request. It comes
// exclusively from the authenticated claims — never from client input
// (URL parameters, query strings or bodies), which would make tenant
// boundaries attacker-choosable. The second return is false for
// unauthenticated requests or claims without a tenant; callers must treat
// that as "no tenant access", not "unrestricted access" (ADR-0004).
func TenantScopeFrom(r *http.Request) (string, bool) {
	if claims, ok := ClaimsFromContext(r.Context()); ok && claims.TenantID != "" {
		return claims.TenantID, true
	}
	return "", false
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
