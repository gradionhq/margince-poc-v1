// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package migrations embeds the SQL migration namespaces (ADR-0017):
// core/ is upstream-owned and sequentially numbered; custom/ is fork-owned
// and timestamp-ordered; jurisdiction packs bring their own third
// namespace. Apply order is always core, then custom, then packs.
package migrations

import (
	"embed"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

//go:embed core custom
var files embed.FS

// Core returns the upstream-owned namespace.
func Core() (dbmigrate.Namespace, error) {
	ms, err := dbmigrate.Load(files, "core")
	if err != nil {
		return dbmigrate.Namespace{}, err
	}
	return dbmigrate.Namespace{Name: "core", Migrations: ms}, nil
}

// Custom returns the fork-owned namespace. Empty upstream by design: a
// fork's agent-authored migrations land here with x_-prefixed columns and
// never collide with a core upgrade (ADR-0017 Amendment 1).
func Custom() (dbmigrate.Namespace, error) {
	ms, err := dbmigrate.Load(files, "custom")
	if err != nil {
		return dbmigrate.Namespace{}, err
	}
	return dbmigrate.Namespace{Name: "custom", Migrations: ms}, nil
}
