// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	// The harness seeds four humans (Rep1, Rep2, Rep3 and the admin seat);
	// identity must survive a reset untouched.
	if got := e.WsCount(t, "SELECT count(*) FROM app_user"); got != 4 {
		t.Errorf("app_user count after sweep = %d, want 4 (identity preserved)", got)
	}
	// SeedPerson/SeedOrg each wrote an audit_log row as a side effect of the
	// store write shape; the ledger is append-only and must survive the sweep.
	if got := e.WsCount(t, "SELECT count(*) FROM audit_log"); got < 1 {
		t.Errorf("audit_log count after sweep = %d, want >= 1 (ledger preserved)", got)
	}
}

// TestPreserveSetIntegrity is the fitness rail for the mass delete, and it
// carries more weight than it used to. The sweep no longer derives its targets
// from the presence of a workspace_id column — it deletes everything the
// preserve set does not name — so a stale entry there is not a table that keeps
// an extra column, it is a table that gets emptied.
//
// Both halves matter: every preserved name must still be a real base table (a
// rename or drop leaves the list pointing at nothing while the table it meant
// to protect is swept), and no preserved table may appear among the targets.
// Derived from information_schema, independent of resetTargetTables' own
// pg_catalog query, so it genuinely fails on drift rather than agreeing with
// the code under test.
func TestPreserveSetIntegrity(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT table_name FROM information_schema.tables
			WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`)
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
				t.Errorf("preserved table %q is not a current base table (renamed/dropped?) — the list protects nothing and the table it named is swept", name)
			}
		}
		targets, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		for _, tgt := range targets {
			if preservedResetTables[tgt.name] {
				t.Errorf("preserved table %q appears in the sweep target set", tgt.name)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("integrity query: %v", err)
	}
}

// TestClearWorkspaceOutboxEmptiesTheStagedEvents is the reset's outbox half: a
// reset that left staged events behind would ship events naming rows it had
// just deleted.
//
// It used to assert that a foreign workspace's rows SURVIVED, and the delete
// matched on the envelope's tenant to do it. The envelope carries no tenant now
// (ADR-0091 §6) and an installation holds one, so there is no other tenant's
// event to spare — every staged row belongs to the installation being reset.
func TestClearWorkspaceOutboxEmptiesTheStagedEvents(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	e.WsExec(t, `INSERT INTO event_outbox (stream, envelope) VALUES ('t', jsonb_build_object('type', 'x'))`)

	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return clearWorkspaceOutbox(ctx, tx)
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM event_outbox`); n != 0 {
		t.Fatalf("staged outbox rows after the reset = %d, want 0", n)
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
		pool:             e.Pool,
		schemaPool:       nil,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	if got := e.WsCount(t, "SELECT count(*) FROM app_user"); got != 4 {
		t.Errorf("app_user count after reset = %d, want 4 (identity preserved)", got)
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
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		budget:           meter,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
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
// deleteGuardedSweepTargets are the sweep targets whose DELETE-firing trigger
// does NOT protect the table as a whole, and why sweeping them is still safe.
//
// The gate below cannot tell the two shapes apart from the catalog alone. An
// append-only ledger refuses every delete, and belongs in preservedResetTables.
// A row-conditional hold refuses deletes for SOME rows and is not a protected
// store — preserving it would exempt a table the reset exists to clear. So the
// second shape is classified here, with what the reset owes it.
//
// Keyed "<table> <trigger>", on the PAIR the gate reports, because a table-keyed
// entry ratifies the category rather than the instance: it clears the trigger it
// was written for AND every DELETE trigger added to that table afterwards,
// including one that really does block. Each trigger earns its own line.
var deleteGuardedSweepTargets = gatekit.Waive(map[string]string{
	// Not a protected table: the guard refuses a DELETE only while a row carries
	// `restricted_at`, which is a statutory hold on that one record. Preserving
	// `activity` would leave every conversation behind on a reset whose whole
	// purpose is to clear them.
	//
	// Nothing can set `restricted_at` today — the trigger requires evidence in
	// activity_retention_evidence first, and no writer for that table exists yet
	// (#1557) — so the sweep cannot currently meet a held row. When that writer
	// lands, the reset must LIFT the restrictions it is entitled to clear before
	// deleting, or the sweep aborts on the first held activity. This entry is
	// that obligation, not a record of one already met.
	"activity activity_refuse_restricted_mutation": "a row-conditional statutory hold, not a protected store: preserving it would leave every activity behind on a reset meant to clear them, and no writer can set the restriction yet (#1557) — when one lands the reset must lift before it sweeps",
	// Not guards at all (migration 1787032690).
	"activity_link activity_link_last_activity": "a clock-maintenance trigger, not a guard: it recomputes the last_activity_at of the records the deleted link reached and refuses no delete; the sweep deletes those records too, so the recompute is discarded with them",
	// The same shape one column over (migration 1787320000): the project clock
	// rather than the person/organization one. Recorded on its own line and not
	// folded into the entry above, because this map is keyed on the PAIR for the
	// reason its own comment gives — a table-keyed entry would ratify the next
	// DELETE trigger on activity_link sight unseen, including one that blocks.
	"activity_link activity_link_project_last_activity": "a clock-maintenance trigger, not a guard: it recomputes project.last_activity_at for the project the deleted link filed the activity against and refuses no delete; `project` is not preserved, so the sweep deletes those rows too and the recompute is discarded with them",
	"relationship relationship_last_activity":           "a clock-maintenance trigger, not a guard: it recomputes the employer's last_activity_at and refuses no delete",
	// Also not a guard (migration 1787226902): BEFORE DELETE ON organization, it
	// sets deal.partner_org_id and deal.partner_attribution to NULL so a deleted
	// partner leaves no dangling attribution. It refuses no delete.
	//
	// It is not free, either, and "the deals go too" is not why it is safe.
	// resetTargetTables orders alphabetically and the sweep retries per pass, so
	// `deal` sorts before `offer` — whose deal_id is ON DELETE RESTRICT — and any
	// installation holding one offer defers `deal` to a later pass. `organization`
	// is then deleted in that same pass with `deal` still fully populated, and the
	// trigger runs its UPDATE once per organization row. The only index on the
	// column, idx_deal_partner, is partial on `archived_at IS NULL` while the
	// trigger's predicate carries no archived_at term, so the planner cannot use
	// it: each organization costs a sequential scan of `deal`, repeated on every
	// deferred pass.
	//
	// It is safe because the work is discarded — `deal` is swept before the reset
	// commits either way — not because it never happens. A workspace with tens of
	// thousands of both pays O(orgs x deals) inside the reset transaction, and
	// that is the cost this entry exists to put on the record.
	"organization organization_delete_clears_deal_partner": "a clear-the-reference trigger, not a guard: it nulls the partner attribution on deals that named the deleted organization and refuses no delete. Not free — the sweep can defer `deal` behind `offer`'s ON DELETE RESTRICT, so this runs against a populated `deal` with a predicate idx_deal_partner cannot serve (it is partial on archived_at IS NULL), costing a scan per organization; the rows are swept afterwards regardless, so the work is discarded rather than wrong",
})

func TestSweepTargetsCarryNoDeleteBlockingTrigger(t *testing.T) {
	defer deleteGuardedSweepTargets.AssertAllMatched(t)
	e := integration.Setup(t)
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		targets, err := resetTargetTables(ctx, tx)
		if err != nil {
			return err
		}
		targetSet := make(map[string]bool, len(targets))
		for _, target := range targets {
			targetSet[target.name] = true
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
			if targetSet[table] && !deleteGuardedSweepTargets.Waived(t, table+" "+trigger) {
				t.Errorf("sweep target %q carries DELETE-firing trigger %q — an append-only or otherwise protected table belongs in preservedResetTables; a row-conditional guard belongs in deleteGuardedSweepTargets, with what the reset owes it", table, trigger)
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
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
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

// TestResetRestoresWorkspaceLevelSettings is the general case behind the
// overlay one above: the mode columns are not the only configuration living on
// the workspace row, and the sweep reaches none of it. The sweep's target list
// is derived from the tables carrying a workspace_id column; workspace keys on
// id, so it is not excluded from that list, it is not a candidate for it.
//
// x_sor_mode stands in for every configuration column the row carries: an
// operator who wiped the installation would otherwise find yesterday's
// settings still applied to a database with nothing in it. The settings that
// live in the `setting` table are the other half of the same obligation, and
// the sibling test below covers them.
func TestResetRestoresWorkspaceLevelSettings(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()
	e.WsExec(t, `
		UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot' WHERE id = $1`, e.WS)

	h := dataResetHandlers{
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(ctx, "Authz"); err != nil {
		t.Fatalf("run: %v", err)
	}

	var mode string
	if err := e.Pool.QueryRow(ctx,
		`SELECT x_sor_mode FROM workspace WHERE id = $1`, e.WS).Scan(&mode); err != nil {
		t.Fatalf("reading the setting back: %v", err)
	}
	if mode != "native" {
		t.Errorf("x_sor_mode = %q after a reset, want native (its declared default) — a workspace-level setting outlived the wipe", mode)
	}
}

// The same obligation for the settings that moved off the workspace row into
// `setting` (ADR-0090/A135). That table carries no workspace_id, so the reset's
// table sweep — derived from the tables that do — never had it as a candidate
// either: without an explicit restore every setting outlives the wipe exactly
// as the workspace row's own settings did before ResetWorkspaceConfig.
//
// The split is what this proves. Configuration goes back to its registered
// default; the installation's IDENTITY does not, because a reset wipes an
// installation's data without re-creating the installation.
func TestResetRestoresSettingRowsButKeepsTheInstallationsIdentity(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	// Configuration a human changed, and identity bootstrap wrote.
	e.WsExec(t, `
		INSERT INTO setting (key, value) VALUES ('capture.auto_enrich', 'false'::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	e.WsExec(t, `
		INSERT INTO setting (key, value) VALUES ('installation.name', '"Brandt Automotive"'::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)

	h := dataResetHandlers{
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// Confirmed with the SETTING's name, not the workspace column's. This test
	// is the one place the two deliberately differ, and the confirmation
	// prompt asks for the name the operator can actually see — which is the
	// one the installation settings screen shows (issue #521).
	if _, err := h.run(ctx, "Brandt Automotive"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Configuration is gone, so a read resolves to the registered default —
	// an absent row and a row holding the default read identically, and the
	// absence keeps "has anyone changed this?" answerable.
	var configRows int
	if err := e.Pool.QueryRow(ctx,
		`SELECT count(*) FROM setting WHERE key = 'capture.auto_enrich'`).Scan(&configRows); err != nil {
		t.Fatalf("reading the configuration setting back: %v", err)
	}
	if configRows != 0 {
		t.Error("capture.auto_enrich survived the reset — a configuration setting must return to its default")
	}

	// Identity survives: the installation is the same installation afterwards.
	var name string
	if err := e.Pool.QueryRow(ctx,
		`SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'`).Scan(&name); err != nil {
		t.Fatalf("the installation lost its name in a reset that preserves the installation: %v", err)
	}
	if name != "Brandt Automotive" {
		t.Errorf("installation.name = %q after a reset, want it preserved", name)
	}
}

// TestResetLeavesANativeWorkspaceAlone: the flip is conditional, so a native
// installation's reset claims no mode change in its evidence.
func TestResetLeavesANativeWorkspaceAlone(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	h := dataResetHandlers{
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(ctx, "Authz"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE action = 'reset_data' AND evidence->>'sor_mode_reverted' = 'true'`); got != 0 {
		t.Errorf("evidence claims a mode revert on an install that was already native (%d rows)", got)
	}
}

// TestResetPurgesTheSealedCredentialsItsSweepOrphans: vault_secret carries no
// workspace_id — the tenant lives inside the ref and inside the AES-256-GCM
// AAD, deliberately, so it is operational infrastructure rather than a tenant
// table and the sweep never sees it.
//
// That means the sweep deletes the connection rows holding the refs and leaves
// the sealed ciphertext behind forever, unreachable but resident: credential
// material outliving the wipe that was supposed to produce a clean slate. The
// refs must therefore be collected BEFORE the rows go, and redeemed after.
//
// The assertion is that the secret is gone from the VAULT, not that a counter
// moved: an implementation that tallied refs without redeeming them would look
// identical from the evidence alone.
func TestResetPurgesTheSealedCredentialsItsSweepOrphans(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.Admin()

	vault := resetTestVault(t, e)
	wsID := ids.From[ids.WorkspaceKind](e.WS)
	mine, err := vault.Put(ctx, wsID, []byte("an incumbent's oauth refresh token"))
	if err != nil {
		t.Fatalf("sealing the credential under test: %v", err)
	}
	e.WsExec(t, `INSERT INTO incumbent_connection (id, incumbent, region, status, credential_ref)
		VALUES ($1, 'hubspot', 'eu', 'active', $2)`, ids.NewV7(), string(mine))

	h := dataResetHandlers{
		pool:             e.Pool,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		vault:            vault,
	}
	if _, err := h.run(ctx, "Authz"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := vault.Get(ctx, wsID, mine); !errors.Is(err, keyvault.ErrNotFound) {
		t.Errorf("the sealed credential outlived the reset (Get returned %v) — a wipe that leaves credential material resident is not a clean slate", err)
	}
	if got := e.WsCount(t, `SELECT count(*) FROM audit_log
		WHERE action = 'reset_data' AND evidence->>'secrets_purged' = '1'`); got != 1 {
		t.Errorf("reset_data rows recording one purged secret = %d, want 1", got)
	}
}

// resetTestVault builds the local vault provider over the harness pool. The
// root key is fixed and meaningless — these tests assert reachability of the
// ciphertext, never its contents.
func resetTestVault(t *testing.T, e *integration.Env) keyvault.Vault {
	t.Helper()
	vault, err := keyvault.New(keyvault.Config{
		RootKey: bytes.Repeat([]byte{7}, 32),
		Pool:    e.Pool,
	})
	if err != nil {
		t.Fatalf("building the test vault: %v", err)
	}
	return vault
}
