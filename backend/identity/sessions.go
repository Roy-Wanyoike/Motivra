package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Session management API (issue #87): user-facing visibility and control
// over refresh sessions. Every method is scoped to the authenticated
// caller — a session id owned by somebody else is indistinguishable from
// an unknown one (404), so the API never discloses session existence
// across accounts. Every state change is audited; a failed audit record
// fails the request (ADR-0004).

// ListSessions returns the caller's sessions (live and revoked, newest
// first) with ALL credential material stripped: RefreshTokenHash is zeroed
// on every returned row — the API never carries token digests.
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	sessions, err := s.store.ListUserSessions(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	for i := range sessions {
		sessions[i].RefreshTokenHash = ""
	}
	return sessions, nil
}

// RevokeSession revokes ONE of the caller's sessions and audits the
// revocation. Unknown sessions and sessions owned by other users yield the
// same not-found error (no cross-account existence oracle). Revoking an
// already-revoked session is idempotent.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID, actorID uuid.UUID) error {
	sess, err := s.store.GetSessionByID(ctx, sessionID)
	if err != nil {
		if isNotFound(err) {
			return platform.ErrNotFound("session not found")
		}
		return fmt.Errorf("load session: %w", err)
	}
	if sess.UserID != userID {
		return platform.ErrNotFound("session not found")
	}
	if err := s.store.RevokeSession(ctx, sessionID); err != nil {
		if isNotFound(err) {
			return platform.ErrNotFound("session not found")
		}
		return fmt.Errorf("revoke session: %w", err)
	}
	return s.audit(ctx, actorID, ActionSessionRevoked, "session", sessionID.String(), map[string]any{
		"via":         "sessions_api",
		"owner_id":    userID.String(),
		"device_name": platform.RedactFreeText(sess.DeviceName),
	})
}

// RevokeAllSessions revokes EVERY live session of the caller (sign-out
// everywhere, including the session that performed the call) and audits
// the sweep with the revoked count.
func (s *Service) RevokeAllSessions(ctx context.Context, userID, actorID uuid.UUID) (int, error) {
	revoked, err := s.store.RevokeUserSessions(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke all sessions: %w", err)
	}
	if err := s.audit(ctx, actorID, ActionSessionRevokedAll, "user", userID.String(), map[string]any{
		"revoked": revoked,
	}); err != nil {
		return 0, err
	}
	return revoked, nil
}
