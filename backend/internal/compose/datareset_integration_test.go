// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// TestPreserveSetIntegrity is the fitness rail for the mass delete: every
// preserved table must still exist as a real workspace_id base table (a rename
// or drop that left preservedResetTables stale would silently sweep it), and no
// preserved table may appear in the sweep target set. Derived from
// information_schema — independent of resetTargetTables' pg_catalog query — so
// it genuinely fails on schema drift.
func TestPreserveSetIntegrity(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.table_name
			FROM information_schema.columns c
			JOIN information_schema.tables t
			  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
			WHERE c.table_schema = 'public'
			  AND c.column_name = 'workspace_id'
			  AND t.table_type = 'BASE TABLE'`)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			existing[n] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for name := range preservedResetTables {
			if !existing[name] {
				t.Errorf("preserved table %q is not a current workspace_id base table (renamed/dropped?) — it would be swept", name)
			}
		}
		targets, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		for _, tgt := range targets {
			if preservedResetTables[tgt] {
				t.Errorf("preserved table %q appears in the sweep target set", tgt)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("integrity query: %v", err)
	}
}

// TestClearWorkspaceOutboxScopesToWorkspace proves the one sweep target with
// NO RLS backstop — event_outbox has no workspace_id column, so its tenant
// boundary rests entirely on the envelope text match — actually scopes to the
// bound workspace: this workspace's rows go, a foreign workspace's survive.
func TestClearWorkspaceOutboxScopesToWorkspace(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	foreign := ids.NewV7()
	e.WsExec(t, `INSERT INTO event_outbox (stream, envelope) VALUES ('t', jsonb_build_object('workspace_id', $1::text))`, e.WS)
	e.WsExec(t, `INSERT INTO event_outbox (stream, envelope) VALUES ('t', jsonb_build_object('workspace_id', $1::text))`, foreign)

	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return clearWorkspaceOutbox(ctx, tx)
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->>'workspace_id' = $1`, e.WS.String()); n != 0 {
		t.Fatalf("this workspace's outbox rows = %d, want 0", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE envelope->>'workspace_id' = $1`, foreign.String()); n != 1 {
		t.Fatalf("foreign workspace's outbox rows = %d, want 1 (must survive)", n)
	}
}
