// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Tenant-isolation coverage as a fitness function (ADR-0018, data-model
// §1.3): carrying a workspace_id column IS the obligation, so the table
// list is derived from the live schema — a future migration cannot add
// a tenant table and forget its RLS without failing here. ENABLE-only
// looks secure and is not: without FORCE the table owner bypasses every
// policy, so both flags and the policy itself are asserted per table.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// freshlyMigratedOwner connects as owner and returns a clean, migrated schema
// (migrated once per process, then a fast data-only reset — see package
// testdb) — the schema-derivation arrange step, needing only the owner DSN (no
// app pool is involved in a coverage sweep).
func freshlyMigratedOwner(t *testing.T) *pgx.Conn {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
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
		t.Fatalf("migrating schema: %v", err)
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatalf("resetting database: %v", err)
	}
	return owner
}
