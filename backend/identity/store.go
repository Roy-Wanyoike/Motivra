package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// ErrVerificationTokenConsumed is returned by ConsumeVerificationToken when
// the token was already consumed (single-use rule, issue #86). The service
// maps it to the same invalid-token response as every other rejection so
// token state is never disclosed.
var ErrVerificationTokenConsumed = errors.New("identity: verification token already consumed")

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
	// hex-encoded HMAC-SHA256 refresh-token digest.
	GetSessionByRefreshHash(ctx context.Context, hash string) (Session, error)
	// RevokeSession marks the session revoked (sets revoked_at).
	RevokeSession(ctx context.Context, id uuid.UUID) error
	// GetUser returns the user with roles loaded.
	GetUser(ctx context.Context, id uuid.UUID) (User, error)
	// ListUsers returns a newest-first page of users with roles loaded,
	// limited to limit rows after skipping offset.
	ListUsers(ctx context.Context, limit, offset int) ([]User, error)
	// GetUserByEmail returns the user with roles loaded; matching is
	// case-insensitive.
	GetUserByEmail(ctx context.Context, email string) (User, error)
	// CreateUser inserts the user and its initial roles in one transaction.
	CreateUser(ctx context.Context, u *User) error
	// AssignRole grants role to the user, optionally tenant-scoped.
	AssignRole(ctx context.Context, userID uuid.UUID, role Role, tenantID *uuid.UUID) error
	// InsertAudit appends one audit record.
	InsertAudit(ctx context.Context, entry AuditEntry) error

	// UpdatePassword replaces the user's password hash (issue #86
	// password-change flow). Unknown users yield ErrUserNotFound.
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string) error
	// RevokeUserSessions revokes every live session of the user and
	// returns how many rows it revoked.
	RevokeUserSessions(ctx context.Context, userID uuid.UUID) (int, error)

	// CreateVerificationToken persists one single-use verification token
	// row (issue #86).
	CreateVerificationToken(ctx context.Context, t VerificationToken) error
	// GetVerificationTokenByHash returns the token owning the given keyed
	// HMAC digest, or ErrUserNotFound when none matches.
	GetVerificationTokenByHash(ctx context.Context, hash string) (VerificationToken, error)
	// ConsumeVerificationToken atomically marks the token consumed
	// (single-use) and stamps the user's channel verification timestamp
	// in one transaction. Already-consumed tokens yield
	// ErrVerificationTokenConsumed; unknown tokens ErrUserNotFound.
	ConsumeVerificationToken(ctx context.Context, tokenID, userID uuid.UUID, channel VerificationChannel, at time.Time) error
	// VerificationTokenStats returns how many tokens the user has for the
	// channel created at or after since, plus the creation time of the
	// most recent token overall (zero when there is none). The pair backs
	// the resend limit and resend cooldown respectively.
	VerificationTokenStats(ctx context.Context, userID uuid.UUID, channel VerificationChannel, since time.Time) (count int, latest time.Time, err error)
}

// PostgresStore implements SessionStore against the identity domain's
// tables (users, user_roles, sessions, verification_tokens, audit_log)
// using a pgx pool.
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
		`SELECT id, email, COALESCE(phone, '') AS phone, password_hash, full_name, status,
                        email_verified_at, phone_verified_at, created_at, updated_at
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
		`SELECT id, email, COALESCE(phone, '') AS phone, password_hash, full_name, status,
                        email_verified_at, phone_verified_at, created_at, updated_at
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

// ListUsers returns a newest-first page of users with roles loaded, limited
// to limit rows after skipping offset. Roles are fetched in one batched
// query to avoid per-user round trips.
func (s *PostgresStore) ListUsers(ctx context.Context, limit, offset int) ([]User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, email, COALESCE(phone, '') AS phone, password_hash, full_name, status,
                        email_verified_at, phone_verified_at, created_at, updated_at
                 FROM users ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users := []User{}
	for rows.Next() {
		u, scanErr := scanUser(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list users: %w", err)
	}
	rows.Close()

	if len(users) == 0 {
		return users, nil
	}
	ids := make([]uuid.UUID, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	grantsByUser, err := s.batchRoleGrants(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range users {
		users[i].Roles = rolesFromGrants(grantsByUser[users[i].ID])
	}
	return users, nil
}

// batchRoleGrants loads role grants for many users in one query, grouped by
// user id.
func (s *PostgresStore) batchRoleGrants(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]RoleGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, role, tenant_id FROM user_roles WHERE user_id = ANY($1) ORDER BY created_at, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("list role grants: %w", err)
	}
	defer rows.Close()

	grants := map[uuid.UUID][]RoleGrant{}
	for rows.Next() {
		var userID uuid.UUID
		var role string
		var tenant *uuid.UUID
		if err := rows.Scan(&userID, &role, &tenant); err != nil {
			return nil, fmt.Errorf("scan role grant: %w", err)
		}
		grants[userID] = append(grants[userID], RoleGrant{Role: Role(role), TenantID: tenant})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list role grants: %w", err)
	}
	return grants, nil
}

// scanUser hydrates a User from one row of the shared user SELECT.
func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Phone, &u.PasswordHash, &u.FullName, &u.Status,
		&u.EmailVerifiedAt, &u.PhoneVerifiedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// CreateSession persists a new refresh session; created_at is set by the
// database default. AbsoluteExpiresAt is explicit so rotation can carry the
// predecessor's non-extendable bound forward (issue #81).
func (s *PostgresStore) CreateSession(ctx context.Context, sess Session) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (id, user_id, refresh_token_hash, device_name, ip_address, expires_at, absolute_expires_at)
                 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.ID, sess.UserID, sess.RefreshTokenHash, sess.DeviceName, sess.IPAddress, sess.ExpiresAt, sess.AbsoluteExpiresAt,
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
		`SELECT id, user_id, refresh_token_hash, device_name, ip_address, expires_at, absolute_expires_at, revoked_at
                 FROM sessions WHERE refresh_token_hash = $1`, hash,
	).Scan(&sess.ID, &sess.UserID, &sess.RefreshTokenHash, &sess.DeviceName, &sess.IPAddress, &sess.ExpiresAt, &sess.AbsoluteExpiresAt, &sess.RevokedAt)
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

// UpdatePassword replaces the user's password hash and bumps updated_at.
// Unknown users yield ErrUserNotFound.
func (s *PostgresStore) UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string) error {
	cmd, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`,
		userID, passwordHash,
	)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// RevokeUserSessions revokes every live session of the user (password-change
// hygiene, issue #86) and returns the number of rows it revoked.
func (s *PostgresStore) RevokeUserSessions(ctx context.Context, userID uuid.UUID) (int, error) {
	cmd, err := s.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		userID,
	)
	if err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	return int(cmd.RowsAffected()), nil
}

// CreateVerificationToken persists one single-use verification token row.
// created_at is set by the database default.
func (s *PostgresStore) CreateVerificationToken(ctx context.Context, t VerificationToken) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO verification_tokens (id, user_id, channel, token_hash, expires_at)
                 VALUES ($1, $2, $3, $4, $5)`,
		t.ID, t.UserID, t.Channel, t.TokenHash, t.ExpiresAt,
	); err != nil {
		if platform.IsUniqueViolation(err) {
			return platform.ErrConflict("verification token already exists")
		}
		return fmt.Errorf("create verification token: %w", err)
	}
	return nil
}

// GetVerificationTokenByHash returns the token owning the given keyed HMAC
// digest, or ErrUserNotFound when none matches.
func (s *PostgresStore) GetVerificationTokenByHash(ctx context.Context, hash string) (VerificationToken, error) {
	var t VerificationToken
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, channel, token_hash, expires_at, consumed_at, created_at
                 FROM verification_tokens WHERE token_hash = $1`, hash,
	).Scan(&t.ID, &t.UserID, &t.Channel, &t.TokenHash, &t.ExpiresAt, &t.ConsumedAt, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VerificationToken{}, ErrUserNotFound
		}
		return VerificationToken{}, fmt.Errorf("get verification token by hash: %w", err)
	}
	return t, nil
}

// ConsumeVerificationToken atomically marks the token consumed (single-use)
// and stamps the user's channel verification timestamp in one transaction.
// The consumed_at update is conditional, so concurrent verifies of the same
// token cannot both succeed.
func (s *PostgresStore) ConsumeVerificationToken(ctx context.Context, tokenID, userID uuid.UUID, channel VerificationChannel, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin consume verification token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cmd, err := tx.Exec(ctx,
		`UPDATE verification_tokens SET consumed_at = $2 WHERE id = $1 AND consumed_at IS NULL`,
		tokenID, at,
	)
	if err != nil {
		return fmt.Errorf("consume verification token: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrVerificationTokenConsumed
	}
	switch channel {
	case ChannelEmail:
		_, err = tx.Exec(ctx,
			`UPDATE users SET email_verified_at = $2, updated_at = $2 WHERE id = $1 AND email_verified_at IS NULL`,
			userID, at)
	case ChannelPhone:
		_, err = tx.Exec(ctx,
			`UPDATE users SET phone_verified_at = $2, updated_at = $2 WHERE id = $1 AND phone_verified_at IS NULL`,
			userID, at)
	default:
		return fmt.Errorf("unknown verification channel %q", channel)
	}
	if err != nil {
		return fmt.Errorf("mark %s verified: %w", channel, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit consume verification token: %w", err)
	}
	return nil
}

// VerificationTokenStats returns how many tokens the user has for the
// channel created at or after since (the resend-limit window) and the
// creation time of the most recent token overall (zero when none exists —
// the resend-cooldown reference).
func (s *PostgresStore) VerificationTokenStats(ctx context.Context, userID uuid.UUID, channel VerificationChannel, since time.Time) (int, time.Time, error) {
	var count int
	var latest *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE created_at >= $3), max(created_at)
                 FROM verification_tokens WHERE user_id = $1 AND channel = $2`,
		userID, channel, since,
	).Scan(&count, &latest)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("verification token stats: %w", err)
	}
	if latest == nil {
		return count, time.Time{}, nil
	}
	return count, *latest, nil
}
