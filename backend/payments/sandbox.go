package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SandboxProvider is the deterministic mock adapter (issue #62): for tests
// and local development, ALWAYS — it is never a production provider and it
// says so. It is fully deterministic: references are derived from the
// transaction identifier (no randomness), the initiation always succeeds,
// and settlement happens only through an explicitly injected outcome or a
// signed sandbox webhook (see SandboxWebhookBody).
//
// Real credentials are an owner-side secret; until the owner provisions
// Daraja (see README), every deployment runs this adapter.
type SandboxProvider struct {
	// WebhookSecret verifies the sandbox webhook signature. In
	// development/test a documented default is allowed; production must
	// configure it explicitly (enforced by Config validation in cmd).
	WebhookSecret []byte

	// nowFunc is overridable in tests.
	nowFunc func() time.Time

	// outcomes and initiated drive deterministic Query/Refund answers.
	// SetOutcome injects the settlement a Query must report (the webhook
	// path does not consult them: the webhook IS the settlement).
	mu        sync.Mutex
	outcomes  map[uuid.UUID]TransactionStatus
	initiated map[uuid.UUID]int64 // transactionID -> amountMinor
}

// NewSandboxProvider returns the deterministic sandbox adapter.
func NewSandboxProvider(webhookSecret []byte) *SandboxProvider {
	return &SandboxProvider{
		WebhookSecret: webhookSecret,
		nowFunc:       time.Now,
		outcomes:      make(map[uuid.UUID]TransactionStatus),
		initiated:     make(map[uuid.UUID]int64),
	}
}

// Name implements Provider.
func (p *SandboxProvider) Name() string { return "sandbox" }

// SandboxProviderRef derives the deterministic provider reference for a
// transaction: "SBX-<transaction uuid>". Exported for tests and the
// local-dev webhook helper.
func SandboxProviderRef(transactionID uuid.UUID) string {
	return "SBX-" + transactionID.String()
}

// sandboxRefToTransaction parses a sandbox reference back to the
// transaction id.
func sandboxRefToTransaction(ref string) (uuid.UUID, error) {
	const prefix = "SBX-"
	if len(ref) <= len(prefix) {
		return uuid.Nil, fmt.Errorf("payments: sandbox reference %q is malformed", ref)
	}
	id, err := uuid.Parse(ref[len(prefix):])
	if err != nil {
		return uuid.Nil, fmt.Errorf("payments: sandbox reference %q does not carry a transaction id", ref)
	}
	return id, nil
}

// SetOutcome injects the settlement that Query reports for transactionID.
// It is the deterministic counterpart of a settlement webhook and exists
// for tests (reconciliation convergence) and local tooling.
func (p *SandboxProvider) SetOutcome(transactionID uuid.UUID, status TransactionStatus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outcomes[transactionID] = status
}

// Initiate implements Provider. The sandbox accepts every valid request:
// its only rejections are invalid money or missing identifiers, so tests
// prove the domain's own validation, not mock randomness. The initiation
// returns PENDING — settlement arrives via webhook (or an injected
// outcome for the reconciliation path).
func (p *SandboxProvider) Initiate(_ context.Context, req InitiateRequest) (InitiateResult, error) {
	if req.IntentID == uuid.Nil || req.TransactionID == uuid.Nil {
		return InitiateResult{}, providerRejected("sandbox: intent_id and transaction_id are required")
	}
	if err := req.Amount.Validate(); err != nil {
		return InitiateResult{}, providerRejected("sandbox: %v", err)
	}
	p.mu.Lock()
	p.initiated[req.TransactionID] = req.Amount.AmountMinor
	p.mu.Unlock()
	return InitiateResult{
		Status:      TxPending,
		ProviderRef: SandboxProviderRef(req.TransactionID),
	}, nil
}

// Query implements Provider: it reports the injected outcome for the
// transaction, or PENDING when none was injected (the default world where
// nothing settled yet).
func (p *SandboxProvider) Query(_ context.Context, providerRef string) (QueryResult, error) {
	txID, err := sandboxRefToTransaction(providerRef)
	if err != nil {
		return QueryResult{}, providerRejected("sandbox: %v", err)
	}
	p.mu.Lock()
	status := p.outcomes[txID]
	amount := p.initiated[txID]
	p.mu.Unlock()

	result := QueryResult{ProviderRef: providerRef, Currency: CurrencyKES}
	switch status {
	case TxSucceeded:
		result.Status = TxSucceeded
		result.AmountMinor = amount
		result.ProviderTransactionRef = "SBXR-" + txID.String()
		result.OccurredAt = p.nowFunc()
		return result, nil
	case TxFailed:
		result.Status = TxFailed
		result.AmountMinor = amount
		result.FailureReason = "sandbox injected failure"
		result.OccurredAt = p.nowFunc()
		return result, nil
	default:
		result.Status = TxPending
		return result, nil
	}
}

// Refund implements Provider: the sandbox completes every refund request
// that carries valid money, deterministically.
func (p *SandboxProvider) Refund(_ context.Context, req RefundRequest) (RefundResult, error) {
	if req.RefundID == uuid.Nil || req.IntentID == uuid.Nil {
		return RefundResult{}, providerRejected("sandbox: refund_id and intent_id are required")
	}
	if err := req.Amount.Validate(); err != nil {
		return RefundResult{}, providerRejected("sandbox: %v", err)
	}
	return RefundResult{
		Status:      RefundCompleted,
		ProviderRef: "SBXRF-" + req.RefundID.String(),
	}, nil
}

// WebhookEvent implements Provider: HMAC verification (the trust boundary)
// precedes any parsing; the body must be the normalized envelope naming
// the sandbox provider.
func (p *SandboxProvider) WebhookEvent(r *http.Request, body []byte, now time.Time) (WebhookPayload, error) {
	if err := verifyWebhookSignature(p.WebhookSecret, body, r.Header.Get(WebhookSignatureHeader), now); err != nil {
		return WebhookPayload{}, err
	}
	return parseWebhookPayload(body, p.Name())
}

// SandboxWebhookBody builds the normalized sandbox webhook body for a
// settlement event. Exported for tests and local development: curl the
// webhook route with this body and its signature (SignWebhookPayload) to
// settle a sandbox payment.
func SandboxWebhookBody(event, providerRef string, amountMinor int64, failureReason string, occurredAt time.Time) ([]byte, error) {
	body, err := json.Marshal(webhookEnvelope{
		Event:         event,
		Provider:      "sandbox",
		ProviderRef:   providerRef,
		AmountMinor:   amountMinor,
		Currency:      CurrencyKES,
		FailureReason: failureReason,
		OccurredAt:    occurredAt,
	})
	if err != nil {
		return nil, fmt.Errorf("payments: build sandbox webhook body: %w", err)
	}
	return body, nil
}
