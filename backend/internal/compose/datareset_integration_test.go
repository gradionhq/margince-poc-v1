// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// TestSweepWorkspaceDataClearsDomainKeepsIdentity is the reset engine's core
// behavioural proof: domain rows are gone, identity survives, and the
// append-only audit ledger is untouched by the sweep it is itself gated by.
func TestSweepWorkspaceDataClearsDomainKeepsIdentity(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	e.SeedPerson(t, "Alice", nil)
	e.SeedOrg(t, "Acme", nil)

	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		tables, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		if err := sweepWorkspaceData(ctx, tx, tables); err != nil {
			return err
		}
		return clearWorkspaceOutbox(ctx, tx)
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := e.WsCount(t, "SELECT count(*) FROM person"); got != 0 {
		t.Errorf("person count after sweep = %d, want 0", got)
	}
	if got := e.WsCount(t, "SELECT count(*) FROM organization"); got != 0 {
		t.Errorf("organization count after sweep = %d, want 0", got)
	}
	// The harness seeds three humans (Rep1, Rep2, Rep3); identity must
	// survive a reset untouched.
	if got := e.WsCount(t, "SELECT count(*) FROM app_user"); got != 3 {
		t.Errorf("app_user count after sweep = %d, want 3 (identity preserved)", got)
	}
	// SeedPerson/SeedOrg each wrote an audit_log row as a side effect of the
	// store write shape; the ledger is append-only and must survive the sweep.
	if got := e.WsCount(t, "SELECT count(*) FROM audit_log"); got < 1 {
		t.Errorf("audit_log count after sweep = %d, want >= 1 (ledger preserved)", got)
	}
}

// TestEveryWorkspaceTableIsSweptOrExplicitlyPreserved is the fitness/guard
// function: it independently enumerates every workspace_id base table and
// fails if one is neither a sweep target nor explicitly preserved, so a
// newly added tenant table can never silently escape classification.
func TestEveryWorkspaceTableIsSweptOrExplicitlyPreserved(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		targets, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		got := map[string]bool{}
		for _, tName := range targets {
			got[tName] = true
		}
		// Independently list ALL workspace_id base tables; each must be either
		// a sweep target or explicitly preserved — never neither.
		rows, err := tx.Query(ctx, `
			SELECT c.relname FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_attribute a ON a.attrelid = c.oid
			WHERE n.nspname='public' AND c.relkind='r' AND a.attname='workspace_id'
			  AND a.attnum>0 AND NOT a.attisdropped AND c.relname NOT LIKE 'schema_migrations_%'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			if !got[name] && !preservedResetTables[name] {
				t.Errorf("table %q is neither swept nor preserved — classify it", name)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("guard query: %v", err)
	}
}
