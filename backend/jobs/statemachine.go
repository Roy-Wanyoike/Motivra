package jobs

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Status is a job lifecycle state. The full lifecycle is enforced by the
// transitions map below; nothing else in the domain may mutate a job
// status outside this discipline.
type Status string

// The 18 job statuses, mirroring the jobs CHECK constraint in the
// migrations chain.
const (
	StatusCreated          Status = "CREATED"
	StatusTriaging         Status = "TRIAGING"
	StatusDispatching      Status = "DISPATCHING"
	StatusAssigned         Status = "ASSIGNED"
	StatusAccepted         Status = "ACCEPTED"
	StatusEnRoute          Status = "EN_ROUTE"
	StatusArrived          Status = "ARRIVED"
	StatusInspection       Status = "INSPECTION"
	StatusDiagnosis        Status = "DIAGNOSIS"
	StatusEstimate         Status = "ESTIMATE"
	StatusAwaitingApproval Status = "AWAITING_APPROVAL"
	StatusApproved         Status = "APPROVED"
	StatusRepairing        Status = "REPAIRING"
	StatusVerification     Status = "VERIFICATION"
	StatusCompleted        Status = "COMPLETED"
	StatusCancelled        Status = "CANCELLED"
	StatusFailed           Status = "FAILED"
	StatusEscalated        Status = "ESCALATED"
)

// ErrConflict is the sentinel wrapped by every illegal-transition error.
// Callers check with errors.Is(err, ErrConflict) and translate it into a
// platform 409 (platform.ErrConflict).
var ErrConflict = errors.New("jobs: transition conflicts with current job state")

// transitions is the single source of truth for legal status moves. Every
// status change must pass through CanTransition, which consults this map.
// The special edges carry domain meaning:
//   - ASSIGNED -> DISPATCHING: dispatch reassignment (technician reassigned
//     before acceptance; the job returns to the dispatching pool).
//   - ACCEPTED -> DISPATCHING: technician released the job before going
//     en route.
//   - REPAIRING -> VERIFICATION is progress; VERIFICATION -> REPAIRING is
//     rework when verification fails.
var transitions = map[Status][]Status{
	StatusCreated:          {StatusTriaging, StatusCancelled},
	StatusTriaging:         {StatusDispatching, StatusCancelled},
	StatusDispatching:      {StatusAssigned, StatusEscalated, StatusCancelled},
	StatusAssigned:         {StatusAccepted, StatusDispatching, StatusCancelled},
	StatusAccepted:         {StatusEnRoute, StatusDispatching, StatusCancelled},
	StatusEnRoute:          {StatusArrived, StatusCancelled},
	StatusArrived:          {StatusInspection, StatusFailed},
	StatusInspection:       {StatusDiagnosis},
	StatusDiagnosis:        {StatusEstimate},
	StatusEstimate:         {StatusAwaitingApproval},
	StatusAwaitingApproval: {StatusApproved, StatusEscalated, StatusCancelled},
	StatusApproved:         {StatusRepairing},
	StatusRepairing:        {StatusVerification, StatusFailed},
	StatusVerification:     {StatusCompleted, StatusRepairing},
}

// transitionSets is derived from transitions at init for O(1) legality
// checks and for the systematic illegal-pair tests.
var transitionSets = func() map[Status]map[Status]struct{} {
	sets := make(map[Status]map[Status]struct{}, len(transitions))
	for from, tos := range transitions {
		set := make(map[Status]struct{}, len(tos))
		for _, to := range tos {
			set[to] = struct{}{}
		}
		sets[from] = set
	}
	return sets
}()

// allStatuses lists every known status in lifecycle order. It is the
// authoritative enumeration: sources of the transitions map plus terminal
// statuses plus ESCALATED (a deliberate dead end that is not terminal).
var allStatuses = []Status{
	StatusCreated,
	StatusTriaging,
	StatusDispatching,
	StatusAssigned,
	StatusAccepted,
	StatusEnRoute,
	StatusArrived,
	StatusInspection,
	StatusDiagnosis,
	StatusEstimate,
	StatusAwaitingApproval,
	StatusApproved,
	StatusRepairing,
	StatusVerification,
	StatusCompleted,
	StatusCancelled,
	StatusFailed,
	StatusEscalated,
}

// AllStatuses returns every known status in lifecycle order. Exported for
// validation in the HTTP layer and for exhaustive tests.
func AllStatuses() []Status {
	out := make([]Status, len(allStatuses))
	copy(out, allStatuses)
	return out
}

// legalTargets returns the sorted legal target statuses from s, or nil
// when s is unknown or terminal.
func legalTargets(s Status) []Status {
	tos, ok := transitions[s]
	if !ok {
		return nil
	}
	out := make([]Status, len(tos))
	copy(out, tos)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// CanTransition reports whether the move from -> to is legal. It returns
// nil when legal. Otherwise it returns an error wrapping ErrConflict with
// a full message naming the current status, the requested status and the
// legal alternatives, so the error is actionable both in logs and in API
// responses.
func CanTransition(from, to Status) error {
	targets, known := transitions[from]
	if !known {
		return fmt.Errorf("%w: illegal transition %s -> %s: status %s has no outgoing transitions (terminal or dead end); known statuses are [%s]",
			ErrConflict, from, to, from, strings.Join(statusNames(AllStatuses()), " "))
	}
	for _, t := range targets {
		if t == to {
			return nil
		}
	}
	return fmt.Errorf("%w: illegal transition %s -> %s; legal transitions from %s are [%s]",
		ErrConflict, from, to, from, strings.Join(statusNames(legalTargets(from)), " "))
}

// IsTerminal reports whether s is a terminal status (COMPLETED, CANCELLED,
// FAILED). Terminal jobs are immutable: the transitions map intentionally
// has no outgoing edges from them, so CanTransition always conflicts.
func IsTerminal(s Status) bool {
	return s == StatusCompleted || s == StatusCancelled || s == StatusFailed
}

// statusNames renders statuses as plain strings for error messages.
func statusNames(statuses []Status) []string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	return names
}
