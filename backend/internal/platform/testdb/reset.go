// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package testdb is the integration lane's fast schema-reset helper. The
// integration suites need a clean database per test, and the obvious way to get
// one — DROP SCHEMA + re-run every embedded migration on each Setup — dominated
// the lane: the heaviest package alone remigrated ~180 times (~0.8s each). This
// package splits the cost. EnsureSchema migrates ONCE per test-binary process;
// every later test in that process rides the already-migrated schema and only
// Truncates the data. Migration cost drops from once-per-test to once-per-
// package, and a TRUNCATE is milliseconds. Correctness holds because no
// migration seeds reference data a test depends on — the only data-touching
// migration (person_social backfill) is a no-op on an empty database.
//
// TRUNCATE empties rows but does not revert schema changes, and the
// customfields engine is the one place in the system allowed to run a runtime
// ALTER TABLE (it adds cf_<slug> columns to record tables). A cf_ column added
// by one test would otherwise survive into the next, which reads it as a
// column that is already "taken platform-wide". So the reset also drops every
// leaked cf_ column — see dropCustomFieldColumns.
//
// The reset stays safe under the lane's -p 1: within a package process tests
// run serially, so nothing races the shared connection between Truncate and the
// next test. Across packages, each go-test binary is its own process (and, under
// the parallel runner, its own throwaway database), so migrateOnce is genuinely
// per-database.
package testdb

import (
	"context"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

// EnsureSchema migrates the test database exactly once per process. The first
// integration test to run pays the DROP SCHEMA + full embedded migration; every
// later test in the same process is a no-op here and resets via Truncate. Any
// caller may pass any owner connection to the same database — the migration runs
// on whichever connection wins the race to the sync.Once, and the result is the
// same schema for all of them.
func EnsureSchema(ctx context.Context, owner *pgx.Conn) error {
	migrateOnce.Do(func() {
		if _, err := owner.Exec(ctx,
			`DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
			migrateErr = err
			return
		}
		core, err := migrations.Core()
		if err != nil {
			migrateErr = err
			return
		}
		custom, err := migrations.Custom()
		if err != nil {
			migrateErr = err
			return
		}
		if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
			migrateErr = err
			return
		}
	})
	return migrateErr
}

// Truncate empties every data table (RESTART IDENTITY, CASCADE) so the next test
// sees a clean database without re-running migrations. The schema_migrations_*
// bookkeeping tables are preserved so EnsureSchema's once-per-process contract
// holds: re-running dbmigrate.Up on a later process (parallel runner, fresh
// clone) still sees an unmigrated database, while a truncate here leaves the
// applied-version ledger intact for the current process.
func Truncate(ctx context.Context, owner *pgx.Conn) error {
	rows, err := owner.Query(ctx, `
		SELECT quote_ident(tablename)
		FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename NOT LIKE 'schema_migrations_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	// The table list is built by concatenation because identifiers cannot be
	// bound parameters — but every name comes from quote_ident() over the
	// pg_tables system catalog, never from caller input, so it is injection-safe.
	// One statement: CASCADE resolves FK order, RESTART IDENTITY resets the few
	// serial sequences (most ids are client-side UUIDv7, so this is belt-and-braces).
	if _, err := owner.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` RESTART IDENTITY CASCADE`); err != nil {
		return err
	}
	return dropCustomFieldColumns(ctx, owner)
}

// dropCustomFieldColumns reverts the runtime DDL the customfields engine adds —
// the cf_<slug> columns it appends to record tables (people, deals, …) as the
// system's single sanctioned ALTER-TABLE chokepoint. TRUNCATE clears the rows
// but leaves the columns, so without this a cf_ column created by one test
// leaks into the next and is rejected as "taken platform-wide". No migrated
// baseline table carries a cf_-prefixed column, so every match here is a
// leaked custom field and safe to drop; DROP COLUMN cascades its generated
// cf_<slug>_check constraint with it.
func dropCustomFieldColumns(ctx context.Context, owner *pgx.Conn) error {
	rows, err := owner.Query(ctx, `
		SELECT quote_ident(table_name), quote_ident(column_name)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND column_name LIKE 'cf\_%'`)
	if err != nil {
		return err
	}
	var stmts []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return err
		}
		// Identifiers cannot be bound parameters, but both names come from
		// quote_ident() over information_schema, never from caller input, so
		// the concatenation is injection-safe (same posture as Truncate).
		stmts = append(stmts, `ALTER TABLE `+table+` DROP COLUMN `+column+` CASCADE`)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
