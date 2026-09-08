//go:build tools

// tools.go pins the shared Motivra dependency set in go.mod so that domain
// agents never need to edit dependency versions. This file is never compiled
// into any binary (build tag `tools`); its blank imports exist only to keep
// the shared requirements in go.mod when `go mod tidy` runs.
//
// Owned by the platform/release engineering zone (issue #3).
package motivra

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/google/uuid"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/nats-io/nats.go"
	_ "github.com/redis/go-redis/v9"
	_ "github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/require"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/metric"
	_ "go.opentelemetry.io/otel/sdk"
	_ "go.opentelemetry.io/otel/trace"
	_ "go.temporal.io/sdk/activity"
	_ "go.temporal.io/sdk/client"
	_ "go.temporal.io/sdk/worker"
	_ "go.temporal.io/sdk/workflow"
	_ "golang.org/x/crypto/bcrypt"
)
