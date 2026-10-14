package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// The service tests pin the domain discipline over a fake store that
// mirrors the PostgresStore semantics (scope-inside-read, expected-status
// conflicts, exactly-once settlement, first-writer idempotency claims):
// webhook replay applies exactly once (N-times test), the signature is the
// trust boundary, refunds are full and single, and tenant isolation holds
// on every read.

// --- capturing publisher -------------------------------------------------

type capturePublisher struct {
	mu      sync.Mutex
	events  []platform.Event
	domains []string
	fail    error
}

func (p *capturePublisher) Publish(_ context.Context, domain string, e platform.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return p.fail
	}
	p.domains = append(p.domains, domain)
	p.events = append(p.events, e)
	return nil
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func (p *capturePublisher) eventTypes(t *testing.T) []string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.events))
	for _, e := range p.events {
		out = append(out, e.EventType)
	}
	return out
}

// eventsOfType returns the payloads of every published event of type.
func eventsOfType[T any](t *testing.T, p *capturePublisher, eventType string) []T {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []T
	for _, e := range p.events {
		if e.EventType == eventType {
			var payload T
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("decode payload of %s: %v", eventType, err)
			}
			out = append(out, payload)
		}
	}
	return out
}

// --- fake store ------------------------------------------------------------

// fakeStore mirrors the PostgresStore semantics the service relies on, so
// the service tests exercise the real discipline without a database.
type fakeStore struct {
	mu          sync.Mutex
	intents     map[uuid.UUID]*PaymentIntent
	byProvider  map[string]uuid.UUID // provider + "|" + provider_ref -> transaction id
	txs         map[uuid.UUID]*Transaction
	refunds     map[uuid.UUID]*Refund
	transitions []IntentTransition
	idempotency map[string]*fakeIdempotencyKey
	now         time.Time
}

type fakeIdempotencyKey struct {
	scope       string
	payloadHash string
	result      string
	completed   bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		intents:     make(map[uuid.UUID]*PaymentIntent),
		byProvider:  make(map[string]uuid.UUID),
		txs:         make(map[uuid.UUID]*Transaction),
		refunds:     make(map[uuid.UUID]*Refund),
		idempotency: make(map[string]*fakeIdempotencyKey),
		now:         time.Now().UTC(),
	}
}

func (f *fakeStore) stamp() time.Time {
	f.now = f.now.Add(time.Millisecond)
	return f.now
}

// intentVisible mirrors the PostgresStore scope predicate for intents.
func intentVisible(i PaymentIntent, scope Scope) bool {
	switch {
	case scope.Unrestricted():
		return true
	case scope.TenantID != nil:
		return i.TenantID != nil && *i.TenantID == *scope.TenantID
	case scope.PrincipalID != nil:
		return i.CreatedBy == *scope.PrincipalID ||
			(i.CustomerID != nil && *i.CustomerID == *scope.PrincipalID)
	default:
		return false // zero scope: fail closed
	}
}

func (f *fakeStore) CreateIntent(_ context.Context, i *PaymentIntent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	f.intents[i.ID] = i
	i.CreatedAt, i.UpdatedAt = f.stamp(), f.stamp()
	return nil
}

func (f *fakeStore) GetIntent(_ context.Context, id uuid.UUID, scope Scope) (PaymentIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[id]
	if !ok || !intentVisible(*i, scope) {
		return PaymentIntent{}, fmt.Errorf("get intent %s: %w", id, ErrIntentNotFound)
	}
	return *i, nil
}

func (f *fakeStore) ListIntents(_ context.Context, scope Scope, filter IntentFilter, limit, offset int) ([]PaymentIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = DefaultIntentsLimit
	}
	if limit > MaxIntentsLimit {
		limit = MaxIntentsLimit
	}
	if offset < 0 {
		offset = 0
	}
	matches := []PaymentIntent{}
	for _, i := range f.intents {
		if !intentVisible(*i, scope) {
			continue
		}
		if filter.Status != nil && i.Status != *filter.Status {
			continue
		}
		if filter.CustomerID != nil && (i.CustomerID == nil || *i.CustomerID != *filter.CustomerID) {
			continue
		}
		if filter.JobID != nil && (i.JobID == nil || *i.JobID != *filter.JobID) {
			continue
		}
		if filter.VehicleID != nil && (i.VehicleID == nil || *i.VehicleID != *filter.VehicleID) {
			continue
		}
		matches = append(matches, *i)
	}
	sort.Slice(matches, func(a, b int) bool {
		if !matches[a].CreatedAt.Equal(matches[b].CreatedAt) {
			return matches[a].CreatedAt.After(matches[b].CreatedAt)
		}
		return matches[a].ID.String() > matches[b].ID.String()
	})
	if offset >= len(matches) {
		return []PaymentIntent{}, nil
	}
	out := matches[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) UpdateIntentStatus(_ context.Context, intentID uuid.UUID, expectedFrom, newStatus IntentStatus, actorID *uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[intentID]
	if !ok {
		return fmt.Errorf("update intent status %s: %w", intentID, ErrIntentNotFound)
	}
	if i.Status != expectedFrom {
		return fmt.Errorf("update intent status %s: status moved to %s: %w", intentID, i.Status, ErrConflict)
	}
	i.Status = newStatus
	i.UpdatedAt = f.stamp()
	switch newStatus {
	case IntentSucceeded:
		t := f.stamp()
		i.SucceededAt = &t
	case IntentFailed:
		t := f.stamp()
		i.FailedAt = &t
	case IntentCanceled:
		t := f.stamp()
		i.CanceledAt = &t
	}
	f.transitions = append(f.transitions, IntentTransition{
		ID: int64(len(f.transitions) + 1), IntentID: intentID, FromStatus: expectedFrom,
		ToStatus: newStatus, ActorID: actorID, Reason: reason, CreatedAt: f.stamp(),
	})
	return nil
}

func (f *fakeStore) InsertTransaction(_ context.Context, t *Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if _, exists := f.txs[t.ID]; exists {
		return fmt.Errorf("insert transaction: duplicate id %s", t.ID)
	}
	if t.ProviderRef != "" {
		key := t.Provider + "|" + t.ProviderRef
		if _, clash := f.byProvider[key]; clash {
			return fmt.Errorf("insert transaction: duplicate provider ref %s", t.ProviderRef)
		}
		f.byProvider[key] = t.ID
	}
	t.CreatedAt, t.UpdatedAt = f.now, f.now
	cp := *t
	f.txs[t.ID] = &cp
	return nil
}

func (f *fakeStore) GetTransaction(_ context.Context, id uuid.UUID, scope Scope) (Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txs[id]
	if !ok {
		return Transaction{}, fmt.Errorf("get transaction %s: %w", id, ErrTransactionNotFound)
	}
	i, ok := f.intents[t.IntentID]
	if !ok || !intentVisible(*i, scope) {
		return Transaction{}, fmt.Errorf("get transaction %s: %w", id, ErrTransactionNotFound)
	}
	return *t, nil
}

func (f *fakeStore) GetTransactionByProviderRef(_ context.Context, provider, providerRef string) (Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byProvider[provider+"|"+providerRef]
	if !ok {
		return Transaction{}, fmt.Errorf("get transaction by ref %s: %w", providerRef, ErrTransactionNotFound)
	}
	return *f.txs[id], nil
}

func (f *fakeStore) ListTransactions(_ context.Context, intentID uuid.UUID, scope Scope) ([]Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.intents[intentID]
	if !ok || !intentVisible(*i, scope) {
		return nil, fmt.Errorf("list transactions: %w", ErrIntentNotFound)
	}
	out := []Transaction{}
	for _, t := range f.txs {
		if t.IntentID == intentID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.Before(out[b].CreatedAt) })
	return out, nil
}

// ApplyTransactionOutcome mirrors the Postgres exactly-once settlement:
// already-settled-to-the-same-status is an idempotent no-op (false, nil),
// any other non-PENDING start is a conflict, and the intent moves with the
// transaction (or has already converged).
func (f *fakeStore) ApplyTransactionOutcome(_ context.Context, p ApplyOutcomeParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txs[p.TransactionID]
	if !ok {
		return false, fmt.Errorf("apply outcome %s: %w", p.TransactionID, ErrTransactionNotFound)
	}
	if t.Status == p.NewTxStatus {
		return false, nil
	}
	if t.Status != TxPending {
		return false, fmt.Errorf("apply outcome %s: transaction is %s: %w", t.ID, t.Status, ErrConflict)
	}
	t.Status = p.NewTxStatus
	if p.ProviderReceipt != "" {
		t.ProviderReceipt = p.ProviderReceipt
	}
	t.FailureReason = p.FailureReason
	occurred := p.OccurredAt
	if occurred.IsZero() {
		occurred = f.now
	}
	t.OccurredAt = &occurred
	t.UpdatedAt = f.stamp()

	i, ok := f.intents[t.IntentID]
	if !ok {
		return false, fmt.Errorf("apply outcome %s: intent missing: %w", t.ID, ErrIntentNotFound)
	}
	if i.Status == p.NewIntentStatus {
		// Convergence: the intent settled moments earlier through the other
		// path while this transaction row was still PENDING.
		return true, nil
	}
	if i.Status != IntentProcessing {
		return false, fmt.Errorf("apply outcome %s: intent is %s: %w", t.ID, i.Status, ErrConflict)
	}
	i.Status = p.NewIntentStatus
	i.UpdatedAt = f.stamp()
	if p.FailureReason != "" {
		i.FailureReason = p.FailureReason
	}
	switch p.NewIntentStatus {
	case IntentSucceeded:
		tStamp := f.stamp()
		i.SucceededAt = &tStamp
	case IntentFailed:
		tStamp := f.stamp()
		i.FailedAt = &tStamp
	}
	f.transitions = append(f.transitions, IntentTransition{
		ID: int64(len(f.transitions) + 1), IntentID: i.ID, FromStatus: IntentProcessing,
		ToStatus: p.NewIntentStatus, Reason: p.Reason, CreatedAt: f.stamp(),
	})
	return true, nil
}

func (f *fakeStore) InsertRefund(_ context.Context, r *Refund) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	r.Status = RefundRequested
	r.CreatedAt, r.UpdatedAt = f.stamp(), f.stamp()
	cp := *r
	f.refunds[r.ID] = &cp
	return nil
}

func refundVisible(r Refund, f *fakeStore, scope Scope) bool {
	i, ok := f.intents[r.IntentID]
	if !ok {
		return false
	}
	return intentVisible(*i, scope)
}

func (f *fakeStore) GetRefund(_ context.Context, id uuid.UUID, scope Scope) (Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refunds[id]
	if !ok || !refundVisible(*r, f, scope) {
		return Refund{}, fmt.Errorf("get refund %s: %w", id, ErrRefundNotFound)
	}
	return *r, nil
}

func (f *fakeStore) UpdateRefundStatus(_ context.Context, refundID uuid.UUID, expectedFrom, newStatus RefundStatus, providerRef, failureReason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refunds[refundID]
	if !ok {
		return fmt.Errorf("update refund status %s: %w", refundID, ErrRefundNotFound)
	}
	if r.Status != expectedFrom {
		return fmt.Errorf("update refund status %s: status moved to %s: %w", refundID, r.Status, ErrConflict)
	}
	r.Status = newStatus
	if providerRef != "" {
		r.ProviderRef = providerRef
	}
	if failureReason != "" {
		r.FailureReason = failureReason
	}
	r.UpdatedAt = f.stamp()
	return nil
}

func (f *fakeStore) LatestRefundForIntent(_ context.Context, intentID uuid.UUID, scope Scope) (Refund, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var latest *Refund
	for _, r := range f.refunds {
		if r.IntentID != intentID {
			continue
		}
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) {
			cp := *r
			latest = &cp
		}
	}
	if latest == nil || !refundVisible(*latest, f, scope) {
		return Refund{}, fmt.Errorf("latest refund for intent %s: %w", intentID, ErrRefundNotFound)
	}
	return *latest, nil
}

func (f *fakeStore) ClaimIdempotencyKey(_ context.Context, key, scope, payloadHash string) (ClaimResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.idempotency[key]; ok {
		return ClaimResult{Claimed: false, ExistingResult: existing.result, ExistingPayloadHash: existing.payloadHash}, nil
	}
	f.idempotency[key] = &fakeIdempotencyKey{scope: scope, payloadHash: payloadHash}
	return ClaimResult{Claimed: true}, nil
}

func (f *fakeStore) CompleteIdempotencyKey(_ context.Context, key, result string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.idempotency[key]
	if !ok {
		return fmt.Errorf("complete idempotency key %s: %w", key, ErrRefundNotFound)
	}
	k.result = result
	k.completed = true
	return nil
}

func (f *fakeStore) ListStaleIntents(_ context.Context, olderThan time.Time, limit int) ([]StaleIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = DefaultIntentsLimit
	}
	var out []StaleIntent
	for _, i := range f.intents {
		if i.Status != IntentProcessing {
			continue
		}
		var newest *Transaction
		for _, t := range f.txs {
			if t.IntentID != i.ID {
				continue
			}
			if newest == nil || t.CreatedAt.After(newest.CreatedAt) {
				cp := *t
				newest = &cp
			}
		}
		if newest == nil || newest.Status != TxPending || !newest.CreatedAt.Before(olderThan) {
			continue
		}
		out = append(out, StaleIntent{Intent: *i, Transaction: *newest})
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Transaction.CreatedAt.Before(out[b].Transaction.CreatedAt) })
	return out, nil
}

// BackdateTransaction moves a transaction's created_at backwards (the fake
// store's clock marches forward by stamps, so staleness windows need this).
func (f *fakeStore) BackdateTransaction(id uuid.UUID, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.txs[id]; ok {
		t.CreatedAt = t.CreatedAt.Add(-d)
	}
}

// --- fixtures --------------------------------------------------------------

var testCtx = context.Background()

func newTestService(t *testing.T) (*Service, *fakeStore, *capturePublisher, *SandboxProvider) {
	t.Helper()
	store := newFakeStore()
	pub := &capturePublisher{}
	sandbox := NewSandboxProvider([]byte(testWebhookSecret))
	svc := NewService(store, map[string]Provider{sandbox.Name(): sandbox}, pub, nil)
	return svc, store, pub, sandbox
}

// initiateThroughSandbox runs a fresh initiation through the sandbox and
// returns the initiated aggregate.
func initiateThroughSandbox(t *testing.T, svc *Service, tenant *uuid.UUID, creator uuid.UUID, amountMinor int64) (InitiatedPayment, error) {
	t.Helper()
	return svc.InitiatePayment(testCtx, InitiateInput{
		TenantID:    tenant,
		CreatedBy:   creator,
		AmountMinor: amountMinor,
		Currency:    CurrencyKES,
		Description: "integration fixture",
	})
}

// signedSandboxWebhook builds a signed sandbox settlement webhook for tx.
func signedSandboxWebhook(t *testing.T, sandbox *SandboxProvider, event, providerRef string, amountMinor int64, at time.Time) (http.Header, []byte) {
	t.Helper()
	body, err := SandboxWebhookBody(event, providerRef, amountMinor, "", at)
	require.NoError(t, err)
	return signedHeader(t, string(sandbox.WebhookSecret), body, at), body
}

// --- initiation paths -------------------------------------------------------

func TestInitiatePaymentHappyPath(t *testing.T) {
	svc, store, pub, _ := newTestService(t)
	tenant := uuid.New()
	creator := uuid.New()

	initiated, err := initiateThroughSandbox(t, svc, &tenant, creator, 15_000)
	require.NoError(t, err)
	require.Equal(t, IntentProcessing, initiated.Intent.Status)
	require.Equal(t, ProviderSandbox, initiated.Intent.Provider)
	require.NotNil(t, initiated.Transaction)
	require.Equal(t, TxPending, initiated.Transaction.Status)
	require.Equal(t, SandboxProviderRef(initiated.Transaction.ID), initiated.Intent.ProviderRef)

	// The intent.created.v1 event carried the integer amount.
	created := eventsOfType[IntentCreatedPayload](t, pub, EventIntentCreated)
	require.Len(t, created, 1)
	assert.Equal(t, int64(15_000), created[0].AmountMinor)
	assert.Equal(t, CurrencyKES, created[0].Currency)

	// The transition audit holds exactly the CREATED -> PROCESSING move.
	require.Len(t, store.transitions, 1)
	assert.Equal(t, IntentCreated, store.transitions[0].FromStatus)
	assert.Equal(t, IntentProcessing, store.transitions[0].ToStatus)
}

func TestInitiatePaymentValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	creator := uuid.New()

	_, err := svc.InitiatePayment(testCtx, InitiateInput{CreatedBy: creator, AmountMinor: 0, Currency: CurrencyKES})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)

	_, err = svc.InitiatePayment(testCtx, InitiateInput{CreatedBy: creator, AmountMinor: 100, Currency: "USD"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)

	_, err = svc.InitiatePayment(testCtx, InitiateInput{AmountMinor: 100, Currency: CurrencyKES})
	require.Error(t, err, "a zero creator is refused")

	_, err = svc.InitiatePayment(testCtx, InitiateInput{CreatedBy: creator, AmountMinor: 100, Currency: CurrencyKES, Provider: "nonexistent"})
	require.Error(t, err, "an unconfigured provider is refused")
}

// unavailableProvider is the deterministic stand-in for a down provider.
type unavailableProvider struct{ name string }

func (p *unavailableProvider) Name() string { return p.name }
func (p *unavailableProvider) Initiate(context.Context, InitiateRequest) (InitiateResult, error) {
	return InitiateResult{}, providerUnavailable("connection refused")
}
func (p *unavailableProvider) Query(context.Context, string) (QueryResult, error) {
	return QueryResult{}, providerUnavailable("connection refused")
}
func (p *unavailableProvider) Refund(context.Context, RefundRequest) (RefundResult, error) {
	return RefundResult{}, providerUnavailable("connection refused")
}
func (p *unavailableProvider) WebhookEvent(http.Header, []byte, time.Time) (WebhookPayload, error) {
	return WebhookPayload{}, errWebhookUnauthorized("no webhook for a down provider")
}

func TestInitiatePaymentProviderUnavailableLeavesIntentRetryable(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	down := &unavailableProvider{name: "down"}
	svc := NewService(store, map[string]Provider{down.Name(): down}, pub, nil)
	svc.DefaultProvider = "down"

	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{
		CreatedBy: uuid.New(), AmountMinor: 5_000, Currency: CurrencyKES,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderUnavailable)
	// The intent stays CREATED with NO transaction row: retryable, never a
	// payment failure (ARCHITECTURE.md §7).
	assert.Equal(t, IntentCreated, initiated.Intent.Status)
	assert.Nil(t, initiated.Transaction)
	require.Len(t, store.txs, 0)
	assert.Equal(t, 0, pub.count(), "no events for an unreached provider")
}

// rejectingProvider deterministically refuses every initiation.
type rejectingProvider struct{ name string }

func (p *rejectingProvider) Name() string { return p.name }
func (p *rejectingProvider) Initiate(_ context.Context, req InitiateRequest) (InitiateResult, error) {
	return InitiateResult{}, providerRejected("%s: insufficient balance", p.name)
}
func (p *rejectingProvider) Query(context.Context, string) (QueryResult, error) {
	return QueryResult{}, providerRejected("%s: refused", p.name)
}
func (p *rejectingProvider) Refund(context.Context, RefundRequest) (RefundResult, error) {
	return RefundResult{}, providerRejected("%s: refused", p.name)
}
func (p *rejectingProvider) WebhookEvent(http.Header, []byte, time.Time) (WebhookPayload, error) {
	return WebhookPayload{}, errWebhookUnauthorized("no webhook")
}

func TestInitiatePaymentProviderRejectedRecordsFailedAttempt(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	stub := &rejectingProvider{name: "stub"}
	svc := NewService(store, map[string]Provider{stub.Name(): stub}, pub, nil)
	svc.DefaultProvider = "stub"

	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{
		TenantID: nil, CreatedBy: uuid.New(), AmountMinor: 7_500, Currency: CurrencyKES,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)
	require.Equal(t, IntentFailed, initiated.Intent.Status)
	require.NotNil(t, initiated.Transaction)
	assert.Equal(t, TxFailed, initiated.Transaction.Status)
	require.NotEmpty(t, initiated.Transaction.FailureReason)

	// Both events published: the intent exists and its attempt failed.
	types := pub.eventTypes(t)
	require.Contains(t, types, EventIntentCreated)
	require.Contains(t, types, EventTransactionFailedV1)
	failed := eventsOfType[TransactionSettledPayload](t, pub, EventTransactionFailedV1)
	require.Len(t, failed, 1)
	assert.Equal(t, int64(7_500), failed[0].AmountMinor)
	assert.Equal(t, "initiation", failed[0].SettledBy)
}

// --- webhook exactly-once discipline ----------------------------------------

func TestWebhookReplayAppliesExactlyOnce(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	tenant := uuid.New()
	initiated, err := initiateThroughSandbox(t, svc, &tenant, uuid.New(), 25_000)
	require.NoError(t, err)
	tx := initiated.Transaction

	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, tx.ProviderRef, 25_000, now)

	const deliveries = 6
	first := true
	for i := 0; i < deliveries; i++ {
		result, werr := svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
		require.NoError(t, werr, "delivery %d", i)
		if first {
			assert.Equal(t, WebhookStatusApplied, result.Status)
			assert.False(t, result.Duplicate)
			first = false
		} else {
			assert.Equal(t, WebhookStatusDuplicate, result.Status, "replay %d must be absorbed", i)
			assert.True(t, result.Duplicate)
		}
		assert.Equal(t, initiated.Intent.ID, result.IntentID)
	}

	// The ledger holds exactly one settled transaction and one settled
	// intent, no matter how many deliveries arrived.
	stored, gerr := store.GetTransaction(testCtx, tx.ID, UnrestrictedScope())
	require.NoError(t, gerr)
	assert.Equal(t, TxSucceeded, stored.Status)
	assert.NotNil(t, stored.OccurredAt)

	intent, gerr := svc.GetIntent(testCtx, initiated.Intent.ID, UnrestrictedScope())
	require.NoError(t, gerr)
	assert.Equal(t, IntentSucceeded, intent.Status)

	// Exactly one settlement event reached the broker.
	succeeded := eventsOfType[TransactionSettledPayload](t, pub, EventTransactionSucceededV1)
	require.Len(t, succeeded, 1, "the webhook must publish transaction.succeeded.v1 exactly once")
	assert.Equal(t, int64(25_000), succeeded[0].AmountMinor)
	assert.Equal(t, "webhook", succeeded[0].SettledBy)

	// Exactly one idempotency key with the recorded outcome.
	require.Len(t, store.idempotency, 1)
	for _, key := range store.idempotency {
		assert.Equal(t, "applied", key.result)
		assert.Equal(t, "webhook", key.scope)
		assert.NotEmpty(t, key.payloadHash)
	}
}

func TestWebhookReplayWithDifferentPayloadIsTamperEvidence(t *testing.T) {
	svc, _, _, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 10_000)
	require.NoError(t, err)
	tx := initiated.Transaction

	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, tx.ProviderRef, 10_000, now)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)

	// A different payload replayed under the same correlation (same event +
	// transaction) is a rejection, even correctly signed.
	other, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, tx.ProviderRef, 999_999, "", now)
	require.NoError(t, err)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, signedHeader(t, string(sandbox.WebhookSecret), other, now), other, now)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWebhookRejected)
}

func TestWebhookSignatureFailureIsTheTrustBoundary(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 3_000)
	require.NoError(t, err)
	tx := initiated.Transaction

	now := time.Now()
	// baseline: the initiation published intent.created.v1; the boundary
	// failures below must not add any event.
	baseline := pub.count()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, tx.ProviderRef, 3_000, "", now)
	require.NoError(t, err)

	tampered, terr := SandboxWebhookBody(WebhookEventTransactionSucceeded, tx.ProviderRef, 99_999, "", now)
	require.NoError(t, terr)

	cases := []struct {
		name   string
		header http.Header
		body   []byte
	}{
		{"missing signature", http.Header{}, body},
		{"malformed signature", func() http.Header {
			h := http.Header{}
			h.Set(WebhookSignatureHeader, "not-a-signature")
			return h
		}(), body},
		{"wrong secret", signedHeader(t, "attacker-secret", body, now), body},
		{"stale timestamp", signedHeader(t, string(sandbox.WebhookSecret), body, now.Add(-2*signatureFreshness)), body},
		// Signature computed over the ORIGINAL bytes, delivered with edited
		// bytes: the HMAC over "<timestamp>.<raw body>" must not match.
		{"tampered body", signedHeader(t, string(sandbox.WebhookSecret), body, now), tampered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.HandleWebhook(testCtx, ProviderSandbox, tc.header, tc.body, now)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrWebhookUnauthorized, "signature failures wrap ErrWebhookUnauthorized")
		})
	}

	// Nothing was applied and nothing published: the boundary precedes the
	// ledger entirely.
	unchanged, gerr := store.GetTransaction(testCtx, tx.ID, UnrestrictedScope())
	require.NoError(t, gerr)
	assert.Equal(t, TxPending, unchanged.Status)
	assert.Equal(t, baseline, pub.count())
}

func TestWebhookPayloadValidationAgainstLedger(t *testing.T) {
	svc, _, _, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 12_345)
	require.NoError(t, err)
	tx := initiated.Transaction
	now := time.Now()

	t.Run("unknown provider reference rejected", func(t *testing.T) {
		header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, "SBX-nope", 12_345, now)
		_, err := svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWebhookRejected)
	})

	t.Run("amount mismatch rejected", func(t *testing.T) {
		header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, tx.ProviderRef, 99_999, now)
		_, err := svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWebhookRejected)
	})

	t.Run("failed settlement applies exactly once with reason", func(t *testing.T) {
		failedBody, ferr := SandboxWebhookBody(WebhookEventTransactionFailed, tx.ProviderRef, 12_345, "cancelled by user", now)
		require.NoError(t, ferr)
		result, werr := svc.HandleWebhook(testCtx, ProviderSandbox,
			signedHeader(t, string(sandbox.WebhookSecret), failedBody, now), failedBody, now)
		require.NoError(t, werr)
		assert.Equal(t, WebhookStatusApplied, result.Status)
	})
}

func TestWebhookInFlightClaimConflicts(t *testing.T) {
	svc, store, _, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 4_000)
	require.NoError(t, err)
	tx := initiated.Transaction
	now := time.Now()

	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, tx.ProviderRef, 4_000, now)

	// A concurrent delivery claimed the key (same deterministic key, same
	// payload hash) but has not completed it yet.
	_, cerr := store.ClaimIdempotencyKey(testCtx,
		fmt.Sprintf("webhook:%s:%s:%s", ProviderSandbox, WebhookEventTransactionSucceeded, tx.ID),
		"webhook", sha256Hex(body))
	require.NoError(t, cerr)

	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConflict, "an in-flight claim asks the provider to retry")
}

func TestWebhookUnknownProviderRoute(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	_, err := svc.HandleWebhook(testCtx, "doesnotexist", http.Header{}, []byte("{}"), time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// --- tenant isolation --------------------------------------------------------

func TestTenantIsolationOnIntentReads(t *testing.T) {
	svc, store, _, _ := newTestService(t)
	tenantA := uuid.New()
	tenantB := uuid.New()
	garageA := uuid.MustParse("a0000000-0000-0000-0000-00000000000a")
	customerA := uuid.MustParse("c0000000-0000-0000-0000-00000000000a")

	paidA, err := initiateThroughSandbox(t, svc, &tenantA, garageA, 1_000)
	require.NoError(t, err)
	// A personal (customer-linked) intent under no tenant.
	personal, err := svc.InitiatePayment(testCtx, InitiateInput{
		CreatedBy: uuid.New(), CustomerID: &customerA, AmountMinor: 2_000, Currency: CurrencyKES,
	})
	require.NoError(t, err)

	// Same tenant sees both its own tenant intent (through the tenant scope)
	// but not the personal one (different tenant, different principal).
	_, err = svc.GetIntent(testCtx, paidA.Intent.ID, Scope{TenantID: &tenantB})
	require.True(t, errors.Is(err, ErrIntentNotFound), "another tenant must read nothing")

	own, err := svc.GetIntent(testCtx, paidA.Intent.ID, Scope{TenantID: &tenantA})
	require.NoError(t, err)
	assert.Equal(t, paidA.Intent.ID, own.ID)

	// A stranger principal sees neither the garage's intent nor the
	// customer's payment.
	_, err = svc.GetIntent(testCtx, paidA.Intent.ID, Scope{PrincipalID: &customerA})
	require.True(t, errors.Is(err, ErrIntentNotFound))
	mine, err := svc.GetIntent(testCtx, personal.Intent.ID, Scope{PrincipalID: &customerA})
	require.NoError(t, err, "the customer reads their own payment")
	assert.Equal(t, personal.Intent.ID, mine.ID)

	// The zero scope matches NOTHING (fail closed, issue #81 hardening).
	empty, err := store.ListIntents(testCtx, Scope{}, IntentFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, empty)
	_, err = store.GetIntent(testCtx, paidA.Intent.ID, Scope{})
	require.True(t, errors.Is(err, ErrIntentNotFound))

	// Transactions are invisible through an invisible intent.
	_, err = svc.GetTransaction(testCtx, paidA.Transaction.ID, Scope{TenantID: &tenantB})
	require.True(t, errors.Is(err, ErrTransactionNotFound))
	_, err = svc.Transactions(testCtx, paidA.Intent.ID, Scope{TenantID: &tenantB})
	require.True(t, errors.Is(err, ErrIntentNotFound))

	// Operators read across tenants (control-plane roles).
	everything, err := svc.Intents(testCtx, UnrestrictedScope(), IntentFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, everything, 2)
}

func TestTransactionReadsAreScopedThroughIntent(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	tenant := uuid.New()
	creator := uuid.New()
	initiated, err := initiateThroughSandbox(t, svc, &tenant, creator, 5_500)
	require.NoError(t, err)

	txs, err := svc.Transactions(testCtx, initiated.Intent.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Len(t, txs, 1)
	assert.Equal(t, TxPending, txs[0].Status)

	got, err := svc.GetTransaction(testCtx, initiated.Transaction.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	assert.Equal(t, initiated.Transaction.ID, got.ID)

	otherTenant := uuid.New()
	_, err = svc.GetTransaction(testCtx, initiated.Transaction.ID, Scope{TenantID: &otherTenant})
	require.True(t, errors.Is(err, ErrTransactionNotFound))
}

// --- refund flow --------------------------------------------------------------

func TestRefundPaymentFullFlow(t *testing.T) {
	svc, _, pub, sandbox := newTestService(t)
	tenant := uuid.New()
	actor := uuid.New()
	initiated, err := initiateThroughSandbox(t, svc, &tenant, actor, 18_000)
	require.NoError(t, err)

	// Only a SUCCEEDED intent is refundable.
	_, err = svc.RefundPayment(testCtx, initiated.Intent.ID, "too early", actor, Scope{TenantID: &tenant})
	require.True(t, errors.Is(err, ErrNotRefundable))

	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 18_000, now)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)

	// The refund runs through the tenant scope: the read stays inside the
	// tenant (role gating is the HTTP layer's RequireRole).
	refund, err := svc.RefundPayment(testCtx, initiated.Intent.ID, "vehicle returned", actor, Scope{TenantID: &tenant})
	require.NoError(t, err)
	assert.Equal(t, RefundCompleted, refund.Status)
	assert.Equal(t, int64(18_000), refund.AmountMinor, "full refund only")
	assert.Equal(t, CurrencyKES, refund.Currency)
	require.NotNil(t, refund.RequestedBy)
	assert.Equal(t, actor, *refund.RequestedBy)

	// A second refund is refused in every form.
	_, err = svc.RefundPayment(testCtx, initiated.Intent.ID, "again", actor, Scope{TenantID: &tenant})
	require.True(t, errors.Is(err, ErrAlreadyRefunded))

	completed := eventsOfType[RefundCompletedPayload](t, pub, EventRefundCompleted)
	require.Len(t, completed, 1)
	assert.Equal(t, int64(18_000), completed[0].AmountMinor)

	// The refund is visible through the tenant scope and invisible outside.
	got, err := svc.GetRefund(testCtx, refund.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	assert.Equal(t, RefundCompleted, got.Status)
	otherTenant := uuid.New()
	_, err = svc.GetRefund(testCtx, refund.ID, Scope{TenantID: &otherTenant})
	require.True(t, errors.Is(err, ErrRefundNotFound))
}

// sandboxForSettle fetches the sandbox adapter registered on the service.
func sandboxForSettle(svc *Service) *SandboxProvider {
	p, _ := svc.Provider(ProviderSandbox)
	return p.(*SandboxProvider)
}

func TestRefundProviderUnavailableLeavesRequestedRow(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	sandbox := NewSandboxProvider([]byte(testWebhookSecret))
	down := &unavailableProvider{name: "down"}
	svc := NewService(store, map[string]Provider{ProviderSandbox: sandbox, down.Name(): down}, pub, nil)

	// Initiate through the sandbox, settle it, then swap the intent's
	// provider to the down adapter for the refund call.
	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{CreatedBy: uuid.New(), AmountMinor: 9_900, Currency: CurrencyKES})
	require.NoError(t, err)
	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 9_900, now)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)
	store.intents[initiated.Intent.ID].Provider = "down"

	refund, err := svc.RefundPayment(testCtx, initiated.Intent.ID, "provider down", uuid.New(), UnrestrictedScope())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderUnavailable)
	// The honest REQUESTED row stays in the ledger for ops review.
	assert.Equal(t, RefundRequested, refund.Status)
	stored, gerr := store.GetRefund(testCtx, refund.ID, UnrestrictedScope())
	require.NoError(t, gerr)
	assert.Equal(t, RefundRequested, stored.Status)
}

func TestRefundFailureRecordsFailedRefund(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	sandbox := NewSandboxProvider([]byte(testWebhookSecret))
	stub := &rejectingProvider{name: "stub"}
	svc := NewService(store, map[string]Provider{ProviderSandbox: sandbox, stub.Name(): stub}, pub, nil)

	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{CreatedBy: uuid.New(), AmountMinor: 4_400, Currency: CurrencyKES})
	require.NoError(t, err)
	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 4_400, now)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)
	store.intents[initiated.Intent.ID].Provider = "stub"

	_, err = svc.RefundPayment(testCtx, initiated.Intent.ID, "will fail", uuid.New(), UnrestrictedScope())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)
	failed := eventsOfType[RefundFailedPayload](t, pub, EventRefundFailed)
	require.Len(t, failed, 1)
}

func TestRefundReasonTooLongRejected(t *testing.T) {
	svc, _, _, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 2_000)
	require.NoError(t, err)
	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 2_000, now)
	_, err = svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)

	_, err = svc.RefundPayment(testCtx, initiated.Intent.ID, string(make([]byte, MaxRefundReasonLength+1)), uuid.New(), UnrestrictedScope())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// --- pagination discipline -----------------------------------------------------

func TestListIntentsPaginationAndFilter(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	tenant := uuid.New()
	for i := 0; i < 5; i++ {
		_, err := initiateThroughSandbox(t, svc, &tenant, uuid.New(), int64(1_000+i))
		require.NoError(t, err)
	}
	page, err := svc.Intents(testCtx, Scope{TenantID: &tenant}, IntentFilter{}, 3, 0)
	require.NoError(t, err)
	assert.Len(t, page, 3)
	second, err := svc.Intents(testCtx, Scope{TenantID: &tenant}, IntentFilter{}, 3, 3)
	require.NoError(t, err)
	assert.Len(t, second, 2)

	status := IntentProcessing
	filtered, err := svc.Intents(testCtx, Scope{TenantID: &tenant}, IntentFilter{Status: &status}, 0, 0)
	require.NoError(t, err)
	assert.Len(t, filtered, 5, "limit 0 selects the default (all 5 fit)")

	unknown := IntentStatus("DESTROYED")
	_, err = svc.Intents(testCtx, Scope{TenantID: &tenant}, IntentFilter{Status: &unknown}, 10, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
}
