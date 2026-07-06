// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

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

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

func TestEveryWorkspaceScopedTableForcesRowLevelSecurity(t *testing.T) {
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
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	rows, err := owner.Query(ctx, `
		SELECT c.table_name,
		       cl.relrowsecurity,
		       cl.relforcerowsecurity,
		       EXISTS (
		         SELECT 1 FROM pg_policies p
		         WHERE p.schemaname = 'public' AND p.tablename = c.table_name
		       ) AS has_policy
		FROM information_schema.columns c
		JOIN pg_class cl
		  ON cl.relname = c.table_name AND cl.relnamespace = 'public'::regnamespace
		WHERE c.table_schema = 'public'
		  AND c.column_name = 'workspace_id'
		  AND cl.relkind IN ('r','p') -- 'p': a future partitioned tenant table must not escape coverage
		ORDER BY c.table_name`)
	if err != nil {
		t.Fatalf("enumerating workspace-scoped tables: %v", err)
	}
	defer rows.Close()

	// The ratified non-RLS workspace_id tables (mirrored, with the same
	// rationale, in migrations/schema_fitness_integration_test.go):
	// booking_page is the slug→tenant RESOLVER (0036) — read to discover
	// which workspace to bind BEFORE any GUC exists, like `workspace`
	// itself; it carries no CRM record data.
	// preference_token is the token→tenant RESOLVER (0048) — read to
	// discover which workspace to bind for the no-login preference center
	// BEFORE any GUC exists, exactly like booking_page; it carries no CRM
	// record data beyond the person link + revocation.
	exempt := map[string]bool{"booking_page": true, "preference_token": true}

	checked := 0
	for rows.Next() {
		var table string
		var enabled, forced, hasPolicy bool
		if err := rows.Scan(&table, &enabled, &forced, &hasPolicy); err != nil {
			t.Fatal(err)
		}
		checked++
		if exempt[table] {
			if enabled || forced {
				t.Errorf("%s is RLS-exempt by rationale but HAS row security — retire the stale exemption", table)
			}
			continue
		}
		if !enabled {
			t.Errorf("%s: row level security is not ENABLEd", table)
		}
		if !forced {
			t.Errorf("%s: row level security is not FORCEd — the table owner bypasses every policy", table)
		}
		if !hasPolicy {
			t.Errorf("%s: no tenant-isolation policy exists — RLS without a policy denies nothing to the owner and everything to no one", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// An empty enumeration means the derivation itself broke (schema
	// name drift, migration failure) — that must fail, not pass green.
	if checked < 10 {
		t.Fatalf("only %d workspace-scoped tables enumerated; the schema derivation is broken", checked)
	}
}
