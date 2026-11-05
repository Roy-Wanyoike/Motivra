package jobs

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Regression gate for issue #209: PostgresStore.ListTransitions selected
// eight columns but scanned seven destinations, and pgx v5 rejects every
// such row with "number of field descriptions must equal number of scan
// destinations" — the transitions read path always errored against real
// PostgreSQL while the fake store and the skipped PG-gated tests masked it.
//
// These tests pin the scan arity of every shared SELECT list in store.go
// against its scan helper WITHOUT a live database: an arityRow stand-in
// records how many destinations the helper hands to pgx, and the count must
// equal the column count of the corresponding column-list constant. A
// future column added to a constant (or dropped from a scan) fails here at
// `go test`, not in staging. Column ORDER (not just count) is proven by the
// PG-gated integration suite (TEST_DATABASE_URL), which reads real rows
// back through these helpers.

// arityRow is a pgx.Row stand-in that records the scan-destination count
// and succeeds, so scan helpers can be exercised without a database.
type arityRow struct {
	destinations int
}

func (r *arityRow) Scan(dest ...any) error {
	r.destinations = len(dest)
	return nil
}

// columnCount counts the columns of a shared SELECT-list constant. The
// jobs column constants are plain comma-separated identifiers (no embedded
// commas), so splitting on the separator is exact.
func columnCount(columns string) int {
	return len(strings.Split(columns, ","))
}

// TestStoreScanArityMatchesSelectedColumns asserts, for every shared
// column-list constant in store.go, that its scan helper scans exactly as
// many destinations as the SELECT carries.
func TestStoreScanArityMatchesSelectedColumns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		columns string
		scan    func(row pgx.Row) error
	}{
		{
			name:    "service_requests",
			columns: requestColumns,
			scan: func(row pgx.Row) error {
				_, err := scanRequest(row)
				return err
			},
		},
		{
			name:    "jobs",
			columns: jobColumns,
			scan: func(row pgx.Row) error {
				_, err := scanJob(row)
				return err
			},
		},
		{
			name:    "job_transitions",
			columns: transitionColumns,
			scan: func(row pgx.Row) error {
				_, err := scanTransition(row)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := &arityRow{}
			require.NoError(t, tt.scan(row))
			require.Equal(t, columnCount(tt.columns), row.destinations,
				"scan destinations must equal the %s SELECT column count (issue #209)", tt.name)
		})
	}
}
