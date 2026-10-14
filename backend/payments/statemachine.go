package payments

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrConflict is the sentinel wrapped by every illegal-transition error in
// the domain's three state machines. Callers check with
// errors.Is(err, ErrConflict) and translate it into a platform 409.
var ErrConflict = errors.New("payments: transition conflicts with current state")

// IntentStatus is a payment intent lifecycle state. The full lifecycle is
// enforced by the intentTransitions map below; nothing else in the domain
// may mutate an intent status outside this discipline.
type IntentStatus string

// The intent statuses, mirroring the payment_intents CHECK constraint in
// the migrations chain. The lifecycle is strictly linear per attempt:
//   - CREATED -> PROCESSING: the provider accepted the initiation (a
//     PENDING transaction exists and a webhook or query is expected).
//   - CREATED -> CANCELED: canceled before any provider round-trip.
//   - PROCESSING -> SUCCEEDED | FAILED: settled exactly once by a verified
//     webhook or the reconciliation query fallback.
//
// SUCCEEDED, FAILED and CANCELED are terminal: a retried payment is a NEW
// intent, mirroring the inspections corrections-as-new-sessions precedent.
const (
	IntentCreated    IntentStatus = "CREATED"
	IntentProcessing IntentStatus = "PROCESSING"
	IntentSucceeded  IntentStatus = "SUCCEEDED"
	IntentFailed     IntentStatus = "FAILED"
	IntentCanceled   IntentStatus = "CANCELED"
)

// intentTransitions is the single source of truth for legal intent moves.
var intentTransitions = map[IntentStatus][]IntentStatus{
	IntentCreated:    {IntentProcessing, IntentCanceled},
	IntentProcessing: {IntentSucceeded, IntentFailed},
}

// TransactionStatus is a provider settlement attempt state.
type TransactionStatus string

// The transaction statuses, mirroring the payment_transactions CHECK
// constraint. A transaction is created PENDING (provider accepted) or
// FAILED (provider rejected synchronously) and settles exactly once.
const (
	TxPending   TransactionStatus = "PENDING"
	TxSucceeded TransactionStatus = "SUCCEEDED"
	TxFailed    TransactionStatus = "FAILED"
)

// transactionTransitions is the single source of truth for legal
// transaction moves.
var transactionTransitions = map[TransactionStatus][]TransactionStatus{
	TxPending: {TxSucceeded, TxFailed},
}

// RefundStatus is a refund lifecycle state.
type RefundStatus string

// The refund statuses, mirroring the payment_refunds CHECK constraint.
const (
	RefundRequested RefundStatus = "REQUESTED"
	RefundCompleted RefundStatus = "COMPLETED"
	RefundFailed    RefundStatus = "FAILED"
)

// refundTransitions is the single source of truth for legal refund moves.
var refundTransitions = map[RefundStatus][]RefundStatus{
	RefundRequested: {RefundCompleted, RefundFailed},
}

// canTransition is the shared matrix walker for the three status machines.
// It returns nil when the move from -> to is legal. Otherwise it returns
// an error wrapping ErrConflict naming the current status, the requested
// status and the legal alternatives, so the error is actionable both in
// logs and in API responses. Unknown statuses have no entry in the map and
// therefore conflict — including self-transitions (a map entry from ->
// {from} would be required to make one legal, and none exists).
func canTransition[S ~string](transitions map[S][]S, all []S, from, to S) error {
	targets, known := transitions[from]
	if !known {
		return fmt.Errorf("%w: illegal transition %s -> %s: status %s has no outgoing transitions (terminal or unknown); known statuses are [%s]",
			ErrConflict, from, to, from, strings.Join(statusNames(all), " "))
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("%w: illegal transition %s -> %s; legal transitions from %s are [%s]",
		ErrConflict, from, to, from, strings.Join(statusNames(targets), " "))
}

// CanTransitionIntentStatus reports whether the intent move is legal.
func CanTransitionIntentStatus(from, to IntentStatus) error {
	return canTransition(intentTransitions, AllIntentStatuses(), from, to)
}

// CanTransitionTransactionStatus reports whether the transaction move is
// legal.
func CanTransitionTransactionStatus(from, to TransactionStatus) error {
	return canTransition(transactionTransitions, AllTransactionStatuses(), from, to)
}

// CanTransitionRefundStatus reports whether the refund move is legal.
func CanTransitionRefundStatus(from, to RefundStatus) error {
	return canTransition(refundTransitions, AllRefundStatuses(), from, to)
}

// AllIntentStatuses returns every known intent status in lifecycle order.
// Exported for validation in the HTTP layer and for the exhaustive matrix
// test.
func AllIntentStatuses() []IntentStatus {
	return []IntentStatus{IntentCreated, IntentProcessing, IntentSucceeded, IntentFailed, IntentCanceled}
}

// AllTransactionStatuses returns every known transaction status.
func AllTransactionStatuses() []TransactionStatus {
	return []TransactionStatus{TxPending, TxSucceeded, TxFailed}
}

// AllRefundStatuses returns every known refund status.
func AllRefundStatuses() []RefundStatus {
	return []RefundStatus{RefundRequested, RefundCompleted, RefundFailed}
}

// IsIntentTerminal reports whether s is a terminal intent status. Terminal
// intents are immutable: the transitions map has no outgoing edges, so
// CanTransitionIntentStatus always conflicts (and the database refuses the
// update independently).
func IsIntentTerminal(s IntentStatus) bool {
	_, known := intentTransitions[s]
	return !known && isKnownIntentStatus(s)
}

// IsTransactionTerminal reports whether s is a terminal transaction status.
func IsTransactionTerminal(s TransactionStatus) bool {
	_, known := transactionTransitions[s]
	return !known && isKnownTransactionStatus(s)
}

// IsRefundTerminal reports whether s is a terminal refund status.
func IsRefundTerminal(s RefundStatus) bool {
	_, known := refundTransitions[s]
	return !known && isKnownRefundStatus(s)
}

// isKnown* report membership in the enumerations.
func isKnownIntentStatus(s IntentStatus) bool {
	for _, k := range AllIntentStatuses() {
		if k == s {
			return true
		}
	}
	return false
}

func isKnownTransactionStatus(s TransactionStatus) bool {
	for _, k := range AllTransactionStatuses() {
		if k == s {
			return true
		}
	}
	return false
}

func isKnownRefundStatus(s RefundStatus) bool {
	for _, k := range AllRefundStatuses() {
		if k == s {
			return true
		}
	}
	return false
}

// statusNames renders statuses as plain strings for error messages.
func statusNames[S ~string](statuses []S) []string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	sort.Strings(names)
	return names
}
