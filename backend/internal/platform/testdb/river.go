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
// Call it AFTER EnsureSchema. EnsureSchema opens the first integration test in a
// process with DROP SCHEMA public CASCADE, which takes River's tables with it, so
// a call that ran first would have its work destroyed.
//
// The guard is river_migration's existence rather than a once-per-process flag,
// because neither of the two things that can invalidate the schema is visible to
// a flag: EnsureSchema drops the tables outright, and Reset empties
// river_migration's applied-version ledger while leaving the tables standing.
// The migrator is itself idempotent, but River reads that emptied ledger and
// would replay its first migration against tables that still exist, failing on
// SQLSTATE 42P07 — so the table, not the ledger, is what must be probed.
//
// The check-then-migrate window needs no lock because nothing else can be in it.
// Each package worker owns a private clone database for its whole run, and within
// that process the lane runs `go test -p 1` with no t.Parallel, so the probe and
// the migration are the only things touching that schema. The shared template is
// finished before any worker starts — the runner builds it, then fans out — so a
// worker never observes a half-prepared one.
//
// migrate arrives as a parameter rather than an import: the River migrator lives
// in platform/jobs, and a testdb that reached for it would make the lane's schema
// helper depend on the job runtime it is only ever asked to prepare a database
// for. Passing it keeps this the one spelling of the probe for every caller —
// the integration harness and the jobs suite alike.
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
