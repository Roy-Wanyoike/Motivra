package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// EventDomain is the JetStream domain prefix used when publishing: events
// go out on motivra.payments.<event_type> (ADR-0002).
const EventDomain = "payments"

// Published event types, following "<aggregate>.<event>.v<n>".
const (
	EventIntentCreated          = "intent.created.v1"
	EventTransactionSucceededV1 = "transaction.succeeded.v1"
	EventTransactionFailedV1    = "transaction.failed.v1"
	EventRefundCompleted        = "refund.completed.v1"
	EventRefundFailed           = "refund.failed.v1"
)

// Validation bounds.
const (
	// MaxDescriptionLength caps the free-text payment memo. It is redacted
	// free text (platform.RedactFreeText): phones/emails pasted into it
	// never reach the ledger or the logs raw.
	MaxDescriptionLength = 200
	// MaxRefundReasonLength caps the refund reason memo.
	MaxRefundReasonLength = 500
	// DefaultIntentsLimit / MaxIntentsLimit bound list pagination.
	DefaultIntentsLimit = 50
	MaxIntentsLimit     = 200
)

// Domain errors the HTTP layer maps onto platform statuses.
var (
	// ErrInvalidInput is wrapped by every request-validation error.
	ErrInvalidInput = errors.New("payments: invalid input")
	// ErrNotRefundable is returned when a refund is requested on an intent
	// that has not settled successfully.
	ErrNotRefundable = errors.New("payments: intent is not refundable")
	// ErrAlreadyRefunded is returned when a second refund is requested.
	ErrAlreadyRefunded = errors.New("payments: intent already refunded")
	// ErrWebhookUnauthorized wraps every signature-verification failure:
	// the trust boundary refused the delivery.
	ErrWebhookUnauthorized = errors.New("payments: webhook unauthorized")
	// ErrWebhookRejected wraps every payload/mismatch rejection: the
	// delivery authenticated but its content cannot be applied.
	ErrWebhookRejected = errors.New("payments: webhook rejected")
)

// invalidInput builds an ErrInvalidInput-wrapped validation error.
func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidInput}, args...)...)
}

func errWebhookUnauthorized(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrWebhookUnauthorized}, args...)...)
}

func errWebhookRejected(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrWebhookRejected}, args...)...)
}

// webhookUnauthorizedCause reports whether err is a signature failure (as
// opposed to a payload rejection) so the HTTP layer can answer 401 vs 422.
func webhookUnauthorizedCause(err error) bool {
	return errors.Is(err, ErrWebhookUnauthorized)
}

// IntentCreatedPayload is the payload of intent.created.v1.
type IntentCreatedPayload struct {
	IntentID    uuid.UUID  `json:"intent_id"`
	AmountMinor int64      `json:"amount_minor"`
	Currency    string     `json:"currency"`
	Provider    string     `json:"provider"`
	CustomerID  *uuid.UUID `json:"customer_id,omitempty"`
	JobID       *uuid.UUID `json:"job_id,omitempty"`
	VehicleID   *uuid.UUID `json:"vehicle_id,omitempty"`
}

// TransactionSettledPayload is the payload of transaction.succeeded.v1 and
// transaction.failed.v1.
type TransactionSettledPayload struct {
	IntentID        uuid.UUID `json:"intent_id"`
	TransactionID   uuid.UUID `json:"transaction_id"`
	Provider        string    `json:"provider"`
	ProviderRef     string    `json:"provider_ref,omitempty"`
	ProviderReceipt string    `json:"provider_receipt,omitempty"`
	AmountMinor     int64     `json:"amount_minor"`
	Currency        string    `json:"currency"`
	SettledBy       string    `json:"settled_by"` // "webhook" | "reconciliation"
	FailureReason   string    `json:"failure_reason,omitempty"`
}

// RefundCompletedPayload is the payload of refund.completed.v1.
type RefundCompletedPayload struct {
	IntentID    uuid.UUID `json:"intent_id"`
	RefundID    uuid.UUID `json:"refund_id"`
	AmountMinor int64     `json:"amount_minor"`
	Currency    string    `json:"currency"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref,omitempty"`
}

// RefundFailedPayload is the payload of refund.failed.v1.
type RefundFailedPayload struct {
	IntentID      uuid.UUID `json:"intent_id"`
	RefundID      uuid.UUID `json:"refund_id"`
	AmountMinor   int64     `json:"amount_minor"`
	Currency      string    `json:"currency"`
	FailureReason string    `json:"failure_reason"`
}

// InitiateInput is the service-level initiation request: the HTTP layer
// fills it from the authenticated claims and the validated body — never
// the other way round.
type InitiateInput struct {
	// TenantID and CreatedBy come from the claims.
	TenantID  *uuid.UUID
	CreatedBy uuid.UUID
	// CustomerID optionally links the payer.
	CustomerID *uuid.UUID
	// AmountMinor/Currency are the integer-ledger amount.
	AmountMinor int64
	Currency    string
	// Provider names the adapter ("sandbox" default, "daraja" when the
	// owner provisioned credentials). Empty selects the sandbox.
	Provider string
	// JobID/VehicleID are optional cross-domain references.
	JobID     *uuid.UUID
	VehicleID *uuid.UUID
	// Description is the payer-facing memo (redacted free text).
	Description string
	// PayerPhone is used transiently by STK-push providers and is never
	// persisted (PII minimization).
	PayerPhone string
}

// InitiatedPayment returns the persisted aggregate after an initiation.
type InitiatedPayment struct {
	Intent      PaymentIntent
	Transaction *Transaction // nil when the provider was unreachable
}

// WebhookResult reports what a webhook delivery did.
type WebhookResult struct {
	// Status is "applied" (this delivery settled the transaction) or
	// "duplicate" (an earlier delivery already did).
	Status string `json:"status"`
	// Duplicate is true when the idempotency key had already recorded an
	// outcome for this payload.
	Duplicate     bool      `json:"duplicate"`
	IntentID      uuid.UUID `json:"intent_id"`
	TransactionID uuid.UUID `json:"transaction_id"`
}

// Webhook statuses.
const (
	WebhookStatusApplied   = "applied"
	WebhookStatusDuplicate = "duplicate"
)

// Service is the payments domain service. It owns the three state
// machines (every status move goes through CanTransition* before it
// touches the store), the webhook idempotency discipline, the refund flow,
// domain metrics and event publishing. publisher and metrics may be nil
// (tests, offline tooling); events and instrumentation are simply skipped
// then.
type Service struct {
	store     Store
	providers map[string]Provider
	publisher platform.Publisher
	metrics   *Metrics

	// DefaultProvider names the adapter initiations select when the
	// caller leaves the provider unset ("sandbox" unless the deployment
	// configured Daraja and asked for it to be the default; cmd wires
	// MOTIVRA_PAYMENTS_PROVIDER). Per-intent providers are recorded on
	// the intent, so webhooks, queries and refunds always use the
	// adapter the payment was initiated with.
	DefaultProvider string
}

// NewService returns a Service over store with the named providers.
// publisher and metrics may be nil.
func NewService(store Store, providers map[string]Provider, publisher platform.Publisher, metrics *Metrics) *Service {
	return &Service{store: store, providers: providers, publisher: publisher, metrics: metrics, DefaultProvider: ProviderSandbox}
}

// Provider returns the named adapter, or the service's default when name
// is empty.
func (s *Service) Provider(name string) (Provider, error) {
	if name == "" {
		name = s.DefaultProvider
	}
	if name == "" {
		name = "sandbox"
	}
	p, ok := s.providers[name]
	if !ok {
		return nil, invalidInput("provider %q is not configured (configured: %v)", name, s.providerNames())
	}
	return p, nil
}

func (s *Service) providerNames() []string {
	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}
	return names
}

// InitiatePayment creates the intent in CREATED, calls the provider, and
// records the outcome: an accepted initiation persists a PENDING
// transaction and moves the intent to PROCESSING (intent.created.v1
// published); a deterministic provider rejection persists a FAILED
// transaction, moves the intent to FAILED (intent.created.v1 +
// transaction.failed.v1 published) and returns the wrapped
// ErrProviderRejected; an unreachable provider leaves the intent CREATED
// with no transaction row and returns the wrapped ErrProviderUnavailable
// (ARCHITECTURE.md §7: provider down is retryable, not a payment failure).
func (s *Service) InitiatePayment(ctx context.Context, in InitiateInput) (InitiatedPayment, error) {
	amount, err := NewMoney(in.AmountMinor, in.Currency)
	if err != nil {
		return InitiatedPayment{}, invalidInput("initiate payment: %v", err)
	}
	if in.CreatedBy == uuid.Nil {
		return InitiatedPayment{}, invalidInput("initiate payment: creator is required")
	}
	if err := validateOptionalID("customer_id", in.CustomerID); err != nil {
		return InitiatedPayment{}, err
	}
	if err := validateOptionalID("job_id", in.JobID); err != nil {
		return InitiatedPayment{}, err
	}
	if err := validateOptionalID("vehicle_id", in.VehicleID); err != nil {
		return InitiatedPayment{}, err
	}
	provider, err := s.Provider(in.Provider)
	if err != nil {
		return InitiatedPayment{}, err
	}

	intent := &PaymentIntent{
		TenantID:    in.TenantID,
		CustomerID:  in.CustomerID,
		CreatedBy:   in.CreatedBy,
		AmountMinor: amount.AmountMinor,
		Currency:    amount.Currency,
		Status:      IntentCreated,
		Provider:    provider.Name(),
		JobID:       in.JobID,
		VehicleID:   in.VehicleID,
		Description: platform.RedactFreeText(in.Description),
	}
	if len(in.Description) > MaxDescriptionLength {
		return InitiatedPayment{}, invalidInput("initiate payment: description exceeds %d characters", MaxDescriptionLength)
	}
	if err := s.store.CreateIntent(ctx, intent); err != nil {
		return InitiatedPayment{}, fmt.Errorf("payments: initiate payment: %w", err)
	}

	// The transaction id pre-exists the provider call so deterministic
	// adapters can derive references from it.
	txID := uuid.New()
	initiated, err := provider.Initiate(ctx, InitiateRequest{
		IntentID:      intent.ID,
		TransactionID: txID,
		Amount:        amount,
		Description:   intent.Description,
		PayerPhone:    in.PayerPhone,
	})
	if err != nil {
		if errors.Is(err, ErrProviderUnavailable) {
			// The provider was never reached: no ledger attempt row, the
			// intent stays CREATED, the caller may retry.
			return InitiatedPayment{Intent: *intent}, fmt.Errorf("payments: initiate payment %s: %w", intent.ID, err)
		}
		// Deterministic rejection: record the failed attempt honestly.
		failed := &Transaction{
			ID:            txID,
			IntentID:      intent.ID,
			Provider:      provider.Name(),
			Status:        TxFailed,
			AmountMinor:   amount.AmountMinor,
			Currency:      amount.Currency,
			FailureReason: failureReasonFrom(err),
		}
		if insertErr := s.store.InsertTransaction(ctx, failed); insertErr != nil {
			return InitiatedPayment{Intent: *intent}, fmt.Errorf("payments: initiate payment %s: record rejection: %w", intent.ID, insertErr)
		}
		if statusErr := s.store.UpdateIntentStatus(ctx, intent.ID, IntentCreated, IntentFailed, &in.CreatedBy, "provider rejected the initiation"); statusErr != nil {
			return InitiatedPayment{Intent: *intent}, fmt.Errorf("payments: initiate payment %s: %w", intent.ID, statusErr)
		}
		intent.Status = IntentFailed
		intent.FailureReason = failed.FailureReason
		s.metrics.PaymentFailed()
		_ = s.publish(ctx, EventIntentCreated, intent.ID, &in.CreatedBy, tenantString(in.TenantID), IntentCreatedPayload{
			IntentID:    intent.ID,
			AmountMinor: intent.AmountMinor,
			Currency:    intent.Currency,
			Provider:    intent.Provider,
			CustomerID:  intent.CustomerID,
			JobID:       intent.JobID,
			VehicleID:   intent.VehicleID,
		})
		_ = s.publish(ctx, EventTransactionFailedV1, failed.ID, &in.CreatedBy, tenantString(in.TenantID), TransactionSettledPayload{
			IntentID:      intent.ID,
			TransactionID: failed.ID,
			Provider:      failed.Provider,
			AmountMinor:   failed.AmountMinor,
			Currency:      failed.Currency,
			SettledBy:     "initiation",
			FailureReason: failed.FailureReason,
		})
		return InitiatedPayment{Intent: *intent, Transaction: failed}, fmt.Errorf("payments: initiate payment %s: %w", intent.ID, err)
	}

	tx := &Transaction{
		ID:          txID,
		IntentID:    intent.ID,
		Provider:    provider.Name(),
		ProviderRef: initiated.ProviderRef,
		Status:      initiated.Status,
		AmountMinor: amount.AmountMinor,
		Currency:    amount.Currency,
	}
	if err := s.store.InsertTransaction(ctx, tx); err != nil {
		return InitiatedPayment{Intent: *intent}, fmt.Errorf("payments: initiate payment %s: %w", intent.ID, err)
	}
	if err := s.store.UpdateIntentStatus(ctx, intent.ID, IntentCreated, IntentProcessing, &in.CreatedBy, "provider accepted the initiation"); err != nil {
		return InitiatedPayment{Intent: *intent}, fmt.Errorf("payments: initiate payment %s: %w", intent.ID, err)
	}
	intent.Status = IntentProcessing
	intent.ProviderRef = initiated.ProviderRef
	s.metrics.PaymentInitiated()

	if err := s.publish(ctx, EventIntentCreated, intent.ID, &in.CreatedBy, tenantString(in.TenantID), IntentCreatedPayload{
		IntentID:    intent.ID,
		AmountMinor: intent.AmountMinor,
		Currency:    intent.Currency,
		Provider:    intent.Provider,
		CustomerID:  intent.CustomerID,
		JobID:       intent.JobID,
		VehicleID:   intent.VehicleID,
	}); err != nil {
		return InitiatedPayment{Intent: *intent, Transaction: tx}, err
	}
	return InitiatedPayment{Intent: *intent, Transaction: tx}, nil
}

// failureReasonFrom extracts a bounded, log-safe reason from a provider
// error (never the raw error chain, which can carry response bodies).
func failureReasonFrom(err error) string {
	reason := err.Error()
	if len(reason) > maxWebhookFailureReason {
		reason = reason[:maxWebhookFailureReason]
	}
	return reason
}

// HandleWebhook verifies, validates, correlates and applies one provider
// webhook delivery exactly once (issue #62): the signature check is the
// trust boundary, the payload is external input validated against the
// ledger, and the idempotency key absorbs every replay. header/body come
// from the webhook route; now is injectable for tests.
func (s *Service) HandleWebhook(ctx context.Context, providerName string, header http.Header, body []byte, now time.Time) (WebhookResult, error) {
	started := time.Now()
	provider, err := s.Provider(providerName)
	if err != nil {
		return WebhookResult{}, err
	}

	// 1. Trust boundary: signature verification precedes any parsing.
	payload, err := provider.WebhookEvent(header, body, now)
	if err != nil {
		s.metrics.WebhookReceived(WebhookOutcomeRejected)
		if errors.Is(err, ErrWebhookUnauthorized) {
			return WebhookResult{}, err
		}
		return WebhookResult{}, fmt.Errorf("%w: %v", ErrWebhookRejected, err)
	}

	// 2. Correlate the provider reference to a ledger transaction.
	tx, err := s.store.GetTransactionByProviderRef(ctx, provider.Name(), payload.ProviderRef)
	if err != nil {
		s.metrics.WebhookReceived(WebhookOutcomeRejected)
		return WebhookResult{}, fmt.Errorf("%w: no transaction for %s reference %q: %v",
			ErrWebhookRejected, provider.Name(), payload.ProviderRef, err)
	}

	// 3. Validate the external input against the ledger: the provider's
	// attested amount must equal the attempted amount exactly, the event
	// provider must match the transaction's provider, and the intent must
	// be the transaction's intent.
	if tx.Provider != provider.Name() {
		s.metrics.WebhookReceived(WebhookOutcomeRejected)
		return WebhookResult{}, fmt.Errorf("%w: transaction %s belongs to provider %q, not %q",
			ErrWebhookRejected, tx.ID, tx.Provider, provider.Name())
	}
	if !EqualSameCurrency(Money{AmountMinor: payload.AmountMinor, Currency: payload.Currency},
		Money{AmountMinor: tx.AmountMinor, Currency: tx.Currency}) {
		s.metrics.WebhookReceived(WebhookOutcomeRejected)
		return WebhookResult{}, fmt.Errorf("%w: attested amount %d %s does not equal the attempted %d %s on transaction %s",
			ErrWebhookRejected, payload.AmountMinor, payload.Currency, tx.AmountMinor, tx.Currency, tx.ID)
	}
	if tx.IntentID != payload.IntentID && payload.IntentID != uuid.Nil {
		s.metrics.WebhookReceived(WebhookOutcomeRejected)
		return WebhookResult{}, fmt.Errorf("%w: webhook intent %s does not match transaction %s intent %s",
			ErrWebhookRejected, payload.IntentID, tx.ID, tx.IntentID)
	}

	// 4. Idempotency: the same payload replayed N times applies exactly
	// once. The key is deterministic per (provider, event, transaction);
	// the payload hash is stored for tamper evidence.
	key := fmt.Sprintf("webhook:%s:%s:%s", provider.Name(), payload.Event, tx.ID)
	claim, err := s.store.ClaimIdempotencyKey(ctx, key, "webhook", sha256Hex(body))
	if err != nil {
		return WebhookResult{}, fmt.Errorf("payments: webhook %s: %w", tx.ID, err)
	}
	if !claim.Claimed {
		if claim.ExistingPayloadHash != sha256Hex(body) {
			s.metrics.WebhookReceived(WebhookOutcomeRejected)
			return WebhookResult{}, fmt.Errorf("%w: replay of %s carries a different payload than the recorded claim",
				ErrWebhookRejected, key)
		}
		if claim.ExistingResult == "" {
			// Claimed but not yet completed: a concurrent delivery is
			// applying it. The provider will retry.
			return WebhookResult{}, fmt.Errorf("payments: webhook %s: %w", tx.ID, ErrConflict)
		}
		s.metrics.WebhookReceived(WebhookOutcomeDuplicate)
		s.metrics.WebhookDuplicate()
		return WebhookResult{
			Status:        WebhookStatusDuplicate,
			Duplicate:     true,
			IntentID:      tx.IntentID,
			TransactionID: tx.ID,
		}, nil
	}

	// 5. Apply the settlement exactly once.
	newTxStatus := TxSucceeded
	newIntentStatus := IntentSucceeded
	eventType := EventTransactionSucceededV1
	settledBy := "webhook"
	if payload.Event == WebhookEventTransactionFailed {
		newTxStatus = TxFailed
		newIntentStatus = IntentFailed
		eventType = EventTransactionFailedV1
	}
	_, err = s.store.ApplyTransactionOutcome(ctx, ApplyOutcomeParams{
		TransactionID:   tx.ID,
		NewTxStatus:     newTxStatus,
		NewIntentStatus: newIntentStatus,
		ProviderReceipt: payload.ProviderReceipt,
		FailureReason:   payload.FailureReason,
		OccurredAt:      payload.OccurredAt,
		Reason:          settledBy,
	})
	if err != nil {
		return WebhookResult{}, fmt.Errorf("payments: webhook %s: %w", tx.ID, err)
	}
	// An "already applied" outcome (false) means an earlier delivery won
	// a race; the state is already correct and the key completion below
	// records it.
	if newTxStatus == TxSucceeded {
		s.metrics.PaymentSucceeded()
	} else {
		s.metrics.PaymentFailed()
	}
	if err := s.store.CompleteIdempotencyKey(ctx, key, "applied"); err != nil {
		return WebhookResult{}, fmt.Errorf("payments: webhook %s: %w", tx.ID, err)
	}
	s.metrics.WebhookReceived(WebhookOutcomeApplied)
	s.metrics.WebhookProcessed(time.Since(started).Seconds())

	if err := s.publish(ctx, eventType, tx.ID, nil, tenantString(nil), TransactionSettledPayload{
		IntentID:        tx.IntentID,
		TransactionID:   tx.ID,
		Provider:        tx.Provider,
		ProviderRef:     tx.ProviderRef,
		ProviderReceipt: payload.ProviderReceipt,
		AmountMinor:     tx.AmountMinor,
		Currency:        tx.Currency,
		SettledBy:       settledBy,
		FailureReason:   payload.FailureReason,
	}); err != nil {
		return WebhookResult{Status: WebhookStatusApplied, IntentID: tx.IntentID, TransactionID: tx.ID}, err
	}
	return WebhookResult{Status: WebhookStatusApplied, IntentID: tx.IntentID, TransactionID: tx.ID}, nil
}

// validateOptionalID requires optional uuid references to be set to a real
// identifier when present.
func validateOptionalID(field string, id *uuid.UUID) error {
	if id != nil && *id == uuid.Nil {
		return invalidInput("%s is not a valid identifier", field)
	}
	return nil
}

// tenantString renders the optional tenant for the event envelope.
func tenantString(t *uuid.UUID) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// correlationFromContext sources the event correlation_id from the
// platform server's request-ID middleware (ADR-0002: correlation is
// propagated from the originating request). It is empty for non-HTTP
// callers.
func correlationFromContext(ctx context.Context) string {
	return middleware.GetReqID(ctx)
}

// publish sends one domain event on the payments domain. It is a no-op
// when no publisher is configured. Publishing failures surface to the
// caller: events are part of the domain contract, not best-effort logging.
// Every attempt is counted (success/failure) on the domain metrics.
func (s *Service) publish(ctx context.Context, eventType string, aggregateID uuid.UUID, actorID *uuid.UUID, tenantID string, payload any) error {
	if s.publisher == nil {
		return nil
	}
	actor := ""
	if actorID != nil && *actorID != uuid.Nil {
		actor = actorID.String()
	}
	e, err := platform.NewEvent(eventType, aggregateID.String(), tenantID, actor, correlationFromContext(ctx), payload)
	if err == nil {
		err = s.publisher.Publish(ctx, EventDomain, e)
		if err != nil {
			err = fmt.Errorf("payments: publish %s: %w", eventType, err)
		}
	} else {
		err = fmt.Errorf("payments: build %s event: %w", eventType, err)
	}
	s.metrics.EventPublished(eventType, err)
	return err
}

// sha256Hex is the payload-hash form the idempotency keys carry.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RefundPayment refunds a SUCCEEDED intent in full (issue #62: full
// refund first). It refuses intents in any other state, intents that
// already carry a completed or in-flight refund, and providers without a
// refund implementation. The refund row is persisted REQUESTED before the
// provider is called, so a provider-unavailable failure leaves an honest
// REQUESTED row (visible to ops) rather than nothing.
func (s *Service) RefundPayment(ctx context.Context, intentID uuid.UUID, reason string, actorID uuid.UUID, scope Scope) (Refund, error) {
	intent, err := s.store.GetIntent(ctx, intentID, scope)
	if err != nil {
		return Refund{}, fmt.Errorf("payments: refund: %w", err)
	}
	if intent.Status != IntentSucceeded {
		return Refund{}, fmt.Errorf("payments: refund intent %s is %s: %w", intentID, intent.Status, ErrNotRefundable)
	}
	if len(reason) > MaxRefundReasonLength {
		return Refund{}, invalidInput("refund: reason exceeds %d characters", MaxRefundReasonLength)
	}
	existing, err := s.store.LatestRefundForIntent(ctx, intentID, scope)
	switch {
	case err == nil:
		if existing.Status != RefundFailed {
			return Refund{}, fmt.Errorf("payments: refund intent %s already has refund %s in status %s: %w",
				intentID, existing.ID, existing.Status, ErrAlreadyRefunded)
		}
	case errors.Is(err, ErrRefundNotFound):
		// No refund yet: proceed.
	default:
		return Refund{}, fmt.Errorf("payments: refund: %w", err)
	}
	provider, err := s.Provider(intent.Provider)
	if err != nil {
		return Refund{}, err
	}

	refund := &Refund{
		IntentID:    intent.ID,
		AmountMinor: intent.AmountMinor,
		Currency:    intent.Currency,
		Status:      RefundRequested,
		Provider:    intent.Provider,
		Reason:      reason,
	}
	if actorID != uuid.Nil {
		refund.RequestedBy = &actorID
	}
	if err := s.store.InsertRefund(ctx, refund); err != nil {
		return Refund{}, fmt.Errorf("payments: refund: %w", err)
	}

	result, err := provider.Refund(ctx, RefundRequest{
		RefundID:    refund.ID,
		IntentID:    intent.ID,
		ProviderRef: intent.ProviderRef,
		Amount:      Money{AmountMinor: intent.AmountMinor, Currency: intent.Currency},
		Reason:      reason,
	})
	if err != nil {
		if errors.Is(err, ErrProviderUnavailable) {
			// The refund row stays REQUESTED (append-only ledger): the
			// provider may still process it; ops reviews it. Retrying the
			// request is refused until the row resolves.
			return *refund, fmt.Errorf("payments: refund %s: %w", refund.ID, err)
		}
		if statusErr := s.store.UpdateRefundStatus(ctx, refund.ID, RefundRequested, RefundFailed, "", failureReasonFrom(err)); statusErr != nil {
			return *refund, fmt.Errorf("payments: refund %s: record failure: %w", refund.ID, statusErr)
		}
		refund.Status = RefundFailed
		refund.FailureReason = failureReasonFrom(err)
		_ = s.publish(ctx, EventRefundFailed, refund.ID, &actorID, tenantString(intent.TenantID), RefundFailedPayload{
			IntentID:      intent.ID,
			RefundID:      refund.ID,
			AmountMinor:   refund.AmountMinor,
			Currency:      refund.Currency,
			FailureReason: refund.FailureReason,
		})
		return *refund, fmt.Errorf("payments: refund %s: %w", refund.ID, err)
	}

	if err := s.store.UpdateRefundStatus(ctx, refund.ID, RefundRequested, result.Status, result.ProviderRef, result.FailureReason); err != nil {
		return *refund, fmt.Errorf("payments: refund %s: %w", refund.ID, err)
	}
	refund.Status = result.Status
	refund.ProviderRef = result.ProviderRef
	s.metrics.PaymentRefunded()

	if err := s.publish(ctx, EventRefundCompleted, refund.ID, &actorID, tenantString(intent.TenantID), RefundCompletedPayload{
		IntentID:    intent.ID,
		RefundID:    refund.ID,
		AmountMinor: refund.AmountMinor,
		Currency:    refund.Currency,
		Provider:    intent.Provider,
		ProviderRef: result.ProviderRef,
	}); err != nil {
		return *refund, err
	}
	return *refund, nil
}

// GetIntent returns the intent when visible to scope.
func (s *Service) GetIntent(ctx context.Context, id uuid.UUID, scope Scope) (PaymentIntent, error) {
	return s.store.GetIntent(ctx, id, scope)
}

// Intents lists intents visible to scope with the service's pagination
// discipline (defaults and caps).
func (s *Service) Intents(ctx context.Context, scope Scope, filter IntentFilter, limit, offset int) ([]PaymentIntent, error) {
	if limit <= 0 {
		limit = DefaultIntentsLimit
	}
	if limit > MaxIntentsLimit {
		limit = MaxIntentsLimit
	}
	if offset < 0 {
		offset = 0
	}
	if filter.Status != nil {
		switch *filter.Status {
		case IntentCreated, IntentProcessing, IntentSucceeded, IntentFailed, IntentCanceled:
		default:
			return nil, invalidInput("list intents: unknown status %q", *filter.Status)
		}
	}
	return s.store.ListIntents(ctx, scope, filter, limit, offset)
}

// GetTransaction returns one transaction of an intent visible to scope.
func (s *Service) GetTransaction(ctx context.Context, id uuid.UUID, scope Scope) (Transaction, error) {
	return s.store.GetTransaction(ctx, id, scope)
}

// Transactions returns the intent's transactions, oldest first. The
// intent must be visible to scope first (an invisible intent reads as
// missing).
func (s *Service) Transactions(ctx context.Context, intentID uuid.UUID, scope Scope) ([]Transaction, error) {
	if _, err := s.store.GetIntent(ctx, intentID, scope); err != nil {
		return nil, fmt.Errorf("payments: list transactions: %w", err)
	}
	return s.store.ListTransactions(ctx, intentID, scope)
}

// GetRefund returns one refund of an intent visible to scope.
func (s *Service) GetRefund(ctx context.Context, id uuid.UUID, scope Scope) (Refund, error) {
	return s.store.GetRefund(ctx, id, scope)
}
