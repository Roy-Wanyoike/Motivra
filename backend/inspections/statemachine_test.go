package inspections

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestLegalTransitionsMatrix walks the transitions map itself and requires
// every recorded pair to be accepted. Any edit to the map that introduces
// a typo therefore fails here (mirrors the jobs guard test).
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
// included (a status is never its own successor — corrections are new
// sessions, never a rewound one).
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
// one of the 4 enumerated values to be rejected with ErrConflict.
func TestUnknownStatusRejected(t *testing.T) {
	unknown := []Status{"SHIPPED", "", "created", "in_progress", "NOT_A_STATUS"}
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
		StatusReportAvailable: true,
	}
	for _, s := range AllStatuses() {
		if got := IsTerminal(s); got != terminal[s] {
			t.Fatalf("IsTerminal(%s) = %v, want %v", s, got, terminal[s])
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

// TestAllStatusesEnumeration pins the size, uniqueness and completeness of
// the status enumeration.
func TestAllStatusesEnumeration(t *testing.T) {
	statuses := AllStatuses()
	if len(statuses) != 4 {
		t.Fatalf("AllStatuses() has %d entries, want 4", len(statuses))
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

// TestTransitionsMapShape pins that exactly the 3 non-terminal statuses are
// keys of the transitions map, that the machine is strictly linear (one
// successor each — there is deliberately no way back: a corrected
// inspection is a NEW session), and that every status is reachable from
// CREATED.
func TestTransitionsMapShape(t *testing.T) {
	if len(transitions) != 3 {
		t.Fatalf("transitions map has %d sources, want 3", len(transitions))
	}
	for from, tos := range transitions {
		if len(tos) != 1 {
			t.Fatalf("status %s has %d outgoing edges, want exactly 1 (strictly linear machine)", from, len(tos))
		}
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

// TestHappyPathWalksTheWholeMachine drives the full lifecycle through the
// public entry points: CREATED -> IN_PROGRESS -> SUBMITTED ->
// REPORT_AVAILABLE.
func TestHappyPathWalksTheWholeMachine(t *testing.T) {
	t.Parallel()
	svc, _, pub, _ := newFixture()
	ctx := context.Background()

	inspector := uuid.New()
	sess := &Session{VehicleClass: ClassSedan, InspectorID: inspector}
	require.NoError(t, svc.CreateSession(ctx, sess))

	started, err := svc.Start(ctx, sess.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, started.Status)
	require.NotNil(t, started.StartedAt, "started_at must be stamped")

	template, err := TemplateByID(started.TemplateID)
	require.NoError(t, err)
	for _, key := range template.RequiredItems() {
		_, err = svc.RecordFinding(ctx, started.ID, &Finding{
			ItemKey: key, Result: ResultPass, Severity: SeverityGreen,
		}, inspector, staffScope(inspector))
		require.NoError(t, err)
	}
	_, err = svc.AttachEvidence(ctx, started.ID, &Evidence{
		Kind: EvidenceKindPhoto, ContentHash: validHash("e1"), CapturedAt: time.Now().UTC().Add(-time.Minute),
	}, inspector, staffScope(inspector))
	require.NoError(t, err)

	submitted, err := svc.Submit(ctx, started.ID, inspector, staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, StatusSubmitted, submitted.Status)
	require.NotNil(t, submitted.SubmittedAt, "submitted_at must be stamped")

	report, err := svc.IssueReport(ctx, started.ID, inspector, "final check", staffScope(inspector))
	require.NoError(t, err)
	require.Equal(t, ReportVersionInitial, report.Version)

	final, err := svc.GetSession(ctx, started.ID, opScope())
	require.NoError(t, err)
	require.Equal(t, StatusReportAvailable, final.Status)
	require.NotNil(t, final.ReportedAt, "reported_at must be stamped")

	for _, want := range []string{EventSessionCreated, EventSessionStarted, EventSessionSubmitted, EventReportIssued} {
		require.Len(t, pub.ofType(t, want), 1, "want exactly one %s event", want)
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
