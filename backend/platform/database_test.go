package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewPostgresRejectsBadURL runs without any database.
func TestNewPostgresRejectsBadURL(t *testing.T) {
	_, err := NewPostgres(context.Background(), "not-a-postgres-url")
	require.Error(t, err)
}

// TestMigrateUpRequiresTable verifies the guard without a database.
func TestMigrateUpRequiresTable(t *testing.T) {
	err := MigrateUp(context.Background(), nil, nil, "")
	require.Error(t, err)
}

// TestIsUniqueViolation exercises the pg error classifier without a DB.
func TestIsUniqueViolation(t *testing.T) {
	require.False(t, IsUniqueViolation(nil))
	require.False(t, IsUniqueViolation(context.DeadlineExceeded))
}

// The following tests require a real PostgreSQL instance (provided by CI
// service containers via TEST_DATABASE_URL; run locally with
// docker compose -f docker-compose.dev.yml up -d postgres).
//
// func TestMigrateUpAppliesChain(t *testing.T) {
// 	pool, err := NewPostgres(ctx, os.Getenv("TEST_DATABASE_URL")) ...
// }
