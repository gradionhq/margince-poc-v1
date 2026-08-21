// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package migrations embeds the SQL migration namespaces (ADR-0017):
// core/ is upstream-owned and named for the unix second the migration was
// written; custom/ is fork-owned and stamped YYYYMMDDHHMMSS; jurisdiction
// packs bring their own third namespace. Apply order is always core, then
// custom, then packs.
//
// core/ carries two version shapes. 0001 is the BASELINE: the schema core
// builds, as the shape it arrives at rather than the path it took, in one
// file whose ORDER is a dependency order — extensions and the ext schema,
// the functions a column default can call, every table, the functions whose
// bodies read a table, then every constraint, index, trigger, grant and
// reference row, because a foreign key names two tables and person/lead
// reference each other. Everything after the baseline is named for the unix
// second it was written. The baseline's version is zero-padded, so it sorts
// below every ten-digit stamp, and the runner's string ordering puts the
// two in the order they were written.
//
// A database built by migrations that predate the baseline cannot be moved
// onto it: the baseline reuses 0001, its ledger records 0001 under another
// name, and dbmigrate stops there rather than skip a migration as done. That
// stop is the intended outcome — rebuild the database (make dev-fresh).
//
// What the baseline BUILDS is committed too, in testdata/head_catalog.txt,
// and TestMigrationsBuildTheCommittedSchema compares the two. So a migration
// added here shows its schema effect as a diff a reviewer can read, and the
// next consolidation is checkable the same way this one was
// (scripts/migration-baseline.sh verify).
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
