// Package payments implements the Motivra Payments domain (issue #62,
// Wave 5 A17): a PaymentIntent + Transaction ledger over integer minor
// units of KES, a provider abstraction with an M-Pesa Daraja adapter and a
// deterministic sandbox adapter, idempotent webhook processing behind
// signature verification, a reconciliation fallback that repairs missed
// webhooks and a full-refund flow.
//
// Money discipline: every monetary value is an int64 of minor units
// (cents) — float money is forbidden package-wide and the arithmetic in
// money.go is overflow/negative-checked and test-enforced.
package payments

import (
	"time"

	"github.com/google/uuid"
)

// PaymentIntent is the aggregate root of the domain: the declared intent
// to collect an amount of money from a customer, its lifecycle status and
// its ledger linkage. Intents are created by staff principals; the tenant
// and creator come exclusively from the authenticated claims.
type PaymentIntent struct {
	ID uuid.UUID
	// TenantID is the owning organization (from the creator's claims) when
	// the payment was initiated under a tenant; nil otherwise.
	TenantID *uuid.UUID
	// CustomerID is the paying user's id when known; it is the read path by
	// which the customer sees their own payments.
	CustomerID *uuid.UUID
	// CreatedBy is the staff principal that initiated the payment. Always
	// the authenticated caller — never client input.
	CreatedBy uuid.UUID
	// AmountMinor is the payment amount in KES minor units (cents). An
	// intent's amount is immutable after creation (database-enforced).
	AmountMinor int64
	// Currency is the ISO 4217 code, always KES for this domain.
	Currency string
	// Status is one of the IntentStatus constants on the state machine.
	Status IntentStatus
	// Provider names the payment provider adapter that settles this intent
	// ("sandbox" or "daraja").
	Provider string
	// ProviderRef is the provider-side reference returned by Initiate. It
	// is the handle reconciliation queries by. Empty until an initiation
	// reached the provider.
	ProviderRef string
	// JobID and VehicleID are optional cross-domain references held by
	// identifier only (no cross-domain FKs, ADR-0003).
	JobID     *uuid.UUID
	VehicleID *uuid.UUID
	// Description is a short human-readable memo (redacted free text).
	Description string
	// FailureReason records why the payment failed (from the provider
	// webhook/query, treated as external input and length-capped).
	FailureReason string
	SucceededAt   *time.Time
	FailedAt      *time.Time
	CanceledAt    *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Transaction is one attempt to settle an intent through a provider. A
// transaction is created PENDING when the provider accepts the initiation
// (or FAILED when the provider rejects it synchronously) and moves to
// SUCCEEDED/FAILED exactly once — via a verified webhook or via the
// reconciliation query fallback. Rows are guarded-update: amount, currency,
// provider, provider_ref and intent_id are immutable from creation, and a
// terminal status is final (database triggers enforce both).
type Transaction struct {
	ID       uuid.UUID
	IntentID uuid.UUID
	Provider string
	// ProviderRef is the provider's transaction reference (e.g. the M-Pesa
	// MpesaReceiptNumber). Immutable after creation.
	ProviderRef   string
	Status        TransactionStatus
	AmountMinor   int64
	Currency      string
	FailureReason string
	// OccurredAt is the provider-attested completion time (validated
	// external input, bounded future skew), nil until terminal.
	OccurredAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Refund is a full refund of a succeeded intent. Rows are guarded-update
// like transactions: REQUESTED moves to COMPLETED or FAILED exactly once
// and every other column is immutable. A second refund is refused while a
// REQUESTED one is in flight (partial unique index) or after one completed.
type Refund struct {
	ID            uuid.UUID
	IntentID      uuid.UUID
	AmountMinor   int64
	Currency      string
	Status        RefundStatus
	Provider      string
	ProviderRef   string
	Reason        string
	FailureReason string
	// RequestedBy is the staff principal that requested the refund.
	RequestedBy *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IntentTransition is one row of the append-only intent transition audit
// log. Rows are never updated or deleted (database trigger).
type IntentTransition struct {
	ID         int64
	IntentID   uuid.UUID
	FromStatus IntentStatus
	ToStatus   IntentStatus
	ActorID    *uuid.UUID
	Reason     string
	CreatedAt  time.Time
}

// WebhookPayload is the normalized webhook event every provider adapter
// produces from its own wire format. It is treated as external input:
// every field is validated before it may touch the ledger.
type WebhookPayload struct {
	// Event is one of the normalized webhook event names
	// ("transaction.succeeded" or "transaction.failed").
	Event string
	// Provider echoes the provider the route was addressed to; a mismatch
	// with the route is a rejection.
	Provider string
	// IntentID and TransactionID identify the domain rows to transition.
	IntentID      uuid.UUID
	TransactionID uuid.UUID
	// ProviderRef is the provider-side transaction reference echoed back.
	ProviderRef string
	// AmountMinor/Currency are the provider-attested values; AmountMinor
	// must equal the ledger transaction's amount exactly.
	AmountMinor int64
	Currency    string
	// FailureReason is set on transaction.failed events.
	FailureReason string
	// OccurredAt is the provider-attested completion time, bounded by a
	// future-skew tolerance.
	OccurredAt time.Time
}
