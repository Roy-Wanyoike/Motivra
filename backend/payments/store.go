package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel read errors the service layer maps onto platform 404s.
var (
	// ErrIntentNotFound is returned by intent reads when the intent does
	// not exist or is outside the caller's scope.
	ErrIntentNotFound = errors.New("payments: payment intent not found")
	// ErrTransactionNotFound is returned by transaction reads.
	ErrTransactionNotFound = errors.New("payments: transaction not found")
	// ErrRefundNotFound is returned by refund reads.
	ErrRefundNotFound = errors.New("payments: refund not found")
)

// Pagination defaults applied by ListIntents.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// IntentFilter narrows ListIntents. All fields are optional; nil matches
// everything. The scope always applies on top of the filter.
type IntentFilter struct {
	Status     *IntentStatus
	CustomerID *uuid.UUID
	JobID      *uuid.UUID
	VehicleID  *uuid.UUID
}

// ClaimResult is the outcome of an idempotency-key claim. Claimed is true
// when the caller won the insert and must process the work; otherwise
// ExistingResult is the stored outcome of a previous run ("" plus
// ExistingPayloadHash set means the key is claimed but still in flight).
type ClaimResult struct {
	Claimed             bool
	ExistingResult      string
	ExistingPayloadHash string
}

// ApplyOutcomeParams drives ApplyTransactionOutcome: the exactly-once
// settlement of one transaction and its intent, in a single transaction.
type ApplyOutcomeParams struct {
	TransactionID uuid.UUID
	// NewTxStatus is the terminal transaction status (SUCCEEDED/FAILED).
	NewTxStatus TransactionStatus
	// NewIntentStatus is the matching terminal intent status.
	NewIntentStatus IntentStatus
	// ProviderReceipt is the provider's settlement identifier when known.
	ProviderReceipt string
	FailureReason   string
	// OccurredAt is the provider-attested settlement time.
	OccurredAt time.Time
	// Reason labels the transition audit row ("webhook" or
	// "reconciliation").
	Reason string
}

// StaleIntent pairs a PROCESSING intent with its PENDING transaction for
// the reconciliation fallback.
type StaleIntent struct {
	Intent      PaymentIntent
	Transaction Transaction
}

// Store is the persistence contract for the payments domain.
// Implementations must keep payment_transactions, payment_refunds,
// payment_idempotency_keys and payment_intent_transitions under the
// integrity rules the migrations enforce (guarded updates, append-only
// audit) and must perform multi-write operations in single transactions.
//
// Every read method takes a Scope derived from the authenticated principal
// (ADR-0004: the repository layer enforces scope; there is no unscoped
// variant). Rows the scope does not satisfy — including under the zero
// scope, which matches nothing — are indistinguishable from missing rows.
// Internal flows (webhook correlation, reconciliation) use
// UnrestrictedScope() constructed in code, never from client input.
type Store interface {
	// CreateIntent persists a new intent in status CREATED.
	CreateIntent(ctx context.Context, i *PaymentIntent) error

	// GetIntent returns the intent for a scope that can see it, or an
	// error wrapping ErrIntentNotFound.
	GetIntent(ctx context.Context, id uuid.UUID, scope Scope) (PaymentIntent, error)

	// ListIntents returns one offset page of the scoped registry, newest
	// first. limit <= 0 selects the default and limit is capped;
	// offset < 0 is treated as 0.
	ListIntents(ctx context.Context, scope Scope, filter IntentFilter, limit, offset int) ([]PaymentIntent, error)

	// UpdateIntentStatus moves the intent from expectedFrom to newStatus
	// and appends one payment_intent_transitions row, in a single
	// transaction with a FOR UPDATE lock (closing the concurrent-mover
	// race). It fails with an error wrapping ErrConflict when the stored
	// status is not expectedFrom and with an error wrapping
	// ErrIntentNotFound when the intent does not exist. The
	// status-appropriate timestamp is stamped inside the transaction.
	UpdateIntentStatus(ctx context.Context, intentID uuid.UUID, expectedFrom, newStatus IntentStatus, actorID *uuid.UUID, reason string) error

	// InsertTransaction appends the settlement attempt row. It assigns
	// t.ID when unset and populates t.CreatedAt/UpdatedAt.
	InsertTransaction(ctx context.Context, t *Transaction) error

	// GetTransaction returns the transaction for a scope that can see it
	// (through its intent), or an error wrapping ErrTransactionNotFound.
	GetTransaction(ctx context.Context, id uuid.UUID, scope Scope) (Transaction, error)

	// GetTransactionByProviderRef resolves the webhook correlation handle.
	// Internal (unrestricted) by design: the caller is the verified
	// webhook path, not a principal.
	GetTransactionByProviderRef(ctx context.Context, provider, providerRef string) (Transaction, error)

	// ListTransactions returns the intent's transactions, oldest first,
	// for an intent visible to scope.
	ListTransactions(ctx context.Context, intentID uuid.UUID, scope Scope) ([]Transaction, error)

	// ApplyTransactionOutcome settles a transaction and its intent in one
	// transaction: it locks both rows, refuses unexpected states with an
	// error wrapping ErrConflict, stamps the outcome columns, and appends
	// the intent transition row. It returns applied=false — without
	// error — when the transaction already sits in the requested terminal
	// state (idempotent replay through this path).
	ApplyTransactionOutcome(ctx context.Context, p ApplyOutcomeParams) (bool, error)

	// InsertRefund appends the refund row (status REQUESTED).
	InsertRefund(ctx context.Context, r *Refund) error

	// GetRefund returns the refund for a scope that can see it (through
	// its intent), or an error wrapping ErrRefundNotFound.
	GetRefund(ctx context.Context, id uuid.UUID, scope Scope) (Refund, error)

	// UpdateRefundStatus moves the refund from expectedFrom to newStatus
	// with a FOR UPDATE lock, stamping providerRef on COMPLETED and
	// failureReason on FAILED. It fails with an error wrapping ErrConflict
	// when the stored status is not expectedFrom.
	UpdateRefundStatus(ctx context.Context, refundID uuid.UUID, expectedFrom, newStatus RefundStatus, providerRef, failureReason string) error

	// LatestRefundForIntent returns the intent's most recent refund, or an
	// error wrapping ErrRefundNotFound.
	LatestRefundForIntent(ctx context.Context, intentID uuid.UUID, scope Scope) (Refund, error)

	// ClaimIdempotencyKey atomically claims key: the first caller wins
	// (Claimed=true); every later caller reads the stored result. The
	// payload hash is recorded with the claim for tamper evidence.
	ClaimIdempotencyKey(ctx context.Context, key, scope, payloadHash string) (ClaimResult, error)

	// CompleteIdempotencyKey stores the outcome of the work claimed under
	// key (the one permitted result write).
	CompleteIdempotencyKey(ctx context.Context, key, result string) error

	// ListStaleIntents returns intents stuck in PROCESSING whose newest
	// transaction has been PENDING since before olderThan — the
	// reconciliation workload. Internal (unrestricted) by design.
	ListStaleIntents(ctx context.Context, olderThan time.Time, limit int) ([]StaleIntent, error)
}

// PostgresStore implements Store on PostgreSQL through pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that PostgresStore satisfies Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore returns a Store backed by pool. The pool must already
// have the payments migrations applied (platform.MigrateUp with
// paymentsmigrations.FS).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const intentColumns = `id, tenant_id, customer_id, created_by, amount_minor, currency, status, provider,
	provider_ref, job_id, vehicle_id, description, failure_reason, succeeded_at, failed_at, canceled_at,
	created_at, updated_at`

const transactionColumns = `id, intent_id, provider, provider_ref, provider_receipt, status, amount_minor,
	currency, failure_reason, occurred_at, created_at, updated_at`

const refundColumns = `id, intent_id, amount_minor, currency, status, provider, provider_ref, reason,
	failure_reason, requested_by, created_at, updated_at`

// intentScopePredicate renders the boundary condition for the
// payment_intents table with unqualified column names.
func intentScopePredicate(scope Scope, nextParam int) (string, []any) {
	return scopePredicate(scope, "tenant_id", "created_by", "customer_id", nextParam)
}

// scanIntent scans one intent row.
func scanIntent(row pgx.Row) (PaymentIntent, error) {
	var i PaymentIntent
	err := row.Scan(&i.ID, &i.TenantID, &i.CustomerID, &i.CreatedBy, &i.AmountMinor, &i.Currency,
		&i.Status, &i.Provider, &i.ProviderRef, &i.JobID, &i.VehicleID, &i.Description,
		&i.FailureReason, &i.SucceededAt, &i.FailedAt, &i.CanceledAt, &i.CreatedAt, &i.UpdatedAt)
	return i, err
}

// scanTransaction scans one transaction row.
func scanTransaction(row pgx.Row) (Transaction, error) {
	var t Transaction
	err := row.Scan(&t.ID, &t.IntentID, &t.Provider, &t.ProviderRef, &t.ProviderReceipt, &t.Status,
		&t.AmountMinor, &t.Currency, &t.FailureReason, &t.OccurredAt, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// scanRefund scans one refund row.
func scanRefund(row pgx.Row) (Refund, error) {
	var r Refund
	err := row.Scan(&r.ID, &r.IntentID, &r.AmountMinor, &r.Currency, &r.Status, &r.Provider,
		&r.ProviderRef, &r.Reason, &r.FailureReason, &r.RequestedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// CreateIntent implements Store.
func (s *PostgresStore) CreateIntent(ctx context.Context, i *PaymentIntent) error {
	if i == nil {
		return fmt.Errorf("payments: create intent: intent is required")
	}
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payment_intents
			(id, tenant_id, customer_id, created_by, amount_minor, currency, status, provider)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`,
		i.ID, i.TenantID, i.CustomerID, i.CreatedBy, i.AmountMinor, i.Currency, i.Status, i.Provider,
	).Scan(&i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return fmt.Errorf("payments: insert intent: %w", err)
	}
	return nil
}

// GetIntent implements Store. The scope predicate runs inside the query
// (ADR-0004): an out-of-scope intent is indistinguishable from a missing
// one.
func (s *PostgresStore) GetIntent(ctx context.Context, id uuid.UUID, scope Scope) (PaymentIntent, error) {
	pred, predArgs := intentScopePredicate(scope, 2)
	where := "id = $1"
	args := []any{id}
	if pred != "" {
		where += " AND " + pred
		args = append(args, predArgs...)
	}
	i, err := scanIntent(s.pool.QueryRow(ctx, `
		SELECT `+intentColumns+`
		FROM payment_intents
		WHERE `+where, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PaymentIntent{}, fmt.Errorf("payments: get intent %s: %w", id, ErrIntentNotFound)
		}
		return PaymentIntent{}, fmt.Errorf("payments: get intent: %w", err)
	}
	return i, nil
}

// ListIntents implements Store: one offset page of the scoped registry,
// newest first.
func (s *PostgresStore) ListIntents(ctx context.Context, scope Scope, filter IntentFilter, limit, offset int) ([]PaymentIntent, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}

	pred, predArgs := intentScopePredicate(scope, 1)
	where := "TRUE"
	args := []any{}
	if pred != "" {
		where = pred
		args = append(args, predArgs...)
	}
	if filter.Status != nil {
		args = append(args, *filter.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if filter.CustomerID != nil {
		args = append(args, *filter.CustomerID)
		where += fmt.Sprintf(" AND customer_id = $%d", len(args))
	}
	if filter.JobID != nil {
		args = append(args, *filter.JobID)
		where += fmt.Sprintf(" AND job_id = $%d", len(args))
	}
	if filter.VehicleID != nil {
		args = append(args, *filter.VehicleID)
		where += fmt.Sprintf(" AND vehicle_id = $%d", len(args))
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`
		SELECT `+intentColumns+`
		FROM payment_intents
		WHERE %s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("payments: list intents: %w", err)
	}
	defer rows.Close()

	out := make([]PaymentIntent, 0, limit)
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("payments: scan intent: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payments: list intents: %w", err)
	}
	return out, nil
}

// intentStatusTimestamp names the context timestamp each status move
// stamps (empty for CREATED, which carries created_at).
func intentStatusTimestamp(to IntentStatus) string {
	switch to {
	case IntentSucceeded:
		return "succeeded_at"
	case IntentFailed:
		return "failed_at"
	case IntentCanceled:
		return "canceled_at"
	default:
		return ""
	}
}

// UpdateIntentStatus implements Store. The FOR UPDATE lock serializes
// concurrent movers; the expectedFrom re-check closes the race the
// service-level check cannot.
func (s *PostgresStore) UpdateIntentStatus(ctx context.Context, intentID uuid.UUID, expectedFrom, newStatus IntentStatus, actorID *uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payments: begin update intent status: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current IntentStatus
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM payment_intents
		WHERE id = $1
		FOR UPDATE`, intentID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("payments: update intent status %s: %w", intentID, ErrIntentNotFound)
		}
		return fmt.Errorf("payments: lock intent %s: %w", intentID, err)
	}
	if current != expectedFrom {
		return fmt.Errorf("payments: update intent status %s: status moved to %s: %w",
			intentID, current, ErrConflict)
	}

	setClause := "status = $2, updated_at = now()"
	if stamp := intentStatusTimestamp(newStatus); stamp != "" {
		setClause += ", " + stamp + " = now()"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment_intents
		SET `+setClause+`
		WHERE id = $1`, intentID, newStatus); err != nil {
		return fmt.Errorf("payments: update intent status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_intent_transitions (intent_id, from_status, to_status, actor_id, reason)
		VALUES ($1, $2, $3, $4, $5)`,
		intentID, current, newStatus, actorID, reason); err != nil {
		return fmt.Errorf("payments: insert intent transition: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("payments: commit update intent status: %w", err)
	}
	return nil
}

// InsertTransaction implements Store.
func (s *PostgresStore) InsertTransaction(ctx context.Context, t *Transaction) error {
	if t == nil {
		return fmt.Errorf("payments: insert transaction: transaction is required")
	}
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payment_transactions
			(id, intent_id, provider, provider_ref, status, amount_minor, currency, failure_reason)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, NULLIF($8, ''))
		RETURNING created_at, updated_at`,
		t.ID, t.IntentID, t.Provider, t.ProviderRef, t.Status, t.AmountMinor, t.Currency, t.FailureReason,
	).Scan(&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("payments: insert transaction: %w", ErrIntentNotFound)
		}
		return fmt.Errorf("payments: insert transaction: %w", err)
	}
	return nil
}

// GetTransaction implements Store. The EXISTS subquery re-applies the
// intent visibility predicate, so transactions of invisible intents read
// as missing.
func (s *PostgresStore) GetTransaction(ctx context.Context, id uuid.UUID, scope Scope) (Transaction, error) {
	pred, predArgs := scopePredicate(scope, "i.tenant_id", "i.created_by", "i.customer_id", 2)
	where := "t.id = $1"
	args := []any{id}
	if pred != "" {
		where += " AND EXISTS (SELECT 1 FROM payment_intents i WHERE i.id = t.intent_id AND " + pred + ")"
		args = append(args, predArgs...)
	}
	t, err := scanTransaction(s.pool.QueryRow(ctx, `
		SELECT `+transactionColumns+`
		FROM payment_transactions t
		WHERE `+where, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Transaction{}, fmt.Errorf("payments: get transaction %s: %w", id, ErrTransactionNotFound)
		}
		return Transaction{}, fmt.Errorf("payments: get transaction: %w", err)
	}
	return t, nil
}

// GetTransactionByProviderRef implements Store. Internal: the webhook path
// reached here only after signature verification, and there is no
// principal to scope by.
func (s *PostgresStore) GetTransactionByProviderRef(ctx context.Context, provider, providerRef string) (Transaction, error) {
	t, err := scanTransaction(s.pool.QueryRow(ctx, `
		SELECT `+transactionColumns+`
		FROM payment_transactions
		WHERE provider = $1 AND provider_ref = $2`, provider, providerRef))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Transaction{}, fmt.Errorf("payments: get transaction by ref %s: %w", providerRef, ErrTransactionNotFound)
		}
		return Transaction{}, fmt.Errorf("payments: get transaction by ref: %w", err)
	}
	return t, nil
}

// ListTransactions implements Store.
func (s *PostgresStore) ListTransactions(ctx context.Context, intentID uuid.UUID, scope Scope) ([]Transaction, error) {
	pred, predArgs := scopePredicate(scope, "i.tenant_id", "i.created_by", "i.customer_id", 2)
	where := "t.intent_id = $1"
	args := []any{intentID}
	if pred != "" {
		where += " AND EXISTS (SELECT 1 FROM payment_intents i WHERE i.id = t.intent_id AND " + pred + ")"
		args = append(args, predArgs...)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+transactionColumns+`
		FROM payment_transactions t
		WHERE `+where+`
		ORDER BY t.created_at ASC, t.id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("payments: list transactions: %w", err)
	}
	defer rows.Close()

	out := []Transaction{}
	for rows.Next() {
		t, err := scanTransaction(rows)
		if err != nil {
			return nil, fmt.Errorf("payments: scan transaction: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payments: list transactions: %w", err)
	}
	return out, nil
}

// ApplyTransactionOutcome implements Store: the exactly-once settlement.
// The transaction locks serialize concurrent webhooks/reconcilers; the
// status re-checks close every race the service-level check cannot.
func (s *PostgresStore) ApplyTransactionOutcome(ctx context.Context, p ApplyOutcomeParams) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("payments: begin apply outcome: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the transaction first (consistent lock order with the intent
	// second, everywhere in this package).
	var t Transaction
	err = tx.QueryRow(ctx, `
		SELECT `+transactionColumns+`
		FROM payment_transactions
		WHERE id = $1
		FOR UPDATE`, p.TransactionID).Scan(
		&t.ID, &t.IntentID, &t.Provider, &t.ProviderRef, &t.ProviderReceipt, &t.Status,
		&t.AmountMinor, &t.Currency, &t.FailureReason, &t.OccurredAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("payments: apply outcome %s: %w", p.TransactionID, ErrTransactionNotFound)
		}
		return false, fmt.Errorf("payments: lock transaction %s: %w", p.TransactionID, err)
	}
	if t.Status == p.NewTxStatus {
		// Already settled exactly this way: idempotent no-op.
		return false, nil
	}
	if t.Status != TxPending {
		return false, fmt.Errorf("payments: apply outcome %s: transaction is %s: %w",
			p.TransactionID, t.Status, ErrConflict)
	}

	occurred := p.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now()
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment_transactions
		SET status = $2, failure_reason = NULLIF($3, ''), occurred_at = $4,
		    provider_receipt = COALESCE(NULLIF($5, ''), provider_receipt), updated_at = now()
		WHERE id = $1`,
		t.ID, p.NewTxStatus, p.FailureReason, occurred, p.ProviderReceipt); err != nil {
		return false, fmt.Errorf("payments: settle transaction: %w", err)
	}

	// Lock and settle the intent.
	var current IntentStatus
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM payment_intents
		WHERE id = $1
		FOR UPDATE`, t.IntentID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("payments: apply outcome %s: intent missing: %w", t.IntentID, ErrIntentNotFound)
		}
		return false, fmt.Errorf("payments: lock intent %s: %w", t.IntentID, err)
	}
	if current == p.NewIntentStatus {
		// The intent already settled (e.g. reconciled a moment ago while
		// a replayed webhook was in flight); the transaction row above
		// was still PENDING, so completing it here is the convergence.
	} else if current != IntentProcessing {
		return false, fmt.Errorf("payments: apply outcome %s: intent is %s: %w",
			t.IntentID, current, ErrConflict)
	} else {
		setClause := "status = $2, updated_at = now()"
		if stamp := intentStatusTimestamp(p.NewIntentStatus); stamp != "" {
			setClause += ", " + stamp + " = now()"
		}
		if p.FailureReason != "" {
			setClause += ", failure_reason = $3"
		}
		args := []any{t.IntentID, p.NewIntentStatus}
		if p.FailureReason != "" {
			args = append(args, p.FailureReason)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payment_intents
			SET `+setClause+`
			WHERE id = $1`, args...); err != nil {
			return false, fmt.Errorf("payments: settle intent: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_intent_transitions (intent_id, from_status, to_status, reason)
			VALUES ($1, $2, $3, $4)`,
			t.IntentID, current, p.NewIntentStatus, p.Reason); err != nil {
			return false, fmt.Errorf("payments: insert settle transition: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("payments: commit apply outcome: %w", err)
	}
	return true, nil
}

// InsertRefund implements Store.
func (s *PostgresStore) InsertRefund(ctx context.Context, r *Refund) error {
	if r == nil {
		return fmt.Errorf("payments: insert refund: refund is required")
	}
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	r.Status = RefundRequested
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payment_refunds
			(id, intent_id, amount_minor, currency, status, provider, reason, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`,
		r.ID, r.IntentID, r.AmountMinor, r.Currency, r.Status, r.Provider, r.Reason, r.RequestedBy,
	).Scan(&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("payments: insert refund: %w", ErrIntentNotFound)
		}
		return fmt.Errorf("payments: insert refund: %w", err)
	}
	return nil
}

// refundScopePredicate renders the intent-visibility condition for
// refunds joined through their intent.
func refundScopeJoin(scope Scope, nextParam int) (string, []any) {
	pred, args := scopePredicate(scope, "i.tenant_id", "i.created_by", "i.customer_id", nextParam)
	if pred == "" {
		return "", nil
	}
	return "EXISTS (SELECT 1 FROM payment_intents i WHERE i.id = r.intent_id AND " + pred + ")", args
}

// GetRefund implements Store.
func (s *PostgresStore) GetRefund(ctx context.Context, id uuid.UUID, scope Scope) (Refund, error) {
	join, joinArgs := refundScopeJoin(scope, 2)
	where := "r.id = $1"
	args := []any{id}
	if join != "" {
		where += " AND " + join
		args = append(args, joinArgs...)
	}
	r, err := scanRefund(s.pool.QueryRow(ctx, `
		SELECT `+refundColumns+`
		FROM payment_refunds r
		WHERE `+where, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Refund{}, fmt.Errorf("payments: get refund %s: %w", id, ErrRefundNotFound)
		}
		return Refund{}, fmt.Errorf("payments: get refund: %w", err)
	}
	return r, nil
}

// UpdateRefundStatus implements Store. The FOR UPDATE lock serializes
// concurrent movers.
func (s *PostgresStore) UpdateRefundStatus(ctx context.Context, refundID uuid.UUID, expectedFrom, newStatus RefundStatus, providerRef, failureReason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payments: begin update refund status: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current RefundStatus
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM payment_refunds
		WHERE id = $1
		FOR UPDATE`, refundID).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("payments: update refund status %s: %w", refundID, ErrRefundNotFound)
		}
		return fmt.Errorf("payments: lock refund %s: %w", refundID, err)
	}
	if current != expectedFrom {
		return fmt.Errorf("payments: update refund status %s: status moved to %s: %w",
			refundID, current, ErrConflict)
	}

	setClause := "status = $2, updated_at = now()"
	args := []any{refundID, newStatus}
	if providerRef != "" {
		setClause += ", provider_ref = $3"
		args = append(args, providerRef)
	}
	if failureReason != "" {
		setClause += fmt.Sprintf(", failure_reason = $%d", len(args)+1)
		args = append(args, failureReason)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment_refunds
		SET `+setClause+`
		WHERE id = $1`, args...); err != nil {
		return fmt.Errorf("payments: update refund status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("payments: commit update refund status: %w", err)
	}
	return nil
}

// LatestRefundForIntent implements Store.
func (s *PostgresStore) LatestRefundForIntent(ctx context.Context, intentID uuid.UUID, scope Scope) (Refund, error) {
	join, joinArgs := refundScopeJoin(scope, 2)
	where := "r.intent_id = $1"
	args := []any{intentID}
	if join != "" {
		where += " AND " + join
		args = append(args, joinArgs...)
	}
	r, err := scanRefund(s.pool.QueryRow(ctx, `
		SELECT `+refundColumns+`
		FROM payment_refunds r
		WHERE `+where+`
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT 1`, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Refund{}, fmt.Errorf("payments: latest refund for intent %s: %w", intentID, ErrRefundNotFound)
		}
		return Refund{}, fmt.Errorf("payments: latest refund: %w", err)
	}
	return r, nil
}

// ClaimIdempotencyKey implements Store: INSERT ... ON CONFLICT DO NOTHING
// is the whole claim — the winner processes the work, everyone else reads
// the stored result.
func (s *PostgresStore) ClaimIdempotencyKey(ctx context.Context, key, scope, payloadHash string) (ClaimResult, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO payment_idempotency_keys (key, scope, payload_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING`,
		key, scope, payloadHash)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("payments: claim idempotency key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return ClaimResult{Claimed: true}, nil
	}
	var existing ClaimResult
	err = s.pool.QueryRow(ctx, `
		SELECT result, payload_hash
		FROM payment_idempotency_keys
		WHERE key = $1`, key).Scan(&existing.ExistingResult, &existing.ExistingPayloadHash)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("payments: read idempotency key: %w", err)
	}
	return existing, nil
}

// CompleteIdempotencyKey implements Store.
func (s *PostgresStore) CompleteIdempotencyKey(ctx context.Context, key, result string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE payment_idempotency_keys
		SET result = $2, completed_at = now()
		WHERE key = $1`, key, result)
	if err != nil {
		return fmt.Errorf("payments: complete idempotency key: %w", err)
	}
	return nil
}

// ListStaleIntents implements Store: PROCESSING intents whose newest
// transaction has been PENDING since before olderThan. Internal flow.
func (s *PostgresStore) ListStaleIntents(ctx context.Context, olderThan time.Time, limit int) ([]StaleIntent, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	rows, err := s.pool.Query(ctx, `
		WITH newest AS (
			SELECT DISTINCT ON (intent_id) id, intent_id, created_at
			FROM payment_transactions
			ORDER BY intent_id, created_at DESC, id DESC
		)
		SELECT `+intentColumns+`, `+transactionColumns+`
		FROM payment_intents
		JOIN payment_transactions ON payment_transactions.intent_id = payment_intents.id
		JOIN newest ON newest.id = payment_transactions.id
		WHERE payment_intents.status = 'PROCESSING'
		  AND payment_transactions.status = 'PENDING'
		  AND payment_transactions.created_at < $1
		ORDER BY payment_transactions.created_at ASC
		LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("payments: list stale intents: %w", err)
	}
	defer rows.Close()

	out := []StaleIntent{}
	for rows.Next() {
		var si StaleIntent
		dest := append(intentScanDest(&si.Intent), transactionScanDest(&si.Transaction)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("payments: scan stale intent: %w", err)
		}
		out = append(out, si)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payments: list stale intents: %w", err)
	}
	return out, nil
}

// intentScanDest and transactionScanDest build scan destinations in the
// exact column order the *Columns constants declare.
func intentScanDest(i *PaymentIntent) []any {
	return []any{&i.ID, &i.TenantID, &i.CustomerID, &i.CreatedBy, &i.AmountMinor, &i.Currency,
		&i.Status, &i.Provider, &i.ProviderRef, &i.JobID, &i.VehicleID, &i.Description,
		&i.FailureReason, &i.SucceededAt, &i.FailedAt, &i.CanceledAt, &i.CreatedAt, &i.UpdatedAt}
}

func transactionScanDest(t *Transaction) []any {
	return []any{&t.ID, &t.IntentID, &t.Provider, &t.ProviderRef, &t.ProviderReceipt, &t.Status,
		&t.AmountMinor, &t.Currency, &t.FailureReason, &t.OccurredAt, &t.CreatedAt, &t.UpdatedAt}
}

// isForeignKeyViolation reports whether err is a 23503 FK violation (an
// insert against a missing parent).
func isForeignKeyViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23503"
	}
	return false
}
