package identity

import (
	"context"
	"errors"
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
	mu        sync.Mutex
	users     map[uuid.UUID]User
	byEmail   map[string]uuid.UUID
	grants    map[uuid.UUID][]RoleGrant
	sessions  map[uuid.UUID]Session
	byRefresh map[string]uuid.UUID
	audits    []AuditEntry

	failCreateUser bool
	failAudit      bool
}

// newFakeStore returns an empty store ready for use.
func newFakeStore() *fakeStore {
	return &fakeStore{
		users:     map[uuid.UUID]User{},
		byEmail:   map[string]uuid.UUID{},
		grants:    map[uuid.UUID][]RoleGrant{},
		sessions:  map[uuid.UUID]Session{},
		byRefresh: map[string]uuid.UUID{},
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
	return nil
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
