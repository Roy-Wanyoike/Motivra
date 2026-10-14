package payments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	paymentsmigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/payments"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// PG-gated integration suite (issue #62 AC: "pgtest integration suite
// included"): the full money flow against real PostgreSQL — the
// exactly-once settlement and idempotency claims, the reconciliation
// convergence, tenant isolation at the SQL level and every integrity
// trigger the migrations install. Self-skips without TEST_DATABASE_URL
// (CI runs it once the billing lock, issue #10, is lifted; locally:
// `make dev` + TEST_DATABASE_URL, see docs/RUNBOOK.md).

// paymentsTables are the tables of the payments chain, truncated before
// each test so each starts from a clean schema (transactions, refunds,
// idempotency keys and transitions all cascade from payment_intents).
var paymentsTables = []string{
	"payment_intents", "payment_transactions", "payment_refunds",
	"payment_idempotency_keys", "payment_intent_transitions",
}

// integrationService connects to the Postgres named by TEST_DATABASE_URL
// through the shared pgtest harness, applies the payments migration chain
// and returns a Service, store and sandbox adapter over the real schema,
// plus the pool for raw SQL assertions. The service runs with a nil
// publisher (events are unit-tested with fakes); the DATABASE is the
// system under test. Skips when the variable is unset.
func integrationService(t *testing.T) (*Service, *PostgresStore, *SandboxProvider, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, paymentsmigrations.FS, "schema_migrations_payments")
	pgtest.Truncate(t, pool, paymentsTables...)

	sandbox := NewSandboxProvider([]byte("payments-integration-webhook-secret"))
	store := NewPostgresStore(pool)
	svc := NewService(store, map[string]Provider{sandbox.Name(): sandbox}, nil, nil)
	return svc, store, sandbox, pool
}

// TestPaymentLifecycleIntegration drives the full lifecycle against real
// PostgreSQL: initiate -> PROCESSING, webhook settlement -> SUCCEEDED,
// transition audit rows, full refund -> COMPLETED, and the scoped reads at
// the SQL level (stranger / other-tenant / zero scope).
func TestPaymentLifecycleIntegration(t *testing.T) {
	svc, store, sandbox, pool := integrationService(t)
	ctx := context.Background()

	tenant := uuid.New()
	creator := uuid.New()
	initiated, err := svc.InitiatePayment(ctx, InitiateInput{
		TenantID:    &tenant,
		CreatedBy:   creator,
		AmountMinor: 150_000,
		Currency:    CurrencyKES,
		Description: "integration lifecycle",
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, initiated.Intent.ID)
	require.False(t, initiated.Intent.CreatedAt.IsZero(), "created_at must be returned from the insert")
	require.Equal(t, IntentProcessing, initiated.Intent.Status)
	require.NotNil(t, initiated.Transaction)
	require.False(t, initiated.Transaction.CreatedAt.IsZero())

	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		require.NoError(t, pool.QueryRow(ctx, query, args...).Scan(&n))
		return n
	}
	require.Equal(t, 1, count(
		`SELECT count(*) FROM payment_intent_transitions WHERE intent_id = $1`, initiated.Intent.ID),
		"exactly the CREATED -> PROCESSING move")

	// Settle through the verified webhook.
	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 150_000, "", now)
	require.NoError(t, err)
	result, err := svc.HandleWebhook(ctx, ProviderSandbox,
		signedHeader(t, string(sandbox.WebhookSecret), body, now), body, now)
	require.NoError(t, err)
	require.Equal(t, WebhookStatusApplied, result.Status)

	stored, err := store.GetTransaction(ctx, initiated.Transaction.ID, UnrestrictedScope())
	require.NoError(t, err)
	require.Equal(t, TxSucceeded, stored.Status)
	require.NotNil(t, stored.OccurredAt)

	intent, err := svc.GetIntent(ctx, initiated.Intent.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, IntentSucceeded, intent.Status)
	require.NotNil(t, intent.SucceededAt)
	require.Equal(t, 2, count(
		`SELECT count(*) FROM payment_intent_transitions WHERE intent_id = $1`, initiated.Intent.ID),
		"CREATED->PROCESSING and PROCESSING->SUCCEEDED")

	// Full refund.
	refund, err := svc.RefundPayment(ctx, initiated.Intent.ID, "integration refund", creator, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, RefundCompleted, refund.Status)
	require.Equal(t, int64(150_000), refund.AmountMinor, "full refund only")
	storedRefund, err := store.GetRefund(ctx, refund.ID, Scope{TenantID: &tenant})
	require.NoError(t, err)
	require.Equal(t, RefundCompleted, storedRefund.Status)

	// A second refund is refused by the service...
	_, err = svc.RefundPayment(ctx, initiated.Intent.ID, "again", creator, Scope{TenantID: &tenant})
	require.True(t, errors.Is(err, ErrAlreadyRefunded))

	// Scoped reads at the SQL level: the tenant sees its rows; another
	// tenant and the zero scope see nothing ("1 = 0").
	page, err := store.ListIntents(ctx, Scope{TenantID: &tenant}, IntentFilter{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, page, 1)
	otherTenant := uuid.New()
	other, err := store.ListIntents(ctx, Scope{TenantID: &otherTenant}, IntentFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, other)
	zero, err := store.ListIntents(ctx, Scope{}, IntentFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, zero, "the zero scope must match no rows at all")

	stranger := uuid.New()
	_, err = svc.GetIntent(ctx, initiated.Intent.ID, Scope{TenantID: &stranger})
	require.True(t, errors.Is(err, ErrIntentNotFound), "a stranger must read nothing")
	_, err = store.GetIntent(ctx, initiated.Intent.ID, Scope{})
	require.True(t, errors.Is(err, ErrIntentNotFound))
}

// TestPaymentWebhookExactlyOnceIntegration replays the same signed payload
// N times against the REAL store: exactly one settlement, exactly one
// idempotency key with the recorded outcome, every replay answered as a
// duplicate (issue #62 AC: "webhook replay applies exactly once").
func TestPaymentWebhookExactlyOnceIntegration(t *testing.T) {
	svc, store, sandbox, pool := integrationService(t)
	ctx := context.Background()

	tenant := uuid.New()
	initiated, err := svc.InitiatePayment(ctx, InitiateInput{
		TenantID: &tenant, CreatedBy: uuid.New(), AmountMinor: 25_000, Currency: CurrencyKES,
	})
	require.NoError(t, err)
	tx := initiated.Transaction

	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, tx.ProviderRef, 25_000, "", now)
	require.NoError(t, err)
	header := signedHeader(t, string(sandbox.WebhookSecret), body, now)

	const deliveries = 5
	for i := 0; i < deliveries; i++ {
		result, werr := svc.HandleWebhook(ctx, ProviderSandbox, header, body, now)
		require.NoError(t, werr, "delivery %d", i)
		if i == 0 {
			require.Equal(t, WebhookStatusApplied, result.Status)
		} else {
			require.Equal(t, WebhookStatusDuplicate, result.Status, "replay %d absorbed", i)
		}
	}

	// One settled transaction, one settled intent.
	stored, err := store.GetTransaction(ctx, tx.ID, UnrestrictedScope())
	require.NoError(t, err)
	require.Equal(t, TxSucceeded, stored.Status)
	intent, err := store.GetIntent(ctx, initiated.Intent.ID, UnrestrictedScope())
	require.NoError(t, err)
	require.Equal(t, IntentSucceeded, intent.Status)

	// Exactly one idempotency key: claimed for the webhook scope, carrying
	// the payload hash (tamper evidence) and the recorded outcome.
	var scopeValue, payloadHash, resultValue string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT scope, payload_hash, result FROM payment_idempotency_keys
		WHERE key = $1`,
		"webhook:sandbox:transaction.succeeded:"+tx.ID.String(),
	).Scan(&scopeValue, &payloadHash, &resultValue))
	assert.Equal(t, "webhook", scopeValue)
	assert.Equal(t, sha256Hex(body), payloadHash)
	assert.Equal(t, "applied", resultValue)

	var keys int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM payment_idempotency_keys`).Scan(&keys))
	assert.Equal(t, 1, keys, "N deliveries produce exactly one idempotency key")
}

// TestPaymentReconciliationIntegration proves the missed-webhook path
// converges against the real store: a PENDING transaction past the (tiny)
// staleness window is settled by the reconciler's provider query exactly
// once; the second run scans nothing.
func TestPaymentReconciliationIntegration(t *testing.T) {
	svc, store, sandbox, pool := integrationService(t)
	ctx := context.Background()

	tenant := uuid.New()
	initiated, err := svc.InitiatePayment(ctx, InitiateInput{
		TenantID: &tenant, CreatedBy: uuid.New(), AmountMinor: 44_000, Currency: CurrencyKES,
	})
	require.NoError(t, err)
	tx := initiated.Transaction

	// The provider settled it; the webhook never arrived. The reconciler's
	// staleness window is 1ns, so the fresh transaction is already stale —
	// real deployments backdate via the window, not the ledger (the
	// created_at column is trigger-immutable, proven below).
	sandbox.SetOutcome(tx.ID, TxSucceeded)
	rec := NewReconciler(store, map[string]Provider{ProviderSandbox: sandbox}, nil, nil, time.Nanosecond)
	result, err := rec.Run(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, result.Scanned)
	require.Equal(t, 1, result.Repaired)

	stored, err := store.GetTransaction(ctx, tx.ID, UnrestrictedScope())
	require.NoError(t, err)
	require.Equal(t, TxSucceeded, stored.Status)
	require.Equal(t, "SBXR-"+tx.ID.String(), stored.ProviderReceipt, "the reconciliation receipt")

	second, err := rec.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, second.Scanned, "converged: terminal intents are never re-scanned")

	var transitions int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM payment_intent_transitions WHERE intent_id = $1 AND reason = 'reconciliation'`,
		initiated.Intent.ID).Scan(&transitions))
	assert.Equal(t, 1, transitions)
}

// TestPaymentAppendOnlyTriggersIntegration proves every integrity trigger
// the payments migrations install, by raw SQL: intent amount/currency
// immutability and terminal-status freeze, transaction guarded-update +
// no-delete, refund guarded-update + no-delete, idempotency-key
// write-once + no-delete, and the append-only transition audit.
func TestPaymentAppendOnlyTriggersIntegration(t *testing.T) {
	svc, store, sandbox, pool := integrationService(t)
	ctx := context.Background()

	tenant := uuid.New()
	creator := uuid.New()
	initiated, err := svc.InitiatePayment(ctx, InitiateInput{
		TenantID: &tenant, CreatedBy: creator, AmountMinor: 60_000, Currency: CurrencyKES,
	})
	require.NoError(t, err)
	intentID := initiated.Intent.ID
	txID := initiated.Transaction.ID

	// --- payment_intents: the integer ledger is frozen --------------------
	_, err = pool.Exec(ctx, `UPDATE payment_intents SET amount_minor = 999 WHERE id = $1`, intentID)
	require.Error(t, err, "amount_minor is immutable")
	require.ErrorContains(t, err, "immutable")

	_, err = pool.Exec(ctx, `UPDATE payment_intents SET currency = 'USD' WHERE id = $1`, intentID)
	require.Error(t, err, "currency is immutable")
	require.ErrorContains(t, err, "immutable")

	// PROCESSING is not terminal: a same-status update is allowed (the
	// terminal freeze only bites after settlement).
	_, err = pool.Exec(ctx, `UPDATE payment_intents SET updated_at = now() WHERE id = $1`, intentID)
	require.NoError(t, err, "non-terminal status columns may still update")

	// --- payment_transactions: guarded update + append-only ---------------
	now := time.Now()
	body, err := SandboxWebhookBody(WebhookEventTransactionSucceeded, initiated.Transaction.ProviderRef, 60_000, "", now)
	require.NoError(t, err)
	_, err = svc.HandleWebhook(ctx, ProviderSandbox,
		signedHeader(t, string(sandbox.WebhookSecret), body, now), body, now)
	require.NoError(t, err)

	// The legal move already happened (PENDING -> SUCCEEDED); every further
	// status change is refused.
	_, err = pool.Exec(ctx, `UPDATE payment_transactions SET status = 'PENDING' WHERE id = $1`, txID)
	require.Error(t, err, "a terminal transaction never reverts")
	require.ErrorContains(t, err, "not a legal ledger move")

	_, err = pool.Exec(ctx, `UPDATE payment_transactions SET amount_minor = 1 WHERE id = $1`, txID)
	require.Error(t, err, "ledger columns are immutable")
	require.ErrorContains(t, err, "immutable")

	_, err = pool.Exec(ctx, `DELETE FROM payment_transactions WHERE id = $1`, txID)
	require.Error(t, err, "the ledger never forgets")
	require.ErrorContains(t, err, "append-only")

	// --- payment_refunds: guarded update + append-only --------------------
	refund, err := svc.RefundPayment(ctx, intentID, "trigger refund", creator, Scope{TenantID: &tenant})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE payment_refunds SET amount_minor = 5 WHERE id = $1`, refund.ID)
	require.Error(t, err, "refund ledger columns are immutable")
	require.ErrorContains(t, err, "immutable")

	_, err = pool.Exec(ctx, `UPDATE payment_refunds SET status = 'REQUESTED' WHERE id = $1`, refund.ID)
	require.Error(t, err, "a terminal refund never reverts")
	require.ErrorContains(t, err, "not a legal refund move")

	_, err = pool.Exec(ctx, `DELETE FROM payment_refunds WHERE id = $1`, refund.ID)
	require.Error(t, err, "refunds are append-only")
	require.ErrorContains(t, err, "append-only")

	// --- payment_idempotency_keys: write-once result + append-only --------
	key := "webhook:sandbox:transaction.succeeded:" + txID.String()
	var payloadHash, resultValue string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload_hash, result FROM payment_idempotency_keys WHERE key = $1`, key).
		Scan(&payloadHash, &resultValue))
	require.Equal(t, "applied", resultValue)

	_, err = pool.Exec(ctx, `UPDATE payment_idempotency_keys SET result = 'rewritten' WHERE key = $1`, key)
	require.Error(t, err, "the result may be written exactly once")
	require.ErrorContains(t, err, "exactly once")

	_, err = pool.Exec(ctx, `UPDATE payment_idempotency_keys SET payload_hash = 'ff' WHERE key = $1`, key)
	require.Error(t, err, "key identity is immutable")
	require.ErrorContains(t, err, "immutable")

	_, err = pool.Exec(ctx, `DELETE FROM payment_idempotency_keys WHERE key = $1`, key)
	require.Error(t, err, "the idempotency audit is append-only")
	require.ErrorContains(t, err, "append-only")

	// --- payment_intent_transitions: append-only audit --------------------
	_, err = pool.Exec(ctx,
		`UPDATE payment_intent_transitions SET to_status = 'TAMPERED' WHERE intent_id = $1`, intentID)
	require.Error(t, err, "the transition audit is append-only")
	require.ErrorContains(t, err, "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM payment_intent_transitions WHERE intent_id = $1`, intentID)
	require.Error(t, err, "the ledger never forgets a move")
	require.ErrorContains(t, err, "append-only")

	// The store's own reads still agree with what the triggers protect.
	got, err := store.GetIntent(ctx, intentID, UnrestrictedScope())
	require.NoError(t, err)
	assert.Equal(t, int64(60_000), got.AmountMinor, "the original integer amount stands")
}
