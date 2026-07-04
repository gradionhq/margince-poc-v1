// Package migrations embeds the SQL migration namespaces (ADR-0017):
// core/ is upstream-owned and sequentially numbered; custom/ is fork-owned
// and timestamp-ordered; jurisdiction packs bring their own third
// namespace. Apply order is always core, then custom, then packs.
package migrations

import (
	"embed"

	"github.com/gradionhq/margince/backend/internal/pgmigrate"
)

//go:embed core custom
var files embed.FS

// Core returns the upstream-owned namespace.
func Core() (pgmigrate.Namespace, error) {
	ms, err := pgmigrate.Load(files, "core")
	if err != nil {
		return pgmigrate.Namespace{}, err
	}
	return pgmigrate.Namespace{Name: "core", Migrations: ms}, nil
}

// Custom returns the fork-owned namespace. Empty upstream by design: a
// fork's agent-authored migrations land here with x_-prefixed columns and
// never collide with a core upgrade (ADR-0017 Amendment 1).
func Custom() (pgmigrate.Namespace, error) {
	ms, err := pgmigrate.Load(files, "custom")
	if err != nil {
		return pgmigrate.Namespace{}, err
	}
	return pgmigrate.Namespace{Name: "custom", Migrations: ms}, nil
}
