// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// The rails under ResetWorkspaceConfig. Its column list is derived from the
// live catalog, so the two ways it can go wrong are both schema drift the Go
// code cannot see: a preserved name that no longer matches a column (the
// installation's identity silently starts being restored), and a config column
// `= DEFAULT` cannot legally write (the whole reset aborts at runtime). Both
// are asserted here, against the schema as migrated.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedConfigWorkspace inserts one workspace to exercise the restore against
// and returns its id. The database persists across binary runs, so the slug
// carries the id's random tail — the leading bytes are a millisecond timestamp
// and two runs in the same minute would collide on workspace_slug_unique.
func seedConfigWorkspace(t *testing.T, owner *pgx.Conn) ids.UUID {
	t.Helper()
	slug := "wsconfig-" + ids.NewV7().String()[24:]
	var ws ids.UUID
	if err := owner.QueryRow(context.Background(),
		`INSERT INTO workspace (name, slug, base_currency, timezone)
		 VALUES ($1, $2, 'EUR', 'Europe/Berlin') RETURNING id`, slug, slug).Scan(&ws); err != nil {
		t.Fatalf("seeding the workspace: %v", err)
	}
	return ws
}

// TestResetWorkspaceConfigRestoresSettingsAndKeepsIdentity is the behavioural
// proof, over the two settings the row carries today: an installation with
// auto-enrich switched off and flipped into overlay mode comes back to exactly
// what a fresh bootstrap leaves, while the name, currency and zone bootstrap
// took from the deployment file are untouched.
func TestResetWorkspaceConfigRestoresSettingsAndKeepsIdentity(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()
	ws := seedConfigWorkspace(t, owner)

	// Move every setting off its default. The two overlay columns move
	// together because x_overlay_iff_incumbent admits no other state.
	if _, err := owner.Exec(ctx, `
		UPDATE workspace
		   SET capture_auto_enrich = false, x_sor_mode = 'overlay', x_incumbent = 'hubspot'
		 WHERE id = $1`, ws); err != nil {
		t.Fatalf("configuring the workspace away from its defaults: %v", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
		return ResetWorkspaceConfig(wsCtx, tx)
	}); err != nil {
		t.Fatalf("ResetWorkspaceConfig: %v", err)
	}

	var autoEnrich bool
	var mode, name, currency, timezone string
	var incumbent *string
	if err := owner.QueryRow(ctx, `
		SELECT capture_auto_enrich, x_sor_mode, x_incumbent, name, base_currency, timezone
		  FROM workspace WHERE id = $1`, ws).
		Scan(&autoEnrich, &mode, &incumbent, &name, &currency, &timezone); err != nil {
		t.Fatalf("reading the workspace back: %v", err)
	}
	if !autoEnrich {
		t.Error("capture_auto_enrich = false, want true — the setting outlived the reset that claimed to restore first-boot state")
	}
	if mode != "native" {
		t.Errorf("x_sor_mode = %q, want native", mode)
	}
	if incumbent != nil {
		t.Errorf("x_incumbent = %q, want NULL", *incumbent)
	}
	if currency != "EUR" || timezone != "Europe/Berlin" || name == "" {
		t.Errorf("identity was rewritten: name=%q base_currency=%q timezone=%q — a reset wipes the data, not the installation",
			name, currency, timezone)
	}
}

// TestResetWorkspaceConfigLeavesOtherWorkspacesAlone: workspace is the one
// table outside RLS (data-model §1.2), so nothing under this statement stops
// it from restoring every row in the table. The bound GUC is the whole
// isolation, which makes a co-tenant's settings surviving the property worth
// asserting rather than assuming.
func TestResetWorkspaceConfigLeavesOtherWorkspacesAlone(t *testing.T) {
	owner, pool := setupIdentityDB(t)
	ctx := context.Background()
	mine := seedConfigWorkspace(t, owner)
	theirs := seedConfigWorkspace(t, owner)

	if _, err := owner.Exec(ctx,
		`UPDATE workspace SET capture_auto_enrich = false WHERE id = $1`, theirs); err != nil {
		t.Fatalf("configuring the co-tenant: %v", err)
	}

	wsCtx := principal.WithWorkspaceID(ctx, mine)
	if err := database.WithWorkspaceTx(wsCtx, pool, func(tx pgx.Tx) error {
		return ResetWorkspaceConfig(wsCtx, tx)
	}); err != nil {
		t.Fatalf("ResetWorkspaceConfig: %v", err)
	}

	var autoEnrich bool
	if err := owner.QueryRow(ctx,
		`SELECT capture_auto_enrich FROM workspace WHERE id = $1`, theirs).Scan(&autoEnrich); err != nil {
		t.Fatalf("reading the co-tenant back: %v", err)
	}
	if autoEnrich {
		t.Error("the co-tenant's capture_auto_enrich was restored too — one installation's reset reconfigured another's")
	}
}

// TestPreservedWorkspaceColumnsAreRealAndExcluded is the stale-name rail: each
// preserved name must still be a column of the workspace table. A rename or a
// drop that left the set behind would not fail anything — the name simply
// stops matching, and the column it was protecting quietly joins the restore
// set on the next reset.
func TestPreservedWorkspaceColumnsAreRealAndExcluded(t *testing.T) {
	_, pool := setupIdentityDB(t)
	ctx := context.Background()

	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		// information_schema, not the pg_catalog query under test, so this
		// genuinely fails on drift rather than agreeing with itself.
		rows, err := tx.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'workspace'`)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			existing[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for name := range preservedWorkspaceColumns {
			if !existing[name] {
				t.Errorf("preserved column %q is not a current workspace column (renamed/dropped?) — a reset would start restoring what it names", name)
			}
		}
		cols, err := workspaceConfigColumns(ctx, tx)
		if err != nil {
			return err
		}
		for _, col := range cols {
			if preservedWorkspaceColumns[col] {
				t.Errorf("preserved column %q appears in the restore set", col)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("column introspection: %v", err)
	}
}

// TestEveryWorkspaceConfigColumnCanTakeItsDeclaredDefault is the forward rail
// the preserved-set check cannot give: the restore writes `= DEFAULT`, which
// on a column declaring no default writes NULL. For a NOT NULL column that
// aborts the whole reset transaction — the sweep, the re-seed and the audit
// row with it — at runtime, on an installation an operator is already wiping.
// A column added as NOT NULL DEFAULT … passes; one added NOT NULL and
// backfilled without keeping a default fails here, where the fix is cheap.
func TestEveryWorkspaceConfigColumnCanTakeItsDeclaredDefault(t *testing.T) {
	_, pool := setupIdentityDB(t)
	ctx := context.Background()

	if err := database.WithInfraTx(ctx, pool, func(tx pgx.Tx) error {
		cols, err := workspaceConfigColumns(ctx, tx)
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			t.Fatal("the workspace row carries no configuration column at all — every assertion here would hold vacuously")
		}
		for _, col := range cols {
			var notNull, hasDefault bool
			if err := tx.QueryRow(ctx, `
				SELECT a.attnotnull, a.atthasdef
				FROM pg_attribute a
				JOIN pg_class c ON c.oid = a.attrelid
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relname = 'workspace' AND a.attname = $1`,
				col).Scan(&notNull, &hasDefault); err != nil {
				return err
			}
			if notNull && !hasDefault {
				t.Errorf("config column %q is NOT NULL with no declared default — `SET %s = DEFAULT` writes NULL and aborts the reset; give the column a default or declare it preserved",
					col, col)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("column introspection: %v", err)
	}
}
