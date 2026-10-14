package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	identitymigrations "github.com/Roy-Wanyoike/Motivra/backend/migrations/identity"
	"github.com/Roy-Wanyoike/Motivra/backend/platform/pgtest"
)

// TestAuditLogAppendOnlyEnforcedByDatabase proves the integrity guarantee
// for audit records is physical, not conventional (integrity issue #83):
// UPDATE and DELETE raise trigger exceptions while INSERT keeps working.
// Gated on TEST_DATABASE_URL like the other integration suites.
func TestAuditLogAppendOnlyEnforcedByDatabase(t *testing.T) {
	pool := pgtest.Pool(t)
	pgtest.Migrate(t, pool, identitymigrations.FS, "schema_migrations_identity")
	pgtest.Truncate(t, pool, identityTables...)
	defer pgtest.Truncate(t, pool, identityTables...)

	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`INSERT INTO audit_log (action, object_type, object_id) VALUES ('identity.user.registered', 'user', 'test-object')`)
	require.NoError(t, err, "INSERT into audit_log must keep working")

	_, err = pool.Exec(ctx, `UPDATE audit_log SET action = 'tampered'`)
	require.Error(t, err, "UPDATE on audit_log must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")

	_, err = pool.Exec(ctx, `DELETE FROM audit_log`)
	require.Error(t, err, "DELETE on audit_log must raise the append-only exception")
	require.Contains(t, err.Error(), "append-only")
}
