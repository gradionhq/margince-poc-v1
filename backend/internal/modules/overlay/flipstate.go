// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns the overlay→native flip's preflight primitives
// (B-E18.26, ADR-0071/OVA-AC-6): the readiness checks, the mirror
// freeze/seal (flip_snapshot_id + mirror_frozen_at on
// overlay_sync_state), and CompleteFlip — the one place the flip mutates
// workspace.x_sor_mode. The cutover ORCHESTRATION (parity dry-run via the
// migration engine, the export check, the wire shapes) lives in compose's
// FlipRunner; these primitives keep every mirror/sync/mode semantic
// inside this module.
//
// Freeze semantics: while mirror_frozen_at is set, every FENCED mirror
// write (sweep ingest, webhook re-fetch, write-back's pending mark)
// refuses with ErrMirrorFrozen — the frozen snapshot the flip imports
// cannot drift under it. Reads stay open (the workspace keeps working off
// its mirror, UC-E18-04 F1). The preflight seals only when every check is
// green and unseals on any blocker, so a failed preflight is a no-op
// return to a healthy overlay; after a completed flip the seal is left in
// place on purpose — a post-flip mirror is dead weight awaiting
// retirement, and a late in-flight write-back must not reach the
// incumbent after the cutover.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ErrMirrorFrozen is a fenced mirror write refused because the flip
// preflight sealed the snapshot (or the flip already completed). Sweep
// workers treat it as a clean stop, like ErrConnectionGone — the freeze
// is deliberate state, not a failure to back off from.
var ErrMirrorFrozen = errors.New("overlay: the mirror is frozen for the overlay→native flip")

// FlipConflict is one open incumbent-wins conflict blocking the flip.
// Branch 1 write-back auto-resolves incumbent-wins at ingest (reconcile
// emits mirror.conflict as a notification), so no persistent conflict
// row exists to report yet — the shape is here so the preflight wire
// contract is stable when branch 2's conflict queue lands.
type FlipConflict struct {
	ObjectClass string
	ExternalID  string
	Property    string
}

// FlipChecks is the preflight's raw readiness read — the compose
// FlipRunner turns it into the wire verdict's blocking[] reasons.
type FlipChecks struct {
	// Incumbent names the workspace's connected incumbent — the import
	// provenance stamp and the emergency disclosure both carry it.
	Incumbent string
	// ConnectionStatus is incumbent_connection.status: revoked/error is
	// the OVA-AC-6(a) incumbent-unreachable block.
	ConnectionStatus string
	// ForceFreshDone: a sweep has succeeded, no mirror row is stale, and
	// every mirrored class's backfill has genuinely converged.
	ForceFreshDone bool
	// PendingSyncCount: rows with un-drained local writes — the flip
	// waits until they drain (AC-mode-flip-4).
	PendingSyncCount int
	// Conflicts: open incumbent-wins conflicts (empty in branch 1 — see
	// FlipConflict).
	Conflicts []FlipConflict
	// LastSyncedAt is the mirror's freshest watermark (zero on an empty
	// mirror) — the emergency cutover's staleness disclosure and the
	// export-recency check both read it.
	LastSyncedAt time.Time
	// MirrorRows counts the whole mirror estate — the parity preview's
	// denominator, and zero distinguishes "nothing mirrored" honestly.
	MirrorRows int
}

// FlipSnapshot is the sealed frozen-mirror snapshot the flip imports.
type FlipSnapshot struct {
	ID       string
	FrozenAt time.Time
	Sealed   bool
}

// FlipChecks reads every preflight input in one workspace transaction.
// Gated ActionRead + overlay mode: it mutates nothing.
func (s *Service) FlipChecks(ctx context.Context) (FlipChecks, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionRead); err != nil {
		return FlipChecks{}, err
	}
	incumbent, err := s.resolveOverlayMode(ctx)
	if err != nil {
		return FlipChecks{}, err
	}
	checks := FlipChecks{Incumbent: incumbent}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status FROM incumbent_connection`).Scan(&checks.ConnectionStatus); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Overlay mode with no connection row cannot arise through
				// Connect/Disconnect (both flip mode and row together) — a
				// hand-seeded fixture gap, surfaced, not guessed around.
				return errors.New("overlay: workspace is in overlay mode but has no incumbent_connection row")
			}
			return fmt.Errorf("overlay: reading the connection status for the flip preflight: %w", err)
		}

		var pending, stale int
		var lastSynced *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE sync_state = $1),
			       count(*) FILTER (WHERE sync_state = $2),
			       count(*), max(last_synced_at)
			FROM overlay_mirror`, syncStatePendingSync, syncStateStale,
		).Scan(&pending, &stale, &checks.MirrorRows, &lastSynced); err != nil {
			return fmt.Errorf("overlay: aggregating mirror state for the flip preflight: %w", err)
		}
		checks.PendingSyncCount = pending
		if lastSynced != nil {
			checks.LastSyncedAt = *lastSynced
		}

		var sweepSucceeded bool
		err := tx.QueryRow(ctx, `SELECT last_success_at IS NOT NULL FROM overlay_sync_state`).Scan(&sweepSucceeded)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("overlay: reading sweep state for the flip preflight: %w", err)
		}

		backfilled, err := s.allMirroredClassesBackfilled(ctx, tx)
		if err != nil {
			return err
		}
		checks.ForceFreshDone = sweepSucceeded && stale == 0 && backfilled
		return nil
	})
	if err != nil {
		return FlipChecks{}, err
	}
	return checks, nil
}

// allMirroredClassesBackfilled answers whether every object class the
// mirror holds has a genuinely converged backfill (backfillCompleteFor's
// done-and-not-truncated, per incumbent class). An empty mirror answers
// true — convergence on an empty incumbent object set is a valid
// force-fresh state; the sweep-success check beside it keeps "never
// synced at all" from slipping through as ready.
func (s *Service) allMirroredClassesBackfilled(ctx context.Context, tx pgx.Tx) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT object_class FROM overlay_mirror ORDER BY object_class`)
	if err != nil {
		return false, fmt.Errorf("overlay: listing mirrored classes for the flip preflight: %w", err)
	}
	classes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("overlay: collecting mirrored classes for the flip preflight: %w", err)
	}
	for _, class := range classes {
		complete, err := s.backfillCompleteFor(ctx, tx, class)
		if err != nil {
			return false, err
		}
		if !complete {
			return false, nil
		}
	}
	return true, nil
}

// SealFlipSnapshot freezes the mirror and seals the snapshot the flip
// will import, idempotently: an already-sealed workspace keeps its seal
// (the id names ONE frozen state; re-preflighting must not silently
// re-freeze a different one). Gated ActionUpdate — freezing pauses every
// fenced mirror write, the same admin/ops posture as RequestSweep.
func (s *Service) SealFlipSnapshot(ctx context.Context) (FlipSnapshot, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return FlipSnapshot{}, err
	}
	if err := s.requireOverlayMode(ctx); err != nil {
		return FlipSnapshot{}, err
	}
	var snap FlipSnapshot
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO overlay_sync_state (workspace_id, next_sweep_at, flip_snapshot_id, mirror_frozen_at, updated_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, now(),
			        'snap-' || to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), now(), now())
			ON CONFLICT (workspace_id) DO UPDATE SET
			  flip_snapshot_id = COALESCE(overlay_sync_state.flip_snapshot_id, EXCLUDED.flip_snapshot_id),
			  mirror_frozen_at = COALESCE(overlay_sync_state.mirror_frozen_at, EXCLUDED.mirror_frozen_at),
			  updated_at = now()
			RETURNING flip_snapshot_id, mirror_frozen_at`,
		).Scan(&snap.ID, &snap.FrozenAt)
	})
	if err != nil {
		return FlipSnapshot{}, fmt.Errorf("overlay: sealing the flip snapshot: %w", err)
	}
	snap.Sealed = true
	return snap, nil
}

// UnsealFlipSnapshot is the UC-E18-04 F1 unfreeze: any preflight blocker
// returns the workspace to a healthy overlay — mirror writable, sweeps
// resume, nothing partially migrated. A workspace with no seal is a
// no-op.
func (s *Service) UnsealFlipSnapshot(ctx context.Context) error {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return err
	}
	if err := s.requireOverlayMode(ctx); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE overlay_sync_state SET flip_snapshot_id = NULL, mirror_frozen_at = NULL, updated_at = now()`); err != nil {
			return fmt.Errorf("overlay: unsealing the flip snapshot: %w", err)
		}
		return nil
	})
}

// FlipSnapshot reads the current seal, if any.
func (s *Service) FlipSnapshot(ctx context.Context) (FlipSnapshot, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionRead); err != nil {
		return FlipSnapshot{}, err
	}
	if err := s.requireOverlayMode(ctx); err != nil {
		return FlipSnapshot{}, err
	}
	var snap FlipSnapshot
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id *string
		var frozenAt *time.Time
		err := tx.QueryRow(ctx, `SELECT flip_snapshot_id, mirror_frozen_at FROM overlay_sync_state`).Scan(&id, &frozenAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("overlay: reading the flip snapshot seal: %w", err)
		}
		if id != nil && frozenAt != nil {
			snap = FlipSnapshot{ID: *id, FrozenAt: *frozenAt, Sealed: true}
		}
		return nil
	})
	if err != nil {
		return FlipSnapshot{}, err
	}
	return snap, nil
}

// CompleteFlip is the cutover's mode change (B-E18.27's last step): ONE
// transaction flips workspace.x_sor_mode to native and clears
// x_incumbent — the x_overlay_iff_incumbent CHECK demands both move
// together, so the incumbent_connection row deliberately SURVIVES, still
// active, no longer authoritative (UC-E18-05 precondition: retirement
// revokes it later, and disconnect-after-flip still tears the mirror
// down). Audit-only, no event: the catalog pins no flip event
// (IEM-GAP-3 declines run-lifecycle events; the EVT-NOEVT precedent),
// and the audit row carries the run id + the structural T2→T1 re-tag —
// mirror-derived reads cease, the imported native rows ARE first-party.
// A workspace no longer in overlay mode answers ErrConflict: the flip is
// one-way and exactly-once.
func (s *Service) CompleteFlip(ctx context.Context, runID ids.UUID, mode string) error {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("overlay: flip completion called outside a workspace context")
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE workspace SET x_sor_mode = 'native', x_incumbent = NULL
			WHERE id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
			  AND x_sor_mode = 'overlay'`)
		if err != nil {
			return fmt.Errorf("overlay: flipping the workspace to native mode: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("overlay: the workspace is not in overlay mode, nothing to flip: %w", apperrors.ErrConflict)
		}
		_, err = storekit.Audit(ctx, tx, "update", "workspace", ws,
			map[string]any{"x_sor_mode": "overlay"},
			map[string]any{"x_sor_mode": "native", "x_incumbent": nil, "import_run_id": runID.String(), "flip_mode": mode, "derivative_tier": "T1 (incumbent-derived reads re-tagged first-party by the cutover)"})
		return err
	})
	if err != nil {
		return err
	}
	s.notifyModeFlip(ws)
	return nil
}
