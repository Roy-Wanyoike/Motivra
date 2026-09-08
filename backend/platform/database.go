package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Postgres pool defaults tuned for a per-domain service.
const (
	defaultMaxConns       = int32(10)
	defaultMinConns       = int32(2)
	defaultConnLifetime   = 30 * time.Minute
	defaultConnIdleTime   = 5 * time.Minute
	defaultConnectTimeout = 10 * time.Second
	defaultPingTimeout    = 5 * time.Second
	defaultMigrateTimeout = 2 * time.Minute
)

// NewPostgres opens a pgx connection pool with Motivra's standard pool
// settings and verifies connectivity with a ping before returning.
func NewPostgres(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = defaultMaxConns
	cfg.MinConns = defaultMinConns
	cfg.MaxConnLifetime = defaultConnLifetime
	cfg.MaxConnIdleTime = defaultConnIdleTime
	cfg.ConnConfig.ConnectTimeout = defaultConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, defaultPingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// MigrateUp applies every up-migration found in migrationsFS (an embedded
// fs.FS rooted at the domain's migrations directory) using the given
// schema_migrations table name. Each domain owns its migration chain
// (ADR-0003) and runs this at boot against its own database or schema.
// It returns nil when the database is already up to date.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, migrationsFS fs.FS, table string) error {
	if table == "" {
		return errors.New("migrate: schema migrations table name is required")
	}
	mCtx, cancel := context.WithTimeout(ctx, defaultMigrateTimeout)
	defer cancel()

	sqlDB := sql.OpenDB(stdlib.GetConnector(*pool.Config().ConnConfig))
	defer sqlDB.Close()

	src, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	driver, err := postgres.WithInstance(sqlDB, &postgres.Config{MigrationsTable: table})
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "motivra", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() {
		_ = src.Close()
		_ = driver.Close()
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		if mCtx.Err() != nil || ctx.Err() != nil {
			return fmt.Errorf("migrate: %w", ctx.Err())
		}
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation, the most common expected conflict in domain services.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
