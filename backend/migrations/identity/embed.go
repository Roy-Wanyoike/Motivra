// Package identitymigrations embeds the identity domain's SQL migration
// chain so the identity service binary can apply it at boot with
// platform.MigrateUp. Embedding must happen inside this directory because
// go:embed cannot reference files outside the package (ADR-0003: each
// domain owns its migration chain under backend/migrations/<domain>).
package identitymigrations

import (
	"embed"
	"io/fs"
)

// migrations holds every paired .up.sql/.down.sql file in this directory.
//
//go:embed *.sql
var migrations embed.FS

// FS exposes the embedded migration chain as an fs.FS rooted at the
// migration directory, ready for platform.MigrateUp with the identity
// domain's schema_migrations table.
var FS fs.FS = migrations
