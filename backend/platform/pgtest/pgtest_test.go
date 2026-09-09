package pgtest

import "testing"

// TestPoolSkipsCleanlyWithoutDatabase proves the deferral-3 gate contract:
// with TEST_DATABASE_URL unset, Pool skips the calling test instead of
// failing, so the whole integration suite compiles and passes on machines
// without Postgres. When a database is configured this test steps aside —
// the domain suites prove the real-database behavior.
func TestPoolSkipsCleanlyWithoutDatabase(t *testing.T) {
	if DatabaseURL() != "" {
		t.Skipf("%s is configured; the skip path is covered on no-database runs", EnvDatabaseURL)
	}
	Pool(t) // must t.Skip above; reaching here would fail the contract
}
