package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// ErrUserNotFound is returned by store lookups when the requested user or
// session does not exist. Callers map it to their own response semantics so
// that "unknown email" and "bad password" stay indistinguishable.
var ErrUserNotFound = errors.New("identity: user or session not found")

// isNotFound reports whether err stems from a missing user or session.
func isNotFound(err error) bool {
	return errors.Is(err, ErrUserNotFound)
}

// SessionStore is the persistence contract of the identity domain. The
// Service and Issuer depend only on this interface; PostgresStore is the
// production implementation and tests use in-memory fakes.
type SessionStore interface {
	// CreateSession persists a new refresh session.
	CreateSession(ctx context.Context, s Session) error
	// GetSessionByRefreshHash returns the session owning the given
	// hex-encoded SHA-256 refresh-token digest.
	GetSessionByRefreshHash(ctx context.Context, hash string) (Session, error)
	// RevokeSession marks the session revoked (sets revoked_at).
	RevokeSession(ctx context.Context, id uuid.UUID) error
	// GetUser returns the user with roles loaded.
	GetUser(ctx context.Context, id uuid.UUID) (User, error)
	// GetUserByEmail returns the user with roles loaded; matching is
	// case-insensitive.
	GetUserByEmail(ctx context.Context, email string) (User, error)
	// CreateUser inserts the user and its initial roles in one transaction.
	CreateUser(ctx context.Context, u *User) error
	// AssignRole grants role to the user, optionally tenant-scoped.
	AssignRole(ctx context.Context, userID uuid.UUID, role Role, tenantID *uuid.UUID) error
	// InsertAudit appends one audit record.
	InsertAudit(ctx context.Context, entry AuditEntry) error
}

// PostgresStore implements SessionStore against the identity domain's
// tables (users, user_roles, sessions, audit_log) using a pgx pool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps an opened pgx pool (platform.NewPostgres) into a
// SessionStore. The pool is owned by the caller; Close shuts it down.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Close closes the underlying connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// CreateUser inserts the user and every role in u.Roles (defaulting to
// CUSTOMER when none are set) inside one transaction. Emails are stored
// lowercased; empty phone values are stored as NULL. A duplicate email or
// phone surfaces as platform.ErrConflict.
func (s *PostgresStore) CreateUser(ctx context.Context, u *User) error {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	u.Phone = strings.TrimSpace(u.Phone)
	roles := u.Roles
	if len(roles) == 0 {
		roles = []Role{RoleCustomer}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx,
		`INSERT INTO users (id, email, phone, password_hash, full_name, status)
                 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)
                 RETURNING created_at, updated_at`,
		u.ID, u.Email, u.Phone, u.PasswordHash, u.FullName, u.Status,
	).Scan(&u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if conflict := mapUserConflict(err); conflict != nil {
			return conflict
		}
		return fmt.Errorf("insert user: %w", err)
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx,
			`INSERT INTO user_roles (user_id, role, tenant_id) VALUES ($1, $2, $3)`,
			u.ID, role, (*uuid.UUID)(nil),
		); err != nil {
			if conflict := mapUserConflict(err); conflict != nil {
				return conflict
			}
			return fmt.Errorf("insert role %s: %w", role, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create user: %w", err)
	}
	u.Roles = roles
	return nil
}

// mapUserConflict translates user-table unique violations into application
// conflicts; other errors pass through unmapped.
func mapUserConflict(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return nil
	}
	switch pgErr.ConstraintName {
	case "users_email_key":
		return platform.ErrConflict("email address is already registered")
	case "users_phone_key":
		return platform.ErrConflict("phone number is already registered")
	default:
		return platform.ErrConflict("user already exists")
	}
}

// AssignRole grants role to the user, optionally scoped to a tenant.
// Unknown roles are rejected client-side; duplicates surface as
// platform.ErrConflict.
func (s *PostgresStore) AssignRole(ctx context.Context, userID uuid.UUID, role Role, tenantID *uuid.UUID) error {
	if _, ok := rolePriority[role]; !ok {
		return platform.ErrValidation(fmt.Sprintf("unknown role %q", role),
			platform.FieldError{Field: "role", Issue: "invalid"})
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO user_roles (user_id, role, tenant_id) VALUES ($1, $2, $3)`,
		userID, role, tenantID,
	); err != nil {
		if platform.IsUniqueViolation(err) {
			return platform.ErrConflict("role already assigned")
		}
		return fmt.Errorf("assign role: %w", err)
	}
	return nil
}

// GetRoleGrants returns the user's role grants ordered from the earliest
// assignment. It implements the optional RoleGrantsProvider capability used
// to resolve the tenant_id token claim.
func (s *PostgresStore) GetRoleGrants(ctx context.Context, userID uuid.UUID) ([]RoleGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT role, tenant_id FROM user_roles WHERE user_id = $1 ORDER BY created_at, id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list role grants: %w", err)
	}
	defer rows.Close()

	grants := []RoleGrant{}
	for rows.Next() {
		var role string
		var tenant *uuid.UUID
		if err := rows.Scan(&role, &tenant); err != nil {
			return nil, fmt.Errorf("scan role grant: %w", err)
		}
		grants = append(grants, RoleGrant{Role: Role(role), TenantID: tenant})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list role grants: %w", err)
	}
	return grants, nil
}

// GetUser returns the user with the given id and all roles loaded.
func (s *PostgresStore) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, email, COALESCE(phone, '') AS phone, password_hash, full_name, status, created_at, updated_at
                 FROM users WHERE id = $1`, id)
	u, err := scanUser(row)
	if err != nil {
		return User{}, err
	}
	grants, err := s.GetRoleGrants(ctx, u.ID)
	if err != nil {
		return User{}, err
	}
	u.Roles = rolesFromGrants(grants)
	return u, nil
}

// GetUserByEmail returns the user with the given email address (matched
// case-insensitively) and all roles loaded.
func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, email, COALESCE(phone, '') AS phone, password_hash, full_name, status, created_at, updated_at
                 FROM users WHERE email = LOWER($1)`, strings.TrimSpace(email))
	u, err := scanUser(row)
	if err != nil {
		return User{}, err
	}
	grants, err := s.GetRoleGrants(ctx, u.ID)
	if err != nil {
		return User{}, err
	}
	u.Roles = rolesFromGrants(grants)
	return u, nil
}

// rolesFromGrants projects role grants onto the plain role list carried by
// User.
func rolesFromGrants(grants []RoleGrant) []Role {
	roles := make([]Role, 0, len(grants))
	for _, g := range grants {
		roles = append(roles, g.Role)
	}
	return roles
}

// scanUser hydrates a User from one row of the shared user SELECT.
func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Phone, &u.PasswordHash, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// CreateSession persists a new refresh session; created_at is set by the
// database default.
func (s *PostgresStore) CreateSession(ctx context.Context, sess Session) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, refresh_token_hash, device_name, ip_address, expires_at)
                 VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.ID, sess.UserID, sess.RefreshTokenHash, sess.DeviceName, sess.IPAddress, sess.ExpiresAt,
	); err != nil {
		if platform.IsUniqueViolation(err) {
			return platform.ErrConflict("session already exists")
		}
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSessionByRefreshHash returns the session owning the given refresh-token
// digest, or ErrUserNotFound when none matches.
func (s *PostgresStore) GetSessionByRefreshHash(ctx context.Context, hash string) (Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, refresh_token_hash, device_name, ip_address, expires_at, revoked_at
                 FROM sessions WHERE refresh_token_hash = $1`, hash,
	).Scan(&sess.ID, &sess.UserID, &sess.RefreshTokenHash, &sess.DeviceName, &sess.IPAddress, &sess.ExpiresAt, &sess.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrUserNotFound
		}
		return Session{}, fmt.Errorf("get session by refresh hash: %w", err)
	}
	return sess, nil
}

// RevokeSession sets revoked_at on the session with the given id. It is
// idempotent for already-revoked sessions; unknown ids yield ErrUserNotFound.
func (s *PostgresStore) RevokeSession(ctx context.Context, id uuid.UUID) error {
	cmd, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// InsertAudit appends one audit record. Empty metadata is stored as '{}'.
func (s *PostgresStore) InsertAudit(ctx context.Context, entry AuditEntry) error {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, object_type, object_id, tenant_id, metadata)
                 VALUES ($1, $2, $3, $4, $5, $6)`,
		entry.ActorID, entry.Action, entry.ObjectType, entry.ObjectID, entry.TenantID, metadata,
	); err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}
