// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package jobs_test

// The mirror of testdb's own ledger gate, for River's.
//
// testdb.Reset used to EMPTY river_migration while River's tables stood — a
// state River reads as "not migrated", so it replays its first migration onto
// tables that already exist and fails on SQLSTATE 42P07. resetTables excludes it
// now, beside schema_migrations_%, for the reason a ledger is not test data.
//
// WHY THIS TEST EXISTS RATHER THAN THE SCOPE TEST ALREADY IN testdb: that one
// compares in ONE direction — it reports relations the reset FAILS to cover, and
// says nothing about scope that is wrongly present. Its independent census
// carries the same exclusion, so the two cannot disagree either. Delete
// `c.relname <> 'river_migration'` from resetTables as redundant-looking and
// every test in this repository still passes, because testdb/river.go probes the
// TABLE and never the ledger — the damage surfaces only in whichever migrator
// later reads it. Nothing else fails, so this is the thing that must.
//
// It lives in package jobs because this is where River's schema is applied
// through the real migrator; asserting it in testdb would mean testdb's own
// tests reaching for River, which is the import EnsureRiverSchema takes a
// function parameter to avoid.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

func TestResetPreservesRiversMigrationLedger(t *testing.T) {
	ctx := t.Context()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatalf("bringing the test schema to head: %v", err)
	}

	// Through the real migrator, on the real path, so the rows counted below are
	// the ones River itself wrote. Applied whether or not this process's
	// EnsureSchema rebuilt: on the reuse path the clone already carries them, and
	// EnsureRiverSchema is a no-op there.
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	if err := testdb.EnsureRiverSchema(ctx, ownerPool, jobs.Migrate); err != nil {
		t.Fatalf("applying the river schema: %v", err)
	}

	before := riverLedgerRows(ctx, t, owner)
	if before == 0 {
		// NOT "the migrator ran and wrote nothing" — EnsureRiverSchema no-ops on
		// an existing river_migration whatever it holds, so in the one reachable
		// state that lands here the migrator did not run at all: a template whose
		// ledger was emptied while its tables stood. A pre-fix
		// `make test-integration-serial` leaves exactly that, since it resets
		// against margince_test itself and ensure_template migrates rather than
		// rebuilds.
		t.Fatal("river_migration exists but is empty, so there is nothing here to preserve and every assertion below would hold for the wrong reason. The template's ledger was emptied while its tables stood; `make test-db-up` rebuilds it")
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if after := riverLedgerRows(ctx, t, owner); after != before {
		t.Errorf("Reset took river_migration from %d rows to %d. River reads an emptied ledger as an unmigrated database and replays its first migration onto tables that still exist, failing on SQLSTATE 42P07 — a migration ledger is not test data, and testdb's resetTables must keep excluding it",
			before, after)
	}
}

func riverLedgerRows(ctx context.Context, t *testing.T, owner *pgx.Conn) int {
	t.Helper()
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM river_migration`).Scan(&n); err != nil {
		t.Fatalf("counting river_migration: %v", err)
	}
	return n
}
