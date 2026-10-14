package identity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// fakeStore is an in-memory SessionStore implementing the full interface
// plus the RoleGrantsProvider capability, for white-box tests without a
// database. Failure injection fields simulate infrastructure faults.
type fakeStore struct {
	mu                 sync.Mutex
	users              map[uuid.UUID]User
	byEmail            map[string]uuid.UUID
	grants             map[uuid.UUID][]RoleGrant
	sessions           map[uuid.UUID]Session
	byRefresh          map[string]uuid.UUID
	verification       map[uuid.UUID]VerificationToken
	verificationByHash map[string]uuid.UUID
	audits             []AuditEntry
	// auditTimes records each audit row's insertion time in lockstep
	// with audits, backing the ListAudit read model (issue #88).
	auditTimes []time.Time

	failCreateUser bool
	failAudit      bool
}

// newFakeStore returns an empty store ready for use.
func newFakeStore() *fakeStore {
	return &fakeStore{
		users:              map[uuid.UUID]User{},
		byEmail:            map[string]uuid.UUID{},
		grants:             map[uuid.UUID][]RoleGrant{},
		sessions:           map[uuid.UUID]Session{},
		byRefresh:          map[string]uuid.UUID{},
		verification:       map[uuid.UUID]VerificationToken{},
		verificationByHash: map[string]uuid.UUID{},
	}
}

// CreateUser inserts the user and its roles, lowercasing the email like the
// SQL store. Duplicate emails surface as platform.ErrConflict.
func (f *fakeStore) CreateUser(_ context.Context, u *User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreateUser {
		return errors.New("injected create-user failure")
	}
	u.Email = strings.ToLower(u.Email)
	if _, taken := f.byEmail[u.Email]; taken {
		return platform.ErrConflict("email address is already registered")
	}
	now := time.Now()
	u.CreatedAt, u.UpdatedAt = now, now
	f.users[u.ID] = *u
	f.byEmail[u.Email] = u.ID
	roles := u.Roles
	if len(roles) == 0 {
		roles = []Role{RoleCustomer}
	}
	for _, role := range roles {
		f.grants[u.ID] = append(f.grants[u.ID], RoleGrant{Role: role})
	}
	u.Roles = roles
	return nil
}

// GetUser returns the stored user with roles loaded.
func (f *fakeStore) GetUser(_ context.Context, id uuid.UUID) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	u.Roles = nil
	for _, g := range f.grants[id] {
		u.Roles = append(u.Roles, g.Role)
	}
	return u, nil
}

// GetUserByEmail returns the user owning the (case-insensitive) email.
func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byEmail[email]
	if !ok {
		return User{}, ErrUserNotFound
	}
	u := f.users[id]
	u.Roles = nil
	for _, g := range f.grants[id] {
		u.Roles = append(u.Roles, g.Role)
	}
	return u, nil
}

// AssignRole grants a role, optionally tenant-scoped; duplicates conflict.
func (f *fakeStore) AssignRole(_ context.Context, userID uuid.UUID, role Role, tenantID *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return ErrUserNotFound
	}
	for _, g := range f.grants[userID] {
		sameTenant := (g.TenantID == nil && tenantID == nil) ||
			(g.TenantID != nil && tenantID != nil && *g.TenantID == *tenantID)
		if g.Role == role && sameTenant {
			return platform.ErrConflict("role already assigned")
		}
	}
	f.grants[userID] = append(f.grants[userID], RoleGrant{Role: role, TenantID: tenantID})
	return nil
}

// GetRoleGrants returns the user's role grants (RoleGrantsProvider).
func (f *fakeStore) GetRoleGrants(_ context.Context, userID uuid.UUID) ([]RoleGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return nil, ErrUserNotFound
	}
	return append([]RoleGrant(nil), f.grants[userID]...), nil
}

// CreateSession persists the session and indexes it by refresh-token hash.
func (f *fakeStore) CreateSession(_ context.Context, s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	f.sessions[s.ID] = s
	f.byRefresh[s.RefreshTokenHash] = s.ID
	return nil
}

// GetSessionByRefreshHash returns the session owning the digest.
func (f *fakeStore) GetSessionByRefreshHash(_ context.Context, hash string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byRefresh[hash]
	if !ok {
		return Session{}, ErrUserNotFound
	}
	return f.sessions[id], nil
}

// GetSessionByID returns the session with the given id (issue #87 API).
func (f *fakeStore) GetSessionByID(_ context.Context, id uuid.UUID) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return Session{}, ErrUserNotFound
	}
	return s, nil
}

// ListUserSessions returns every session of the user, newest first
// (revoked ones included), like the SQL store's ordering (issue #87 API).
func (f *fakeStore) ListUserSessions(_ context.Context, userID uuid.UUID) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID.String() > out[j].ID.String()
	})
	return out, nil
}

// RevokeSession marks the session revoked; unknown ids are not found.
func (f *fakeStore) RevokeSession(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return ErrUserNotFound
	}
	now := time.Now()
	s.RevokedAt = &now
	f.sessions[id] = s
	return nil
}

// InsertAudit appends an audit record unless failure is injected.
func (f *fakeStore) InsertAudit(_ context.Context, entry AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAudit {
		return errors.New("injected audit failure")
	}
	f.audits = append(f.audits, entry)
	f.auditTimes = append(f.auditTimes, time.Now())
	return nil
}

// ListAudit projects the recorded audit rows through the AuditFilter,
// mirroring the SQL store's contract: exact-match constraints, tenant
// equality, since/until window, newest first (reverse insertion order,
// which matches created_at DESC for same-clock rows), offset pagination.
func (f *fakeStore) ListAudit(_ context.Context, filter AuditFilter) ([]AuditRecord, error) {
	filter.clamp()
	f.mu.Lock()
	defer f.mu.Unlock()

	matched := make([]AuditRecord, 0, len(f.audits))
	for i, a := range f.audits {
		if filter.ActorID != nil && (a.ActorID == nil || *a.ActorID != *filter.ActorID) {
			continue
		}
		if filter.Action != "" && a.Action != filter.Action {
			continue
		}
		if filter.ObjectType != "" && a.ObjectType != filter.ObjectType {
			continue
		}
		if filter.ObjectID != "" && a.ObjectID != filter.ObjectID {
			continue
		}
		if filter.TenantID != nil && (a.TenantID == nil || *a.TenantID != *filter.TenantID) {
			continue
		}
		at := f.auditTimes[i]
		if filter.Since != nil && at.Before(*filter.Since) {
			continue
		}
		if filter.Until != nil && !at.Before(*filter.Until) {
			continue
		}
		metadata := a.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		matched = append(matched, AuditRecord{
			ID:         int64(i + 1),
			ActorID:    a.ActorID,
			Action:     a.Action,
			ObjectType: a.ObjectType,
			ObjectID:   a.ObjectID,
			TenantID:   a.TenantID,
			Metadata:   metadata,
			CreatedAt:  at,
		})
	}
	// Newest first: reverse insertion order, then offset-page.
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	if filter.Offset > len(matched) {
		filter.Offset = len(matched)
	}
	matched = matched[filter.Offset:]
	if filter.Limit < len(matched) {
		matched = matched[:filter.Limit]
	}
	return matched, nil
}

// UpdatePassword replaces the user's stored password hash.
func (f *fakeStore) UpdatePassword(_ context.Context, userID uuid.UUID, passwordHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	u.PasswordHash = passwordHash
	u.UpdatedAt = time.Now()
	f.users[userID] = u
	return nil
}

// RevokeUserSessions revokes every live session of the user, returning the
// number of sessions it revoked.
func (f *fakeStore) RevokeUserSessions(_ context.Context, userID uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return 0, ErrUserNotFound
	}
	revoked := 0
	now := time.Now()
	for id, s := range f.sessions {
		if s.UserID == userID && s.RevokedAt == nil {
			s.RevokedAt = &now
			f.sessions[id] = s
			revoked++
		}
	}
	return revoked, nil
}

// CreateVerificationToken persists a single-use verification token row.
func (f *fakeStore) CreateVerificationToken(_ context.Context, t VerificationToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[t.UserID]; !ok {
		return ErrUserNotFound
	}
	if _, ok := f.verificationByHash[t.TokenHash]; ok {
		return platform.ErrConflict("verification token already exists")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	f.verification[t.ID] = t
	f.verificationByHash[t.TokenHash] = t.ID
	return nil
}

// GetVerificationTokenByHash returns the token owning the digest.
func (f *fakeStore) GetVerificationTokenByHash(_ context.Context, hash string) (VerificationToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.verificationByHash[hash]
	if !ok {
		return VerificationToken{}, ErrUserNotFound
	}
	return f.verification[id], nil
}

// ConsumeVerificationToken atomically marks the token consumed and stamps
// the user's channel verification timestamp; already-consumed rows conflict.
func (f *fakeStore) ConsumeVerificationToken(_ context.Context, tokenID, userID uuid.UUID, channel VerificationChannel, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.verification[tokenID]
	if !ok {
		return ErrUserNotFound
	}
	if t.ConsumedAt != nil {
		return ErrVerificationTokenConsumed
	}
	u, ok := f.users[userID]
	if !ok {
		return ErrUserNotFound
	}
	t.ConsumedAt = &at
	f.verification[tokenID] = t
	switch channel {
	case ChannelEmail:
		u.EmailVerifiedAt = &at
	case ChannelPhone:
		u.PhoneVerifiedAt = &at
	default:
		return fmt.Errorf("unknown verification channel %q", channel)
	}
	u.UpdatedAt = at
	f.users[userID] = u
	return nil
}

// VerificationTokenStats returns the token count created at or after since
// plus the most recent creation time overall for the user+channel.
func (f *fakeStore) VerificationTokenStats(_ context.Context, userID uuid.UUID, channel VerificationChannel, since time.Time) (int, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count, latest := 0, time.Time{}
	for _, t := range f.verification {
		if t.UserID != userID || t.Channel != channel {
			continue
		}
		if !t.CreatedAt.Before(since) {
			count++
		}
		if t.CreatedAt.After(latest) {
			latest = t.CreatedAt
		}
	}
	return count, latest, nil
}

// ListUsers returns a newest-first page of users with roles loaded.
func (f *fakeStore) ListUsers(_ context.Context, limit, offset int) ([]User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]User, 0, len(f.users))
	for _, u := range f.users {
		cp := u
		cp.Roles = nil
		for _, g := range f.grants[u.ID] {
			cp.Roles = append(cp.Roles, g.Role)
		}
		all = append(all, cp)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() < all[j].ID.String()
	})
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

// setStatus mutates a stored user's status for scenario tests.
func (f *fakeStore) setStatus(id uuid.UUID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	u.Status = status
	f.users[id] = u
}

// expireSession forces a session's expiry into the past.
func (f *fakeStore) expireSession(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	s.ExpiresAt = time.Now().Add(-time.Minute)
	f.sessions[id] = s
}

// ageAbsoluteSession forces a session's ABSOLUTE expiry into the past while
// leaving the idle expiry valid — the 91st-day scenario of issue #81.
func (f *fakeStore) ageAbsoluteSession(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[id]
	s.AbsoluteExpiresAt = time.Now().Add(-time.Minute)
	f.sessions[id] = s
}

// ageUser backdates the account's creation so the verification grace window
// measured from CreatedAt has (or has not) ended — the boundary control for
// the issue #86 refresh gate.
func (f *fakeStore) ageUser(id uuid.UUID, createdAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[id]
	u.CreatedAt = createdAt
	f.users[id] = u
}

// expireVerificationToken forces a verification token's expiry into the
// past (the TTL-boundary scenario of issue #86).
func (f *fakeStore) expireVerificationToken(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.verification[id]
	t.ExpiresAt = time.Now().Add(-time.Minute)
	f.verification[id] = t
}

// backdateVerificationToken moves a verification token's creation (and the
// cooldown reference) into the past without expiring it.
func (f *fakeStore) backdateVerificationToken(id uuid.UUID, createdAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.verification[id]
	t.CreatedAt = createdAt
	f.verification[id] = t
}

// verificationTokens returns every stored verification token (test probe).
func (f *fakeStore) verificationTokens() []VerificationToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]VerificationToken, 0, len(f.verification))
	for _, t := range f.verification {
		out = append(out, t)
	}
	return out
}

// auditsByAction returns the recorded entries with the given action.
func (f *fakeStore) auditsByAction(action string) []AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []AuditEntry
	for _, a := range f.audits {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}
