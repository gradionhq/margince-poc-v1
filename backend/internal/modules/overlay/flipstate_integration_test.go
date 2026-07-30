// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedOverlayWorkspace puts the testWorkspaceCtx workspace into overlay
// mode with an active hubspot connection — the state every flip
// primitive requires. It seeds directly (owner connection) rather than
// through Connect: these tests exercise the flip state machine, not the
// OAuth/vault path.
func seedOverlayWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE workspace SET x_sor_mode = 'overlay', x_incumbent = 'hubspot'
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO incumbent_connection (workspace_id, incumbent, region, credential_ref, status)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'hubspot', 'eu1', $1, 'active')`,
			string(keyvault.Ref("test-ref-"+ids.NewV7().String())))
		return err
	})
	if err != nil {
		t.Fatalf("seeding overlay workspace: %v", err)
	}
}

func setConnectionStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, status string) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE incumbent_connection SET status = $1`, status)
		return err
	})
	if err != nil {
		t.Fatalf("setting connection status: %v", err)
	}
}

func seedMirrorRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, class, ext, syncState string) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_mirror (workspace_id, object_class, external_id, fields, updated_at_baseline, sync_state)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, '{"full_name":"Fixture Row"}'::jsonb, now(), $3)`,
			class, ext, syncState)
		return err
	})
	if err != nil {
		t.Fatalf("seeding mirror row: %v", err)
	}
}

func recordSweepSuccess(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_sync_state (workspace_id, next_sweep_at, consecutive_failures, last_success_at, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, now(), 0, now(), now())
			ON CONFLICT (workspace_id) DO UPDATE SET last_success_at = now(), updated_at = now()`)
		return err
	})
	if err != nil {
		t.Fatalf("recording sweep success: %v", err)
	}
}

func markBackfillDone(t *testing.T, ctx context.Context, pool *pgxpool.Pool, incumbentClass string) {
	t.Helper()
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_backfill_cursor (workspace_id, object_class, cursor, done, truncated)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, '', true, false)
			ON CONFLICT (workspace_id, object_class) DO UPDATE SET done = true, truncated = false`,
			incumbentClass)
		return err
	})
	if err != nil {
		t.Fatalf("marking backfill done: %v", err)
	}
}

// flipService builds the Service under test with the hubspot
// canonical→incumbent translation the backfill-completeness check needs.
func flipService(pool *pgxpool.Pool) *Service {
	svc := NewService(pool, nil, NewMirrorStore(pool, nil))
	return svc.WithIncumbentClassesTranslator(func(canonical string) ([]string, bool) {
		switch canonical {
		case "person":
			return []string{IncumbentClassContacts}, true
		case "organization":
			return []string{IncumbentClassCompanies}, true
		default:
			return nil, false
		}
	})
}

func TestFlipChecksReportUnreachableStaleAndPending(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedOverlayWorkspace(t, ctx, pool)
	svc := flipService(pool)

	// Fresh overlay, one converged person row.
	seedMirrorRow(t, ctx, pool, "person", "p-1", "fresh")
	recordSweepSuccess(t, ctx, pool)
	markBackfillDone(t, ctx, pool, IncumbentClassContacts)

	checks, err := svc.FlipChecks(ctx)
	if err != nil {
		t.Fatalf("FlipChecks: %v", err)
	}
	if checks.ConnectionStatus != "active" || !checks.ForceFreshDone || checks.PendingSyncCount != 0 {
		t.Fatalf("green checks = %+v, want active + force-fresh + drained", checks)
	}
	if checks.MirrorRows != 1 || checks.LastSyncedAt.IsZero() {
		t.Fatalf("green checks = %+v, want the mirror aggregate populated", checks)
	}

	// A stale row breaks force-fresh; a pending_sync row is counted.
	seedMirrorRow(t, ctx, pool, "person", "p-stale", "stale")
	seedMirrorRow(t, ctx, pool, "person", "p-dirty", "pending_sync")
	checks, err = svc.FlipChecks(ctx)
	if err != nil {
		t.Fatalf("FlipChecks: %v", err)
	}
	if checks.ForceFreshDone || checks.PendingSyncCount != 1 {
		t.Fatalf("dirty checks = %+v, want force-fresh false + 1 pending", checks)
	}

	// A revoked connection reports as such (OVA-AC-6 a's trigger state).
	setConnectionStatus(t, ctx, pool, "revoked")
	checks, err = svc.FlipChecks(ctx)
	if err != nil {
		t.Fatalf("FlipChecks: %v", err)
	}
	if checks.ConnectionStatus != "revoked" {
		t.Fatalf("checks.ConnectionStatus = %q, want revoked", checks.ConnectionStatus)
	}
}

func TestFlipChecksRequireBackfillConvergence(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedOverlayWorkspace(t, ctx, pool)
	svc := flipService(pool)

	seedMirrorRow(t, ctx, pool, "person", "p-1", "fresh")
	recordSweepSuccess(t, ctx, pool)
	// No backfill cursor at all → not converged, not force-fresh.
	checks, err := svc.FlipChecks(ctx)
	if err != nil {
		t.Fatalf("FlipChecks: %v", err)
	}
	if checks.ForceFreshDone {
		t.Fatal("force-fresh must be false while the class's backfill never converged")
	}
}

func TestSealUnsealLifecycleAndFreezeFence(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedOverlayWorkspace(t, ctx, pool)
	svc := flipService(pool)
	ms := NewMirrorStore(pool, nil)

	snap, err := svc.SealFlipSnapshot(ctx)
	if err != nil {
		t.Fatalf("SealFlipSnapshot: %v", err)
	}
	if !snap.Sealed || snap.ID == "" || snap.FrozenAt.IsZero() {
		t.Fatalf("seal = %+v, want a sealed snapshot with id + instant", snap)
	}

	// Sealing again keeps the SAME snapshot (idempotent, never a silent
	// re-freeze of a different mirror state).
	again, err := svc.SealFlipSnapshot(ctx)
	if err != nil {
		t.Fatalf("second SealFlipSnapshot: %v", err)
	}
	if again.ID != snap.ID || !again.FrozenAt.Equal(snap.FrozenAt) {
		t.Fatalf("re-seal = %+v, want the original %+v", again, snap)
	}

	// A fenced ingest refuses while frozen — the snapshot cannot drift.
	err = ms.WithFence().Ingest(ctx, Record{
		ExternalID: "p-frozen", ObjectClass: "person",
		Fields: map[string]any{"full_name": "Late Arrival"}, ModifiedAt: time.Now(),
	})
	if !errors.Is(err, ErrMirrorFrozen) {
		t.Fatalf("fenced ingest under freeze err = %v, want ErrMirrorFrozen", err)
	}

	// The read seam reports the seal.
	got, err := svc.FlipSnapshot(ctx)
	if err != nil {
		t.Fatalf("FlipSnapshot: %v", err)
	}
	if !got.Sealed || got.ID != snap.ID {
		t.Fatalf("FlipSnapshot = %+v, want the sealed %+v", got, snap)
	}

	// Unseal (the F1 unfreeze): fenced writes work again.
	if err := svc.UnsealFlipSnapshot(ctx); err != nil {
		t.Fatalf("UnsealFlipSnapshot: %v", err)
	}
	err = ms.WithFence().Ingest(ctx, Record{
		ExternalID: "p-thawed", ObjectClass: "person",
		Fields: map[string]any{"full_name": "After Thaw"}, ModifiedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("fenced ingest after unseal: %v", err)
	}
}

func TestCompleteFlipFlipsModeOnceAndKeepsConnection(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	seedOverlayWorkspace(t, ctx, pool)
	svc := flipService(pool)
	var flipped []ids.UUID
	svc = svc.WithModeFlipObserver(func(id ids.UUID) { flipped = append(flipped, id) })

	runID := ids.NewV7()
	if err := svc.CompleteFlip(ctx, runID, "fresh_sync"); err != nil {
		t.Fatalf("CompleteFlip: %v", err)
	}
	if len(flipped) != 1 || flipped[0] != ws {
		t.Fatalf("mode-flip observer calls = %v, want the workspace once", flipped)
	}

	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		var mode string
		var incumbent *string
		var connStatus string
		if err := tx.QueryRow(ctx, `
			SELECT x_sor_mode, x_incumbent FROM workspace
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
		).Scan(&mode, &incumbent); err != nil {
			return err
		}
		if mode != "native" || incumbent != nil {
			t.Errorf("workspace after flip = %s/%v, want native with x_incumbent cleared (DS-AC-5)", mode, incumbent)
		}
		if err := tx.QueryRow(ctx, `SELECT status FROM incumbent_connection`).Scan(&connStatus); err != nil {
			return err
		}
		if connStatus != "active" {
			t.Errorf("connection after flip = %s, want active (still present, no longer authoritative — retirement revokes it later)", connStatus)
		}
		var audits int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'workspace' AND action = 'update'`,
		).Scan(&audits); err != nil {
			return err
		}
		if audits != 1 {
			t.Errorf("workspace flip audit rows = %d, want exactly 1", audits)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("asserting post-flip state: %v", err)
	}

	// The flip is one-way and exactly-once.
	if err := svc.CompleteFlip(ctx, runID, "fresh_sync"); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("second CompleteFlip err = %v, want ErrConflict", err)
	}
}

func TestDisconnectRefusesARunningImportButNeverALatchedFreeze(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	seedOverlayWorkspace(t, ctx, pool)
	svc := flipService(pool)

	// A RUNNING flip import is the one thing disconnect refuses: tearing
	// the mirror down mid-import would migrate a vanishing estate.
	importRunning := true
	svc = svc.WithFlipImportProbe(func(context.Context, pgx.Tx) (bool, error) { return importRunning, nil })
	if _, err := svc.SealFlipSnapshot(ctx); err != nil {
		t.Fatalf("SealFlipSnapshot: %v", err)
	}
	if err := svc.Disconnect(ctx); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("Disconnect during a running import err = %v, want ErrConflict", err)
	}

	// A sealed-but-IDLE workspace must still disconnect. Disconnect is
	// the only path that revokes the incumbent credential and purges the
	// mirrored PII, so a freeze can never latch it shut — an operator who
	// preflights and then thinks better of the cutover is not trapped.
	importRunning = false
	if err := svc.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect on a sealed-but-idle workspace: %v — the freeze must never latch the escape hatch", err)
	}
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		var connStatus, mode string
		if err := tx.QueryRow(ctx, `SELECT status FROM incumbent_connection`).Scan(&connStatus); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT x_sor_mode FROM workspace
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
		).Scan(&mode); err != nil {
			return err
		}
		if connStatus != "revoked" || mode != "native" {
			t.Errorf("after retirement: connection=%s mode=%s, want revoked + native", connStatus, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("asserting retirement state: %v", err)
	}
}
