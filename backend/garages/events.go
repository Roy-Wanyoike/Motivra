package garages

import (
	"context"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

// correlationFromContext sources the event correlation_id from the
// platform server's request-ID middleware (ADR-0002: correlation is
// propagated from the originating request). It is empty for non-HTTP
// callers. Mirrors backend/jobs.
func correlationFromContext(ctx context.Context) string {
	return middleware.GetReqID(ctx)
}

// tenantString renders the optional tenant for the event envelope.
func tenantString(t uuid.UUID) string {
	if t == uuid.Nil {
		return ""
	}
	return t.String()
}
