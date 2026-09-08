// Package jobsnextmigrations embeds the jobs domain SQL migration chain so
// the jobs service can apply it at boot via platform.MigrateUp (ADR-0003:
// every domain owns its migration chain).
package jobsnextmigrations

import (
	"embed"
	"io/fs"
)

// embedded holds every *.sql migration file, rooted at this directory.
//
//go:embed *.sql
var embedded embed.FS

// FS exposes the migration chain as an fs.FS for platform.MigrateUp.
var FS fs.FS = embedded
