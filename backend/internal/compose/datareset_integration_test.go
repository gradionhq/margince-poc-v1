// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
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
		return sweepWorkspaceData(ctx, tx, tables)
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

// TestResetRunRestoresBootstrapState is the orchestration's end-to-end
// proof: a wrong confirmation is rejected without touching data, and the
// correct one sweeps the domain, re-seeds module defaults exactly as
// bootstrap does, preserves identity, and leaves one audit_log row
// recording the reset itself.
func TestResetRunRestoresBootstrapState(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	e.SeedPerson(t, "Alice", nil)
	// A pre-reset staged event, marked by its stream so the seeders' own outbox
	// writes cannot be mistaken for it: the run must leave nothing for the relay
	// to ship into the streams it purges.
	e.WsExec(t, `INSERT INTO event_outbox (stream, envelope) VALUES ('pre-reset', jsonb_build_object('workspace_id', $1::text))`, e.WS)

	h := dataResetHandlers{
		pool:       e.Pool,
		schemaPool: nil,
		seeds:      deployconfig.Seeds{},
		env:        runtimeenv.Development,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if _, err := h.run(ctx, "wrong"); !errors.Is(err, errResetConfirmationMismatch) {
		t.Fatalf("bad confirmation: want errResetConfirmationMismatch, got %v", err)
	}
	// The rejected attempt must not have touched anything.
	if got := e.WsCount(t, "SELECT count(*) FROM person"); got != 1 {
		t.Fatalf("person count after rejected reset = %d, want 1 (untouched)", got)
	}

	sum, err := h.run(ctx, "Authz")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if sum.TablesCleared == 0 {
		t.Fatal("expected some tables cleared")
	}
	if got := e.WsCount(t, "SELECT count(*) FROM person"); got != 0 {
		t.Errorf("person count after reset = %d, want 0", got)
	}
	if got := e.WsCount(t, "SELECT count(*) FROM stage"); got < 1 {
		t.Errorf("stage count after reset = %d, want >= 1 (pipeline re-seeded)", got)
	}
	if got := e.WsCount(t, "SELECT count(*) FROM app_user"); got != 3 {
		t.Errorf("app_user count after reset = %d, want 3 (identity preserved)", got)
	}
	if got := e.WsCount(t, "SELECT count(*) FROM audit_log WHERE action='reset_data'"); got != 1 {
		t.Errorf("audit_log reset_data rows = %d, want 1", got)
	}
	if got := e.WsCount(t, `SELECT count(*) FROM event_outbox WHERE stream = 'pre-reset'`); got != 0 {
		t.Errorf("pre-reset staged events = %d, want 0 — the relay would ship them into the streams the reset just purged", got)
	}
}

// resetBudgetIncumbent is an arbitrary configured incumbent: its identity does
// not matter to the purge, only that a metered call leaves counters under the
// workspace's ovb:<ws>:… prefix for the reset to find.
const resetBudgetIncumbent = "acme"

// TestResetDataAuditEvidenceCarriesTheSameCacheKeyTallyAsTheResponse:
// cache_keys_deleted is one number with one meaning. Every Redis surface a reset
// purges — the bus's dedupe marks and the overlay budget's counters — is cleared
// before the sweep's transaction opens, so the audit row written inside it, the
// 200 body and the completion log line all report the same total. A purge that
// drifted after the commit would leave the PERMANENT record under-reporting
// while the response over-reported, under one key name.
func TestResetDataAuditEvidenceCarriesTheSameCacheKeyTallyAsTheResponse(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	meter := budgettest.Meter(t, budgettest.SmallConfig(resetBudgetIncumbent))
	// Spend through the meter's own public API rather than hand-writing a key,
	// so the counters the reset purges are exactly what real traffic leaves.
	if err := meter.ConsumeSearch(principal.WithWorkspaceID(context.Background(), e.WS), resetBudgetIncumbent, 1); err != nil {
		t.Fatalf("seeding a budget counter: %v", err)
	}

	h := dataResetHandlers{
		pool:   e.Pool,
		seeds:  deployconfig.Seeds{},
		env:    runtimeenv.Development,
		budget: meter,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	counts, err := h.run(ctx, "Authz")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if counts.CacheKeys == 0 {
		t.Fatal("cache_keys_deleted = 0 although a budget counter was spent; the assertion below would hold vacuously")
	}
	recorded := e.WsCount(t,
		`SELECT (evidence->>'cache_keys_deleted')::int FROM audit_log WHERE action = 'reset_data'`)
	if recorded != counts.CacheKeys {
		t.Errorf("audit evidence cache_keys_deleted = %d, response reports %d — the durable record and the reply disagree about what the same key name counts",
			recorded, counts.CacheKeys)
	}
}

// TestDropResetCustomFieldColumns proves the DDL finalize in isolation — a
// fake cf_* column added directly via the owner pool (standing in for a
// customfields definition that outlived a reset) is dropped, without
// exercising the full customfields engine.
func TestDropResetCustomFieldColumns(t *testing.T) {
	sp := integration.SchemaPool(t)
	ctx := context.Background()

	if _, err := sp.Exec(ctx, `ALTER TABLE person ADD COLUMN cf_zzz text`); err != nil {
		t.Fatalf("seeding fake cf_ column: %v", err)
	}
	// cf_zzz is real schema on a database sibling tests in this package share;
	// drop it on every exit path so a failure here never leaks a column into
	// TestPreserveSetIntegrity / TestSweepTargetsCarryNoDeleteBlockingTrigger,
	// which both introspect the live schema. IF NOT EXISTS: the assertion below
	// proves the reset drop already removed it on the success path.
	t.Cleanup(func() {
		if _, err := sp.Exec(context.Background(), `ALTER TABLE person DROP COLUMN IF EXISTS cf_zzz`); err != nil {
			t.Errorf("cleaning up cf_zzz: %v", err)
		}
	})

	if err := dropResetCustomFieldColumns(ctx, sp); err != nil {
		t.Fatalf("dropResetCustomFieldColumns: %v", err)
	}

	var remaining int
	if err := sp.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE 'cf\_%'`).Scan(&remaining); err != nil {
		t.Fatalf("checking remaining cf_ columns: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining cf_ columns = %d, want 0", remaining)
	}
}

// TestSweepTargetsCarryNoDeleteBlockingTrigger is the forward safety rail the
// preserve-set check cannot give on its own: a table the sweep TARGETS must not
// carry a DELETE-firing trigger. Today only the append-only ledgers (audit_log,
// system_log) have one, and both are preserved. If a future workspace_id table
// arrives with a delete guard — an append-only or otherwise protected store —
// and is not added to preservedResetTables, it would either abort the sweep at
// runtime or be wiped against its guard's intent. This turns that silent
// runtime hazard into a test failure that forces a conscious classification.
func TestSweepTargetsCarryNoDeleteBlockingTrigger(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		targets, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		targetSet := make(map[string]bool, len(targets))
		for _, name := range targets {
			targetSet[name] = true
		}
		// tgtype bit 0x08 marks a trigger that fires on DELETE; tgisinternal
		// excludes FK-enforcement triggers so only real guards remain.
		rows, err := tx.Query(ctx, `
			SELECT c.relname, t.tgname
			FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public'
			  AND NOT t.tgisinternal
			  AND (t.tgtype & 8) <> 0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var table, trigger string
			if err := rows.Scan(&table, &trigger); err != nil {
				return err
			}
			if targetSet[table] {
				t.Errorf("sweep target %q carries DELETE-firing trigger %q — a protected/append-only table must be listed in preservedResetTables, not swept", table, trigger)
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("trigger scan: %v", err)
	}
}

// TestResetReturnsAnOverlayWorkspaceToNativeMode: a reset restores first-boot
// state, and a first-boot installation is native.
//
// The workspace row is in the preserved set — it carries the organization, so
// the sweep must not delete it — but the overlay-mode columns living on that
// row are configuration a connect flow wrote, not identity. Everything overlay
// mode depends on IS swept: the incumbent connection, the mirror, the budget
// counters. Leaving the mode behind therefore strands the installation claiming
// to read from an incumbent it no longer has a connection to, with every read
// dispatching to a mirror that has nothing in it.
//
// The two columns move together because the schema requires it:
// CHECK ((x_sor_mode = 'overlay') = (x_incumbent IS NOT NULL)).
func TestResetReturnsAnOverlayWorkspaceToNativeMode(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	e.WsExec(t, `UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot' WHERE id = $1`, e.WS)

	h := dataResetHandlers{
		pool:  e.Pool,
		seeds: deployconfig.Seeds{},
		env:   runtimeenv.Development,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(ctx, "Authz"); err != nil {
		t.Fatalf("run: %v", err)
	}

	var mode string
	var incumbent *string
	if err := e.Pool.QueryRow(ctx,
		`SELECT x_sor_mode, x_incumbent FROM workspace WHERE id = $1`, e.WS).Scan(&mode, &incumbent); err != nil {
		t.Fatalf("reading the workspace's mode back: %v", err)
	}
	if mode != "native" {
		t.Errorf("x_sor_mode = %q, want native — the install still reads from an incumbent the reset disconnected it from", mode)
	}
	if incumbent != nil {
		t.Errorf("x_incumbent = %q, want NULL", *incumbent)
	}
	if got := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE action = 'reset_data' AND evidence->>'sor_mode_reverted' = 'true'`); got != 1 {
		t.Errorf("reset_data rows recording the mode revert = %d, want 1 — a flip this consequential belongs in the permanent record", got)
	}
}

// TestResetLeavesANativeWorkspaceAlone: the flip is conditional, so a native
// installation's reset claims no mode change in its evidence.
func TestResetLeavesANativeWorkspaceAlone(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	h := dataResetHandlers{
		pool:  e.Pool,
		seeds: deployconfig.Seeds{},
		env:   runtimeenv.Development,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(ctx, "Authz"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE action = 'reset_data' AND evidence->>'sor_mode_reverted' = 'true'`); got != 0 {
		t.Errorf("evidence claims a mode revert on an install that was already native (%d rows)", got)
	}
}
