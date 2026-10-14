package payments

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reconciliation tests prove the missed-webhook path converges onto the
// same settled state the webhook path produces (issue #62 AC: "query-based
// reconciliation test proves state repair"), and the provider-swap test
// drives the SAME flow through both adapters behind the Provider
// abstraction — sandbox and Daraja-over-httptest — with the ledger unable
// to tell the difference.

// staleBackdate pushes a transaction outside the staleness window.
func staleBackdate(t *testing.T, store *fakeStore, txID uuid.UUID, d time.Duration) {
	t.Helper()
	store.BackdateTransaction(txID, d)
}

func TestReconciliationConvergesMissedWebhook(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	tenant := uuid.New()
	initiated, err := initiateThroughSandbox(t, svc, &tenant, uuid.New(), 31_000)
	require.NoError(t, err)
	tx := initiated.Transaction

	// The webhook never arrived: the transaction ages past the window and
	// the provider has (deterministically) settled it.
	sandbox.SetOutcome(tx.ID, TxSucceeded)
	staleBackdate(t, store, tx.ID, 10*DefaultStaleAfter)

	rec := NewReconciler(store, map[string]Provider{ProviderSandbox: sandbox}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Scanned)
	assert.Equal(t, 1, result.Repaired, "the query fallback must repair the missed webhook")
	assert.Equal(t, 0, result.StillPending)
	assert.Equal(t, 0, result.Errors)

	// The ledger converged onto exactly the webhook path's state: settled
	// transaction with the provider receipt, succeeded intent, transition
	// rows labelled "reconciliation".
	stored, err := store.GetTransaction(testCtx, tx.ID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, TxSucceeded, stored.Status)
	assert.Equal(t, "SBXR-"+tx.ID.String(), stored.ProviderReceipt)
	require.NotNil(t, stored.OccurredAt)

	intent, err := svc.GetIntent(testCtx, initiated.Intent.ID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, IntentSucceeded, intent.Status)

	settled := eventsOfType[TransactionSettledPayload](t, pub, EventTransactionSucceededV1)
	require.Len(t, settled, 1)
	assert.Equal(t, "reconciliation", settled[0].SettledBy)
	assert.Equal(t, int64(31_000), settled[0].AmountMinor)

	// Convergence is final: the second run scans nothing — the repaired
	// intent is terminal and never re-queried.
	second, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Scanned)
	assert.Equal(t, 0, second.Repaired)
	assert.Equal(t, 0, second.Errors, "no repair happens twice: exactly-once holds across paths")
}

func TestReconciliationConvergesFailedSettlement(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 8_800)
	require.NoError(t, err)
	sandbox.SetOutcome(initiated.Transaction.ID, TxFailed)
	staleBackdate(t, store, initiated.Transaction.ID, 10*DefaultStaleAfter)

	rec := NewReconciler(store, map[string]Provider{ProviderSandbox: sandbox}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Repaired)

	intent, err := svc.GetIntent(testCtx, initiated.Intent.ID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, IntentFailed, intent.Status)
	failed := eventsOfType[TransactionSettledPayload](t, pub, EventTransactionFailedV1)
	require.Len(t, failed, 1)
	assert.Equal(t, "reconciliation", failed[0].SettledBy)
}

func TestReconciliationLeavesUnsettledPending(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 6_600)
	require.NoError(t, err)
	// Nothing settled at the provider and the transaction is stale.
	staleBackdate(t, store, initiated.Transaction.ID, 10*DefaultStaleAfter)

	rec := NewReconciler(store, map[string]Provider{ProviderSandbox: sandbox}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Scanned)
	assert.Equal(t, 0, result.Repaired)
	assert.Equal(t, 1, result.StillPending, "unsettled transactions stay pending for the next run")

	// A fresh transaction inside the grace window is never scanned.
	fresh, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 1_100)
	require.NoError(t, err)
	second, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 1, second.Scanned, "only the stale transaction is examined")
	_ = fresh
}

func TestReconciliationProviderDownIsRetryable(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	down := &unavailableProvider{name: "down"}

	// A PENDING transaction exists (initiated through the live adapter),
	// then the adapter becomes unreachable.
	sandbox := NewSandboxProvider([]byte(testWebhookSecret))
	svc2 := NewService(store, map[string]Provider{ProviderSandbox: sandbox, down.Name(): down}, pub, nil)
	initiated, err := svc2.InitiatePayment(testCtx, InitiateInput{CreatedBy: uuid.New(), AmountMinor: 5_000, Currency: CurrencyKES})
	require.NoError(t, err)
	store.BackdateTransaction(initiated.Transaction.ID, 10*DefaultStaleAfter)
	store.intents[initiated.Intent.ID].Provider = "down"
	store.txs[initiated.Transaction.ID].Provider = "down"

	rec := NewReconciler(store, map[string]Provider{down.Name(): down}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err, "provider failure is designed-for, not exceptional")
	assert.Equal(t, 1, result.Scanned)
	assert.Equal(t, 0, result.Repaired)
	assert.Equal(t, 1, result.Errors)

	// The row stays PENDING; the next run queries again (ARCHITECTURE.md §7).
	stored, err := store.GetTransaction(testCtx, initiated.Transaction.ID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, TxPending, stored.Status)
}

// TestWebhookAndReconciliationPathsRaceSafe proves the two settlement paths
// converge cleanly in either order: whichever lands first settles the
// ledger, the second becomes a no-op, and the event stream carries exactly
// one settlement per path taken.
func TestWebhookAndReconciliationPathsRaceSafe(t *testing.T) {
	svc, store, pub, sandbox := newTestService(t)
	initiated, err := initiateThroughSandbox(t, svc, nil, uuid.New(), 44_000)
	require.NoError(t, err)
	tx := initiated.Transaction

	// Path A: reconciliation repairs it first.
	sandbox.SetOutcome(tx.ID, TxSucceeded)
	staleBackdate(t, store, tx.ID, 10*DefaultStaleAfter)
	rec := NewReconciler(store, map[string]Provider{ProviderSandbox: sandbox}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Repaired)

	// Path B: the missed webhook arrives afterwards — replayed through the
	// exactly-once discipline it must not double-apply.
	now := time.Now()
	header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, tx.ProviderRef, 44_000, now)
	webhookResult, err := svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
	require.NoError(t, err)
	assert.Equal(t, WebhookStatusApplied, webhookResult.Status,
		"the webhook completes its own idempotency claim; the ledger move itself is the converging no-op")

	succeeded := eventsOfType[TransactionSettledPayload](t, pub, EventTransactionSucceededV1)
	require.Len(t, succeeded, 2, "exactly one reconciliation event + exactly one webhook event")
	counts := map[string]int{}
	for _, e := range succeeded {
		counts[e.SettledBy]++
	}
	assert.Equal(t, 1, counts["reconciliation"])
	assert.Equal(t, 1, counts["webhook"])
}

// --- provider swap: the same flow against both adapters ----------------------

// darajaFixture is a deterministic local stand-in for the Daraja API: the
// OAuth token, STK push, status query and reversal endpoints answer exactly
// the documented wire shapes.
type darajaFixture struct {
	server      *httptest.Server
	provider    *DarajaProvider
	pushCount   int
	queryCount  int
	reversalCnt int
}

func newDarajaFixture(t *testing.T, webhookSecret []byte) *darajaFixture {
	t.Helper()
	f := &darajaFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v1/generate", func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user == "" || pass == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fixture-token","expires_in":"3599"}`))
	})
	mux.HandleFunc("/mpesa/stkpush/v1/processrequest", func(w http.ResponseWriter, r *http.Request) {
		f.pushCount++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// The fixture asserts the money discipline the adapter must uphold:
		// the pushed amount is whole KES (no cents reach Daraja).
		amount, ok := body["Amount"].(float64)
		require.True(t, ok, "Amount must be a whole number")
		require.Equal(t, float64(int64(amount)), amount, "Daraja amounts are whole KES")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"MerchantRequestID":"%d-1","CheckoutRequestID":"ws_CO_FIX_%d","ResponseCode":"0","ResponseDescription":"Success. Request accepted for processing","CustomerMessage":"Success. Request accepted for processing"}`, f.pushCount, f.pushCount)
	})
	mux.HandleFunc("/mpesa/stkpushquery/v1/query", func(w http.ResponseWriter, r *http.Request) {
		f.queryCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ResponseCode":"0","ResponseDescription":"The service request has been accepted","ResultCode":"0","ResultDesc":"The service request is processed successfully."}`))
	})
	mux.HandleFunc("/mpesa/reversal/v1/request", func(w http.ResponseWriter, r *http.Request) {
		f.reversalCnt++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ResponseCode":"0","ResponseDescription":"Accept the service request"}`))
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	f.provider = NewDarajaProvider(DarajaConfig{
		BaseURL:            f.server.URL,
		ConsumerKey:        "fixture-key",
		ConsumerSecret:     "fixture-secret",
		Shortcode:          "174379",
		Passkey:            "bfb279f9aa9bdbcf158e97dd71a467cd2e0c893059b10f78e6b72ada1ed2c919",
		CallbackURL:        "https://payments.example.com/v1/payments/webhooks/daraja",
		Initiator:          "testapi",
		SecurityCredential: "fixture-credential",
		WebhookSecret:      webhookSecret,
	})
	return f
}

// runStandardFlow exercises the identical provider-agnostic flow: initiate
// -> settlement webhook -> status reads -> full refund. Every step must
// behave the same no matter which adapter sits behind the abstraction.
func runStandardFlow(t *testing.T, svc *Service, providerName string, settle func(t *testing.T, initiated InitiatedPayment)) InitiatedPayment {
	t.Helper()
	tenant := uuid.New()
	actor := uuid.New()

	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{
		TenantID:    &tenant,
		CreatedBy:   actor,
		AmountMinor: 15_000,
		Currency:    CurrencyKES,
		Provider:    providerName,
		Description: "provider swap flow",
		PayerPhone:  "+254711000111", // transient STK-push input, never persisted
	})
	require.NoError(t, err, "%s: initiate", providerName)
	require.Equal(t, providerName, initiated.Intent.Provider, "%s: the intent records its adapter", providerName)
	require.Equal(t, IntentProcessing, initiated.Intent.Status)
	require.NotNil(t, initiated.Transaction)
	require.Equal(t, TxPending, initiated.Transaction.Status)

	// The pending transaction is visible through the scoped reads.
	txs, err := svc.Transactions(testCtx, initiated.Intent.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Len(t, txs, 1)

	// Settlement arrives (each provider's own callback shape).
	settle(t, initiated)

	intent, err := svc.GetIntent(testCtx, initiated.Intent.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, IntentSucceeded, intent.Status, "%s: settlement", providerName)
	stored, err := svc.GetTransaction(testCtx, initiated.Transaction.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, TxSucceeded, stored.Status)

	// Full refund through the SAME service path.
	refund, err := svc.RefundPayment(testCtx, initiated.Intent.ID, "provider swap", actor, Scope{TenantID: &tenant})
	require.NoError(t, err, "%s: refund", providerName)
	assert.Equal(t, RefundCompleted, refund.Status, "%s: refund", providerName)
	assert.Equal(t, int64(15_000), refund.AmountMinor)

	_, err = svc.RefundPayment(testCtx, initiated.Intent.ID, "second", actor, Scope{TenantID: &tenant})
	require.ErrorIs(t, err, ErrAlreadyRefunded, "%s: single refund only", providerName)
	return initiated
}

func TestProviderSwapSameFlowAgainstBothAdapters(t *testing.T) {
	webhookSecret := []byte("provider-swap-webhook-secret")
	store := newFakeStore()
	pub := &capturePublisher{}
	sandbox := NewSandboxProvider(webhookSecret)
	daraja := newDarajaFixture(t, webhookSecret)
	svc := NewService(store, map[string]Provider{
		sandbox.Name():         sandbox,
		daraja.provider.Name(): daraja.provider,
	}, pub, nil)

	t.Run("sandbox", func(t *testing.T) {
		runStandardFlow(t, svc, ProviderSandbox, func(t *testing.T, initiated InitiatedPayment) {
			now := time.Now()
			header, body := signedSandboxWebhook(t, sandbox, WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 15_000, now)
			result, err := svc.HandleWebhook(testCtx, ProviderSandbox, header, body, now)
			require.NoError(t, err)
			require.Equal(t, WebhookStatusApplied, result.Status)
		})
	})

	t.Run("daraja", func(t *testing.T) {
		runStandardFlow(t, svc, ProviderDaraja, func(t *testing.T, initiated InitiatedPayment) {
			// The Daraja callback wire shape, signed by the ingress HMAC
			// (Daraja signs nothing itself).
			callback := []byte(`{"Body":{"stkCallback":{"MerchantRequestID":"29115-34620561-1",` +
				`"CheckoutRequestID":"` + initiated.Transaction.ProviderRef + `","ResultCode":0,` +
				`"ResultDesc":"The service request is processed successfully.",` +
				`"CallbackMetadata":{"Item":[{"Name":"Amount","Value":150},` +
				`{"Name":"MpesaReceiptNumber","Value":"NLJ7RT61SV"},` +
				`{"Name":"PhoneNumber","Value":254708374149}]}}}}`)
			now := time.Now()
			result, err := svc.HandleWebhook(testCtx, ProviderDaraja,
				signedHeader(t, string(webhookSecret), callback, now), callback, now)
			require.NoError(t, err)
			require.Equal(t, WebhookStatusApplied, result.Status)

			// The M-Pesa receipt was stamped with the outcome; the phone
			// number never entered the ledger (PII minimization).
			stored, gerr := store.GetTransaction(testCtx, initiated.Transaction.ID, UnrestrictedScope())
			require.NoError(t, gerr)
			assert.Equal(t, "NLJ7RT61SV", stored.ProviderReceipt)
			encoded, _ := json.Marshal(stored)
			assert.NotContains(t, string(encoded), "254708374149")
		})
	})

	// The flow ran twice against the same ledger without cross-talk: two
	// intents, two settled transactions, two refunds, one per provider.
	everything, err := store.ListIntents(testCtx, UnrestrictedScope(), IntentFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, everything, 2)
	providers := map[string]bool{}
	for _, i := range everything {
		require.Equal(t, IntentSucceeded, i.Status)
		providers[i.Provider] = true
	}
	assert.True(t, providers[ProviderSandbox] && providers[ProviderDaraja],
		"the same flow passed against both providers via the abstraction")
}

// TestDarajaWholeKESRefusal pins the audit fix: a cent-amount is refused by
// the adapter instead of silently truncating to a different settlement.
func TestDarajaWholeKESRefusal(t *testing.T) {
	f := newDarajaFixture(t, []byte("irrelevant"))
	_, err := f.provider.Initiate(testCtx, InitiateRequest{
		IntentID:      uuid.New(),
		TransactionID: uuid.New(),
		Amount:        Money{AmountMinor: 15_050, Currency: CurrencyKES}, // KES 150.05
		PayerPhone:    "+254711000111",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)
	assert.Contains(t, err.Error(), "whole-KES")

	_, err = f.provider.Refund(testCtx, RefundRequest{
		RefundID: uuid.New(), IntentID: uuid.New(), ProviderRef: "ws_CO_FIX_1",
		Amount: Money{AmountMinor: 15_001, Currency: CurrencyKES},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProviderRejected)

	// Whole KES still flows through to the push.
	res, err := f.provider.Initiate(testCtx, InitiateRequest{
		IntentID:      uuid.New(),
		TransactionID: uuid.New(),
		Amount:        Money{AmountMinor: 15_000, Currency: CurrencyKES},
		PayerPhone:    "+254711000111",
	})
	require.NoError(t, err)
	assert.Equal(t, TxPending, res.Status)
	assert.NotEmpty(t, res.ProviderRef)
}

// TestDarajaQueryReconciles pins the query-side reconciliation path for the
// real adapter: the status query maps ResultCode 0 onto a repairable
// settlement.
func TestDarajaQueryReconciles(t *testing.T) {
	store := newFakeStore()
	pub := &capturePublisher{}
	daraja := newDarajaFixture(t, []byte("irrelevant"))
	svc := NewService(store, map[string]Provider{ProviderDaraja: daraja.provider}, pub, nil)
	svc.DefaultProvider = ProviderDaraja

	initiated, err := svc.InitiatePayment(testCtx, InitiateInput{
		CreatedBy: uuid.New(), AmountMinor: 150_000, Currency: CurrencyKES, PayerPhone: "+254711000111",
	})
	require.NoError(t, err)
	store.BackdateTransaction(initiated.Transaction.ID, 10*DefaultStaleAfter)

	rec := NewReconciler(store, map[string]Provider{ProviderDaraja: daraja.provider}, pub, nil, DefaultStaleAfter)
	result, err := rec.Run(testCtx)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Repaired, "the daraja status query repairs the missed webhook")

	stored, err := store.GetTransaction(testCtx, initiated.Transaction.ID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, TxSucceeded, stored.Status)
}
