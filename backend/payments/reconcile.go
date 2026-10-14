package payments

import (
	"context"
	"fmt"
	"time"

	"github.com/Roy-Wanyoike/Motivra/backend/platform"
)

// Reconciler is the reconciliation job skeleton (issue #62): the
// provider-query fallback that repairs missed webhooks. A PENDING
// transaction older than StaleAfter is asked about directly; a terminal
// answer applies the same exactly-once settlement the webhook path uses,
// so the ledger converges no matter which path lands first
// (ARCHITECTURE.md §7: provider down → retry → alternate → reconcile).
//
// The job is a bounded batch run, driven by the cmd ticker and/or the
// operator route — not an unbounded loop inside the domain.
type Reconciler struct {
	store     Store
	providers map[string]Provider
	publisher platform.Publisher
	metrics   *Metrics

	// StaleAfter bounds how long a PENDING transaction waits for its
	// webhook before reconciliation queries the provider about it.
	StaleAfter time.Duration
	// BatchLimit caps one run's scan size (the next tick picks up more).
	BatchLimit int
	// nowFunc is overridable in tests.
	nowFunc func() time.Time
}

// NewReconciler returns a Reconciler. metrics and publisher may be nil.
func NewReconciler(store Store, providers map[string]Provider, publisher platform.Publisher, metrics *Metrics, staleAfter time.Duration) *Reconciler {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	limit := DefaultReconcileBatch
	return &Reconciler{
		store:      store,
		providers:  providers,
		publisher:  publisher,
		metrics:    metrics,
		StaleAfter: staleAfter,
		BatchLimit: limit,
		nowFunc:    time.Now,
	}
}

// Defaults for the reconciliation skeleton.
const (
	// DefaultStaleAfter is the webhook grace window.
	DefaultStaleAfter = 2 * time.Minute
	// DefaultReconcileBatch bounds one run.
	DefaultReconcileBatch = 100
)

// ReconcileResult reports one run.
type ReconcileResult struct {
	// Scanned counts the stale intents the run examined.
	Scanned int
	// Repaired counts transactions settled by this run (the convergence
	// metric: a webhook was missed and the query fallback repaired it).
	Repaired int
	// StillPending counts transactions the provider has not settled yet.
	StillPending int
	// Errors counts provider/store failures inside the run (each run
	// continues past individual failures).
	Errors int
}

// Run executes one reconciliation batch.
func (r *Reconciler) Run(ctx context.Context) (ReconcileResult, error) {
	cutoff := r.nowFunc().Add(-r.StaleAfter)
	limit := r.BatchLimit
	if limit <= 0 {
		limit = DefaultReconcileBatch
	}
	stale, err := r.store.ListStaleIntents(ctx, cutoff, limit)
	if err != nil {
		r.metrics.ReconciliationRun(ReconcileError, 0)
		return ReconcileResult{}, fmt.Errorf("payments: reconcile: %w", err)
	}

	result := ReconcileResult{Scanned: len(stale)}
	r.metrics.ReconciliationDrift(len(stale))
	for _, si := range stale {
		provider, err := r.providerFor(si.Transaction.Provider)
		if err != nil {
			result.Errors++
			continue
		}
		query, err := provider.Query(ctx, si.Transaction.ProviderRef)
		if err != nil {
			// Provider down is the designed-for case: leave the row
			// pending; the next run queries again.
			result.Errors++
			continue
		}
		if query.Status == TxPending {
			result.StillPending++
			continue
		}

		newIntentStatus := IntentSucceeded
		eventType := EventTransactionSucceededV1
		if query.Status == TxFailed {
			newIntentStatus = IntentFailed
			eventType = EventTransactionFailedV1
		}
		applied, err := r.store.ApplyTransactionOutcome(ctx, ApplyOutcomeParams{
			TransactionID:   si.Transaction.ID,
			NewTxStatus:     query.Status,
			NewIntentStatus: newIntentStatus,
			ProviderReceipt: query.ProviderTransactionRef,
			FailureReason:   query.FailureReason,
			OccurredAt:      query.OccurredAt,
			Reason:          "reconciliation",
		})
		if err != nil {
			result.Errors++
			continue
		}
		if applied {
			result.Repaired++
			if query.Status == TxSucceeded {
				r.metrics.PaymentSucceeded()
			} else {
				r.metrics.PaymentFailed()
			}
			if r.publisher != nil {
				actor := ""
				e, err := platform.NewEvent(eventType, si.Transaction.ID.String(), tenantString(si.Intent.TenantID), actor, "", TransactionSettledPayload{
					IntentID:        si.Intent.ID,
					TransactionID:   si.Transaction.ID,
					Provider:        si.Transaction.Provider,
					ProviderRef:     si.Transaction.ProviderRef,
					ProviderReceipt: query.ProviderTransactionRef,
					AmountMinor:     si.Transaction.AmountMinor,
					Currency:        si.Transaction.Currency,
					SettledBy:       "reconciliation",
					FailureReason:   query.FailureReason,
				})
				if err == nil {
					err = r.publisher.Publish(ctx, EventDomain, e)
				}
				r.metrics.EventPublished(eventType, err)
			}
		}
	}

	outcome := ReconcileClean
	switch {
	case result.Errors > 0:
		outcome = ReconcileError
	case result.Repaired > 0:
		outcome = ReconcileRepaired
	}
	r.metrics.ReconciliationRun(outcome, result.StillPending)
	return result, nil
}

// providerFor resolves the adapter by name.
func (r *Reconciler) providerFor(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("payments: reconcile: provider %q is not configured", name)
	}
	return p, nil
}
