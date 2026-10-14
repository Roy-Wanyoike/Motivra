package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Provider is the settlement abstraction every payment adapter implements
// (issue #62): initiate a collection, query its status (the reconciliation
// fallback when a webhook is missed) and refund a settled payment.
// Implementations own their wire formats; the domain works purely in
// normalized types.
//
// Provider responses are EXTERNAL INPUT: adapters validate them and the
// service re-validates every field again before it may touch the ledger.
type Provider interface {
	// Name is the stable adapter identifier stored on intents,
	// transactions and refunds ("sandbox", "daraja").
	Name() string

	// Initiate requests a new collection from the payer. A synchronous
	// rejection returns an error; an accepted initiation returns a PENDING
	// result carrying the provider-side reference the callback/query
	// correlates by.
	Initiate(ctx context.Context, req InitiateRequest) (InitiateResult, error)

	// Query asks the provider for the current status of a transaction by
	// its provider-side reference. It returns TxPending while the provider
	// has not settled the transaction, else the terminal status.
	Query(ctx context.Context, providerRef string) (QueryResult, error)

	// Refund requests a (full) refund of a settled payment.
	Refund(ctx context.Context, req RefundRequest) (RefundResult, error)

	// WebhookEvent verifies the webhook request's authenticity (the trust
	// boundary — signature or shared-secret verification is mandatory and
	// fail-closed) and normalizes its body into a WebhookPayload. An
	// unverified, malformed or foreign payload is an error, never a
	// partially-decoded structure.
	WebhookEvent(r *http.Request, body []byte, now time.Time) (WebhookPayload, error)
}

// InitiateRequest is the normalized initiate call.
type InitiateRequest struct {
	// IntentID and TransactionID identify the domain rows the provider
	// interaction belongs to. The transaction id pre-exists the call so
	// adapters can derive deterministic references (the sandbox does).
	IntentID      uuid.UUID
	TransactionID uuid.UUID
	// Amount is the collection amount. Adapters must refuse invalid money.
	Amount Money
	// Description is a short memo for the payer's statement view.
	Description string
	// PayerPhone is the paying party's MSISDN, needed by STK-push
	// providers at call time. It is used transiently and NEVER persisted:
	// the ledger stores identifiers and provider references only (PII
	// minimization, issue #62 security section).
	PayerPhone string
}

// InitiateResult is the provider's answer to an accepted initiation.
type InitiateResult struct {
	// Status is TxPending when the provider accepted the initiation.
	Status TransactionStatus
	// ProviderRef is the provider-side handle subsequent callbacks and
	// queries correlate by (e.g. the Daraja CheckoutRequestID).
	ProviderRef string
	// ProviderTransactionRef is the provider's own transaction identifier
	// when one is issued up front (usually only known after settlement).
	ProviderTransactionRef string
}

// QueryResult is the provider's answer to a status query.
type QueryResult struct {
	// Status is TxPending while unsettled, else the terminal status.
	Status TransactionStatus
	// ProviderRef echoes the queried reference.
	ProviderRef string
	// ProviderTransactionRef is the settlement receipt when known
	// (e.g. the M-Pesa MpesaReceiptNumber).
	ProviderTransactionRef string
	// AmountMinor/Currency attest the settled amount; zero when pending.
	AmountMinor int64
	Currency    string
	// FailureReason is set when Status is TxFailed.
	FailureReason string
	// OccurredAt is the provider-attested settlement time.
	OccurredAt time.Time
}

// RefundRequest is the normalized refund call (full refund first, issue
// #62; partial refunds are a later wave).
type RefundRequest struct {
	RefundID uuid.UUID
	IntentID uuid.UUID
	// ProviderRef is the original payment's provider-side reference.
	ProviderRef string
	// Amount is the refund amount (the full settled amount today).
	Amount Money
	// Reason is a short operator memo.
	Reason string
}

// RefundResult is the provider's answer to a refund request.
type RefundResult struct {
	// Status is RefundCompleted or RefundFailed.
	Status RefundStatus
	// ProviderRef is the provider-side refund reference.
	ProviderRef string
	// FailureReason is set when Status is RefundFailed.
	FailureReason string
}

// ErrProviderUnavailable marks a provider-side transport or 5xx failure:
// the call may be retried and must not be recorded as a payment failure
// (ARCHITECTURE.md §7: provider down → retry → alternate → reconcile).
var ErrProviderUnavailable = errors.New("payments: provider unavailable")

// ErrProviderRejected marks a deterministic provider-side rejection of the
// request itself (bad credentials, malformed request, refused payment).
var ErrProviderRejected = errors.New("payments: provider rejected the request")

// providerUnavailable classifies transport/5xx responses.
func providerUnavailable(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProviderUnavailable}, args...)...)
}

// providerRejected classifies deterministic rejections.
func providerRejected(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProviderRejected}, args...)...)
}
