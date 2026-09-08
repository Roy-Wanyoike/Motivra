// Package vehiclesmigrations embeds the vehicles domain SQL migration chain
// so the vehicles service can apply it at boot via platform.MigrateUp
// (ADR-0003: every domain owns its migration chain).
package vehiclesmigrations

import "embed"

// FS holds every *.sql migration file, rooted at this directory.
//
//go:embed *.sql
var FS embed.FS
