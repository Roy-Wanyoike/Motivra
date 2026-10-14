package inspections

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Status is an inspection session lifecycle state. The full lifecycle is
// enforced by the transitions map below; nothing else in the domain may
// mutate a session status outside this discipline.
type Status string

// The 4 session statuses, mirroring the inspection_sessions CHECK
// constraint in the migrations chain. The lifecycle is strictly linear: an
// inspection is opened, worked, submitted and reported.
const (
	StatusCreated         Status = "CREATED"
	StatusInProgress      Status = "IN_PROGRESS"
	StatusSubmitted       Status = "SUBMITTED"
	StatusReportAvailable Status = "REPORT_AVAILABLE"
)

// ErrConflict is the sentinel wrapped by every illegal-transition error.
// Callers check with errors.Is(err, ErrConflict) and translate it into a
// platform 409 (platform.ErrConflict).
var ErrConflict = errors.New("inspections: transition conflicts with current session state")

// transitions is the single source of truth for legal status moves. Every
// status change must pass through CanTransition, which consults this map.
// The edges carry domain meaning:
//   - CREATED -> IN_PROGRESS: the inspector started the walk (started_at
//     stamped). Findings and evidence may only be recorded while
//     IN_PROGRESS — the database triggers refuse them in every other state.
//   - IN_PROGRESS -> SUBMITTED: the inspector attests the inspection is
//     complete; findings and evidence are sealed (database-enforced) and
//     the report integrity preconditions are validated first.
//   - SUBMITTED -> REPORT_AVAILABLE: the verifiable report is assembled
//     and issued (report.issued.v1).
//
// There is deliberately no way back: a corrected inspection is a NEW
// session, mirroring the vehicles corrections-as-new-records precedent.
var transitions = map[Status][]Status{
	StatusCreated:    {StatusInProgress},
	StatusInProgress: {StatusSubmitted},
	StatusSubmitted:  {StatusReportAvailable},
}

// allStatuses lists every known status in lifecycle order.
var allStatuses = []Status{
	StatusCreated,
	StatusInProgress,
	StatusSubmitted,
	StatusReportAvailable,
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
		return fmt.Errorf("%w: illegal transition %s -> %s: status %s has no outgoing transitions (terminal or unknown); known statuses are [%s]",
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

// IsTerminal reports whether s is a terminal status (REPORT_AVAILABLE).
// Terminal sessions are immutable: the transitions map intentionally has
// no outgoing edges, so CanTransition always conflicts.
func IsTerminal(s Status) bool {
	return s == StatusReportAvailable
}

// statusNames renders statuses as plain strings for error messages.
func statusNames(statuses []Status) []string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	return names
}

// isKnownStatus reports whether status is one of the enumerated statuses.
func isKnownStatus(status Status) bool {
	for _, s := range allStatuses {
		if s == status {
			return true
		}
	}
	return false
}
