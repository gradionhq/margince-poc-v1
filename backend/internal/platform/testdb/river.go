// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsureRiverSchema layers River's own schema onto the already-migrated test
// database, at most once per database.
//
// Call it AFTER EnsureSchema. When EnsureSchema rebuilds, it opens with DROP
// SCHEMA public CASCADE, which takes River's tables with it — so a call that
// ran first would have its work destroyed.
//
// The guard is river_migration's EXISTENCE rather than a once-per-process flag,
// because what a process finds here depends on something no flag records:
// EnsureSchema drops River's tables when it rebuilds, and leaves them standing
// when it reuses an already-migrated clone. Probing the table answers both
// cases without either caller having to say which one it is.
//
// Presence is not currency, and on the reuse path this does not establish it —
// it inherits it. A reused clone is a file copy of margince_test, and the lane
// brings that template to head with THIS binary before any package starts:
// scripts/lib-testdb.sh migrate_template shells out to cmd/migrate up, which
// applies River's migrations alongside core and custom. So the tables this
// finds are as current as the template, and the template is as current as the
// run. A database reached any other way takes the rebuild instead, where the
// drop leaves nothing for the probe to find.
//
// The ledger cannot stand in for the table, and it is worth saying why the two
// can be trusted to agree now. Reset used to EMPTY river_migration while the
// tables stood: River read that as "unmigrated", replayed its first migration
// onto tables that already existed, and failed on SQLSTATE 42P07. resetTables
// excludes it now, for the reason it excludes schema_migrations_*. The table is
// still what to probe, because the table is what River collides with.
//
// The check-then-migrate window needs no lock because nothing else can be in it.
// Each package worker owns a private clone database for its whole run, and within
// that process the lane runs `go test -p 1` with no t.Parallel, so the probe and
// the migration are the only things touching that schema. The shared template is
// finished before any worker starts — the runner builds it, then fans out — so a
// worker never observes a half-prepared one.
//
// migrate arrives as a parameter rather than an import. The layering permits the
// import — platform may depend on platform — but taking it would link River's
// runtime into every integration binary that wants nothing from it beyond a
// migrated schema. Passing the migrator keeps this the one spelling of the probe
// for every caller, the integration harness and the jobs suite alike, and leaves
// the dependency where it is actually used.
func EnsureRiverSchema(ctx context.Context, ownerPool *pgxpool.Pool, migrate func(context.Context, *pgxpool.Pool) (int, error)) error {
	var present bool
	if err := ownerPool.QueryRow(ctx,
		`SELECT to_regclass('public.river_migration') IS NOT NULL`).Scan(&present); err != nil {
		return fmt.Errorf("checking the river schema: %w", err)
	}
	if present {
		return nil
	}
	if _, err := migrate(ctx, ownerPool); err != nil {
		return fmt.Errorf("applying the river schema: %w", err)
	}
	return nil
}
