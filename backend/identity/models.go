// Package identity implements Motivra's authentication core: user accounts,
// the fixed RBAC role set, argon2id password hashing, refresh-session
// management and HS256 access-token issuance (ADR-0004). The HTTP layer for
// these operations lives in the identity service command; every other
// service validates tokens through backend/platform only.
package identity

import (
	"time"

	"github.com/google/uuid"
)

// Role is one of the nine fixed Motivra roles (ADR-0004). Roles are plain
// strings at the boundary so they serialize directly into JWT claims and
// into the platform RBAC middleware.
type Role string

// The fixed role set. No other values are accepted by the store or the
// database CHECK constraint.
const (
	RoleCustomer   Role = "CUSTOMER"
	RoleTechnician Role = "TECHNICIAN"
	RoleDispatcher Role = "DISPATCHER"
	RoleGarage     Role = "GARAGE"
	RoleFleetAdmin Role = "FLEET_ADMIN"
	RoleSupport    Role = "SUPPORT"
	RoleFinance    Role = "FINANCE"
	RoleAdmin      Role = "ADMIN"
	RoleSuperAdmin Role = "SUPER_ADMIN"
)

// User status values stored in the users.status column.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDeleted   = "deleted"
)

// User is an identity account. PasswordHash is the PHC-encoded argon2id
// string and is never serialized to clients; Profile clears it.
type User struct {
	ID           uuid.UUID
	Email        string
	Phone        string // empty when the user has no phone number
	FullName     string
	PasswordHash string
	Status       string
	Roles        []Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session is a refresh-token session row. RefreshTokenHash is the hex-encoded
// SHA-256 digest of the opaque client refresh token; the token itself is
// never persisted. RevokedAt is non-nil once the session (or its token via
// rotation) is no longer usable.
type Session struct {
	ID               uuid.UUID
	UserID           uuid.UUID
	RefreshTokenHash string
	DeviceName       string
	IPAddress        string
	ExpiresAt        time.Time
	RevokedAt        *time.Time
}

// RoleGrant is a role assignment, optionally scoped to a tenant. A nil
// TenantID marks a personal (platform-wide) grant.
type RoleGrant struct {
	Role     Role
	TenantID *uuid.UUID
}

// HighestRole returns the highest-priority role among the given roles using
// the fixed precedence SUPER_ADMIN > ADMIN > FINANCE > SUPPORT >
// FLEET_ADMIN > GARAGE > DISPATCHER > TECHNICIAN > CUSTOMER. It returns an
// empty string when roles is empty.
func HighestRole(roles []Role) string {
	best := -1
	result := ""
	for _, r := range roles {
		p, ok := rolePriority[r]
		if ok && p > best {
			best = p
			result = string(r)
		}
	}
	return result
}

// rolePriority maps each role to its precedence (higher wins).
var rolePriority = map[Role]int{
	RoleSuperAdmin: 8,
	RoleAdmin:      7,
	RoleFinance:    6,
	RoleSupport:    5,
	RoleFleetAdmin: 4,
	RoleGarage:     3,
	RoleDispatcher: 2,
	RoleTechnician: 1,
	RoleCustomer:   0,
}
