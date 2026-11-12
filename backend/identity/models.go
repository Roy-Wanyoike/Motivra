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

// Technician skill-verification lifecycle values (issue #166), stored in
// the users.verification_status column. The empty string on the User
// struct means "never assessed" — the column is NULL — and every reader
// (profile read model, GET /v1/technician/verification-status) renders it
// as VerificationStatusUnverified. Only "verified" means dispatch-eligible
// (issue #167 fails closed on every other value, so "pending" and
// "rejected" are safe lifecycle extensions); the vocabulary is owned by
// this domain and documented as the contract of record in
// contracts/identity/openapi.yaml.
const (
	VerificationStatusUnverified = "unverified"
	VerificationStatusPending    = "pending"
	VerificationStatusVerified   = "verified"
	VerificationStatusRejected   = "rejected"
)

// IsValidVerificationStatus reports whether status is one of the fixed
// verification lifecycle values. The empty string (never assessed) is a
// valid STORAGE state but not a valid DECISION: status writes go through
// the approve/reject API and always carry an explicit value.
func IsValidVerificationStatus(status string) bool {
	switch status {
	case VerificationStatusUnverified, VerificationStatusPending,
		VerificationStatusVerified, VerificationStatusRejected:
		return true
	}
	return false
}

// User is an identity account. PasswordHash is the PHC-encoded argon2id
// string and is never serialized to clients; Profile clears it.
// EmailVerifiedAt and PhoneVerifiedAt are nil until the corresponding
// channel has been confirmed through a verification token (issue #86);
// while any present channel is unverified, session minting is gated to the
// post-registration grace window (Issuer verification grace).
type User struct {
	ID              uuid.UUID
	Email           string
	Phone           string // empty when the user has no phone number
	FullName        string
	PasswordHash    string
	Status          string
	Roles           []Role
	EmailVerifiedAt *time.Time
	PhoneVerifiedAt *time.Time
	// VerificationStatus is the technician skill-verification attribute
	// (issue #166): empty means never assessed (the column is NULL and
	// readers render "unverified"). It is written only by the verification
	// API together with its append-only decision record and audit entry.
	VerificationStatus string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// verificationSatisfied reports whether every contact channel the account
// carries has been verified. Email is mandatory at registration, phone is
// optional — an account with no phone is fully gated on email alone.
func (u User) verificationSatisfied() bool {
	if u.EmailVerifiedAt == nil {
		return false
	}
	if u.Phone != "" && u.PhoneVerifiedAt == nil {
		return false
	}
	return true
}

// Session is a refresh-token session row. RefreshTokenHash is the hex-encoded
// keyed HMAC-SHA256 digest of the opaque client refresh token; the token
// itself is never persisted. RevokedAt is non-nil once the session (or its
// token via rotation) is no longer usable. CreatedAt drives the newest-first
// ordering of the session management API (issue #87).
type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	RefreshTokenHash  string
	DeviceName        string
	IPAddress         string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time // rotation never extends this (issue #81)
	RevokedAt         *time.Time
}

// RoleGrant is a role assignment, optionally scoped to a tenant. A nil
// TenantID marks a personal (platform-wide) grant.
type RoleGrant struct {
	Role     Role
	TenantID *uuid.UUID
}

// VerificationChannel names the contact channel a verification token
// proves ownership of (issue #86).
type VerificationChannel string

// The two verification channels. Email is always present; phone tokens
// exist only for accounts registered with a phone number.
const (
	ChannelEmail VerificationChannel = "email"
	ChannelPhone VerificationChannel = "phone"
)

// IsValidVerificationChannel reports whether channel is one of the fixed
// channel values.
func IsValidVerificationChannel(channel VerificationChannel) bool {
	switch channel {
	case ChannelEmail, ChannelPhone:
		return true
	}
	return false
}

// VerificationToken is one single-use proof-of-ownership token for a
// contact channel. TokenHash is the keyed HMAC-SHA256 digest of the opaque
// client token (the raw token is delivered out-of-band and never
// persisted). A row is consumed by the first successful verification;
// expired or consumed rows are rejected indistinguishably.
type VerificationToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Channel    VerificationChannel
	TokenHash  string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

// TechnicianVerification is one append-only skill-verification decision
// (issue #166): a staff principal records an approve/reject outcome for a
// technician, with a reason and credential-evidence REFERENCES (never the
// evidence itself). Rows are never mutated — a correction is a new
// superseding row — and every row commits together with its audit_log
// entry and the users.verification_status update in one transaction.
// AssessmentTemplateID and AssessmentSessionID reference the Inspections
// context by id only (no cross-domain FKs, ADR-0003).
type TechnicianVerification struct {
	ID                   uuid.UUID
	TechnicianID         uuid.UUID
	Status               string
	Reason               string
	ActorID              uuid.UUID
	AssessmentTemplateID *uuid.UUID
	AssessmentSessionID  *uuid.UUID
	EvidenceRefs         []string
	CreatedAt            time.Time
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
