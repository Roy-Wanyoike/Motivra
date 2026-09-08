package jobs

import (
	"errors"
	"strings"
	"testing"
)

// TestLegalTransitionsMatrix walks the transitions map itself and requires
// every recorded pair to be accepted. Any edit to the map that introduces
// a typo therefore fails here.
func TestLegalTransitionsMatrix(t *testing.T) {
	for from, tos := range transitions {
		for _, to := range tos {
			from, to := from, to
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				t.Parallel()
				if err := CanTransition(from, to); err != nil {
					t.Fatalf("CanTransition(%s, %s) = %v, want nil", from, to, err)
				}
			})
		}
	}
}

// TestIllegalTransitionsSystematically generates every pair of known
// statuses NOT present in the transitions map and requires all of them to
// be rejected with an error wrapping ErrConflict. Same-status pairs are
// included (a status is never its own successor).
func TestIllegalTransitionsSystematically(t *testing.T) {
	statuses := AllStatuses()
	for _, from := range statuses {
		for _, to := range statuses {
			if isLegalPair(from, to) {
				continue
			}
			from, to := from, to
			t.Run(string(from)+"-!->"+string(to), func(t *testing.T) {
				t.Parallel()
				err := CanTransition(from, to)
				if err == nil {
					t.Fatalf("CanTransition(%s, %s) = nil, want ErrConflict", from, to)
				}
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("CanTransition(%s, %s) = %v, want error wrapping ErrConflict", from, to, err)
				}
				if !strings.Contains(err.Error(), string(from)) || !strings.Contains(err.Error(), string(to)) {
					t.Fatalf("CanTransition(%s, %s) error %q must name both statuses", from, to, err)
				}
			})
		}
	}
}

// TestUnknownStatusRejected requires any move from a status that is not
// one of the 18 enumerated values to be rejected with ErrConflict.
func TestUnknownStatusRejected(t *testing.T) {
	unknown := []Status{"SHIPPED", "", "created", "completed", "NOT_A_STATUS"}
	for _, from := range unknown {
		for _, to := range AllStatuses() {
			if err := CanTransition(from, to); !errors.Is(err, ErrConflict) {
				t.Fatalf("CanTransition(%q, %s) = %v, want ErrConflict", from, to, err)
			}
		}
	}
}

// TestTerminalStatuses pins the terminal set and requires terminal
// statuses to have no outgoing edges in the map.
func TestTerminalStatuses(t *testing.T) {
	terminal := map[Status]bool{
		StatusCompleted: true,
		StatusCancelled: true,
		StatusFailed:    true,
	}
	for _, s := range AllStatuses() {
		want := terminal[s]
		if got := IsTerminal(s); got != want {
			t.Fatalf("IsTerminal(%s) = %v, want %v", s, got, want)
		}
	}
	for s := range terminal {
		if _, hasEdges := transitions[s]; hasEdges {
			t.Fatalf("terminal status %s must have no outgoing transitions", s)
		}
		for _, to := range AllStatuses() {
			if err := CanTransition(s, to); !errors.Is(err, ErrConflict) {
				t.Fatalf("terminal %s -> %s = %v, want ErrConflict", s, to, err)
			}
		}
	}
}

// TestEscalatedIsNonTerminalDeadEnd pins that ESCALATED is reachable but
// has no outgoing edges and is not reported terminal.
func TestEscalatedIsNonTerminalDeadEnd(t *testing.T) {
	if IsTerminal(StatusEscalated) {
		t.Fatal("ESCALATED must not be terminal")
	}
	if !isLegalPair(StatusDispatching, StatusEscalated) || !isLegalPair(StatusAwaitingApproval, StatusEscalated) {
		t.Fatal("ESCALATED must be reachable from DISPATCHING and AWAITING_APPROVAL")
	}
	for _, to := range AllStatuses() {
		if err := CanTransition(StatusEscalated, to); !errors.Is(err, ErrConflict) {
			t.Fatalf("ESCALATED -> %s = %v, want ErrConflict (dead end)", to, err)
		}
	}
}

// TestAllStatusesEnumeration pins the size, uniqueness and completeness of
// the status enumeration.
func TestAllStatusesEnumeration(t *testing.T) {
	statuses := AllStatuses()
	if len(statuses) != 18 {
		t.Fatalf("AllStatuses() has %d entries, want 18", len(statuses))
	}
	seen := make(map[Status]bool, len(statuses))
	for _, s := range statuses {
		if seen[s] {
			t.Fatalf("duplicate status %s in AllStatuses()", s)
		}
		seen[s] = true
	}
	for from := range transitions {
		if !seen[from] {
			t.Fatalf("transitions source %s missing from AllStatuses()", from)
		}
	}
	for _, tos := range transitions {
		for _, to := range tos {
			if !seen[to] {
				t.Fatalf("transitions target %s missing from AllStatuses()", to)
			}
		}
	}
}

// TestEveryKnownStatusHasAnEntryFrom pins that exactly the 14 non-terminal,
// non-dead-end statuses are keys of the transitions map, and that every
// other known status is reachable from CREATED by some legal path.
func TestTransitionsMapShape(t *testing.T) {
	if len(transitions) != 14 {
		t.Fatalf("transitions map has %d sources, want 14", len(transitions))
	}
	reachable := map[Status]bool{StatusCreated: true}
	for changed := true; changed; {
		changed = false
		for from, tos := range transitions {
			if reachable[from] {
				for _, to := range tos {
					if !reachable[to] {
						reachable[to] = true
						changed = true
					}
				}
			}
		}
	}
	for _, s := range AllStatuses() {
		if !reachable[s] {
			t.Fatalf("status %s is not reachable from CREATED", s)
		}
	}
}

func isLegalPair(from, to Status) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}
