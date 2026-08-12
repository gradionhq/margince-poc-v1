// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The metering counters under real RLS: usage accumulates per
// (workspace, day, task, tier), the month sum feeds the budget bands,
// and one tenant's spend is invisible to another — the ai_usage table
// is tenant data like any other.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// meterFreshDatabase resets the data to a clean slate and ensures the schema is
// migrated (once per process, then a fast data-only reset — see package testdb),
// returning the owner connection and the RLS-bound app DSN.
func meterFreshDatabase(t *testing.T, ctx context.Context) (*pgx.Conn, string) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
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
		t.Fatalf("migrating schema: %v", err)
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatalf("resetting database: %v", err)
	}
	return owner, appDSN
}

// meterWorkspace plants one tenant row through the owner connection.
func meterWorkspace(t *testing.T, ctx context.Context, owner *pgx.Conn, slug string) ids.UUID {
	t.Helper()
	var raw string
	if err := owner.QueryRow(ctx,
		`INSERT INTO workspace (slug) VALUES ($1) RETURNING id`,
		slug).Scan(&raw); err != nil {
		t.Fatalf("workspace insert: %v", err)
	}
	wsID, err := ids.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return wsID
}
