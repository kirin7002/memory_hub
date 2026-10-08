package migrations

import "embed"

// Files contains the versioned PostgreSQL migrations.
//
//go:embed *.sql
var Files embed.FS
