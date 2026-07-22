// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns GET /overlay/sync-status and GET /overlay/budget's
// domain logic (design.md §4.7): per-object mirror freshness and the OVB
// meter's read surface. Both are mode-gated — SyncStatus/Budget have no
// meaning for a workspace that never connected an incumbent, so calling
// either in SoR-mode answers apperrors.ErrModeNotOverlay (404
// mode_not_overlay), never a silently-empty payload.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ObjectSyncStatus is one object class's mirror freshness — the domain
// shape GetOverlaySyncStatus (handlers.go) maps onto the wire
// crmcontracts.OverlaySyncStatus.Objects entry. Object carries the
// CANONICAL entity type (overlay_mirror's own object_class column, e.g.
// "person"), not the incumbent's class name — the same vocabulary
// datasource.EntityType and every other overlay read verb already uses.
type ObjectSyncStatus struct {
	Object           string
	LastSyncedAt     time.Time
	State            string
	BackfillComplete bool
}

// SyncStatus states (overlay_mirror.sync_state's CHECK vocabulary,
// mirrored here for the aggregate this file computes over many rows).
const (
	syncStateFresh = "fresh"
	syncStateStale = "stale"
)

// requireOverlayMode gates SyncStatus/Budget/Reconcile: each answers
// apperrors.ErrModeNotOverlay (404 mode_not_overlay) for a workspace that
// never connected an incumbent — these ops have no SoR-mode equivalent
// (crm.yaml's own doc comment on the /overlay cluster). It reads
// workspace.x_sor_mode directly rather than incumbent_connection's own
// status, the same source of truth compose.Dispatcher's isOverlay uses,
// so a workspace mid-teardown (connection revoked, mode not yet flipped
// back — Disconnect flips both in the SAME transaction, so this window
// does not exist in practice, but the check names the true source
// rather than inferring it from a second table).
func (s *Service) requireOverlayMode(ctx context.Context) error {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("overlay: sync-status/budget/reconcile called outside a workspace context")
	}
	var mode string
	err := database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT x_sor_mode FROM workspace WHERE id = $1`, wsID).Scan(&mode)
	})
	if err != nil {
		return fmt.Errorf("overlay: resolving workspace sor_mode: %w", err)
	}
	if mode != "overlay" {
		return apperrors.ErrModeNotOverlay
	}
	return nil
}

// selectMirrorSyncAggregateSQL rolls up overlay_mirror by object_class:
// the freshest/staleness-worst sync_state observed for that class (any
// pending_sync row outranks any stale row, which outranks plain fresh —
// the aggregate reports the WORST state present, never hides a dirty or
// stale row behind a majority-fresh count) and the most recent
// last_synced_at across the class's rows (the class's own freshest
// watermark, the number a UI's "last synced" affordance shows).
const selectMirrorSyncAggregateSQL = `
SELECT object_class, max(last_synced_at),
       bool_or(sync_state = $1) AS any_pending,
       bool_or(sync_state = $2) AS any_stale
FROM overlay_mirror
GROUP BY object_class
ORDER BY object_class`

// SyncStatus answers ctx's workspace per-object mirror freshness (design
// §4.7). Gated by auth.Require("overlay_connection", ActionRead) — the
// same "every role reads" posture as Get (identity/internal/policy) —
// and requireOverlayMode. An object class with zero overlay_mirror rows
// is omitted entirely: there is nothing to report a sync state for
// (backfill may still be running, or may have converged on an empty
// incumbent object set — either way, an honest omission rather than a
// fabricated "fresh, zero records" row).
func (s *Service) SyncStatus(ctx context.Context) ([]ObjectSyncStatus, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionRead); err != nil {
		return nil, err
	}
	if err := s.requireOverlayMode(ctx); err != nil {
		return nil, err
	}

	var out []ObjectSyncStatus
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Drain the aggregate query FULLY before running any further query
		// on this same tx — pgx ties the connection up while a Rows result
		// is still open, so calling backfillCompleteFor's own tx.QueryRow
		// per row WHILE this Query's rows are still being iterated would
		// contend with it on the same connection (observed: it silently
		// answered "no row", not a panic — exactly the kind of quiet
		// wrong-answer a second, nested pass on the drained slice avoids).
		rows, err := tx.Query(ctx, selectMirrorSyncAggregateSQL, syncStatePendingSync, syncStateStale)
		if err != nil {
			return fmt.Errorf("overlay: aggregating mirror sync state: %w", err)
		}
		for rows.Next() {
			var objectClass string
			var lastSyncedAt time.Time
			var flags mirrorStateFlags
			if err := rows.Scan(&objectClass, &lastSyncedAt, &flags.anyPending, &flags.anyStale); err != nil {
				rows.Close()
				return fmt.Errorf("overlay: scanning a mirror sync aggregate row: %w", err)
			}
			out = append(out, ObjectSyncStatus{
				Object:       objectClass,
				LastSyncedAt: lastSyncedAt,
				State:        aggregateState(flags),
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()

		for i := range out {
			complete, err := s.backfillCompleteFor(ctx, tx, out[i].Object)
			if err != nil {
				return err
			}
			out[i].BackfillComplete = complete
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// mirrorStateFlags carries the per-class aggregate booleans the mirror
// sync query yields, named so aggregateState reads them by field rather
// than by positional bool (the argument order otherwise being a silent
// trap: swapping the two would invert pending vs stale precedence).
type mirrorStateFlags struct {
	anyPending bool
	anyStale   bool
}

// aggregateState answers the worst sync state present across a class's
// mirror rows: an un-drained local write outranks staleness, which
// outranks plain freshness.
func aggregateState(f mirrorStateFlags) string {
	switch {
	case f.anyPending:
		return syncStatePendingSync
	case f.anyStale:
		return syncStateStale
	default:
		return syncStateFresh
	}
}

// backfillCompleteFor answers whether canonicalObjectClass's backfill has
// converged (overlay_backfill_cursor.done), translating through
// s.toIncumbentClasses first (overlay_backfill_cursor is keyed by the
// INCUMBENT class name — see the Service doc). No translator wired or no
// declared mapping for this class both answer (false, nil) — an honest
// "not confirmed complete", never a guessed true.
//
// The translation is plural: a canonical type can be backed by several
// incumbent classes ("activity" ← the five v3 engagement classes), and its
// backfill is complete only when EVERY one of them has converged — so a
// single lagging engagement class correctly reports activity as still
// backfilling. A missing cursor row (a class whose backfill never ran) is
// an honest false, but any other query error is propagated rather than
// folded into it, so a real DB failure fails the SyncStatus response
// instead of silently reporting incomplete backfill as fact.
func (s *Service) backfillCompleteFor(ctx context.Context, tx pgx.Tx, canonicalObjectClass string) (bool, error) {
	if s.toIncumbentClasses == nil {
		return false, nil
	}
	incumbentClasses, ok := s.toIncumbentClasses(canonicalObjectClass)
	if !ok || len(incumbentClasses) == 0 {
		// An empty class set (however it arose) is "no class to confirm", so
		// completeness is unconfirmed — never a vacuously-true report from a
		// loop that ran zero iterations.
		return false, nil
	}
	for _, incumbentClass := range incumbentClasses {
		var done bool
		err := tx.QueryRow(ctx,
			`SELECT done FROM overlay_backfill_cursor WHERE object_class = $1`, incumbentClass,
		).Scan(&done)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("overlay: checking backfill completeness for %s (%s): %w", canonicalObjectClass, incumbentClass, err)
		}
		if !done {
			return false, nil
		}
	}
	return true, nil
}

// Budget answers ctx's workspace current OVB consumption window (design
// §4.7). Gated the same way SyncStatus is: every role reads, but only
// inside overlay mode. meter==nil (a Service built with no
// WithBudgetMeter call) answers apperrors.ErrModeNotOverlay-shaped
// honesty is wrong here — this is a WIRING gap, not a mode question — so
// it surfaces its own explicit error instead of silently degrading to a
// fabricated all-zero Budget.
func (s *Service) Budget(ctx context.Context) (Budget, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionRead); err != nil {
		return Budget{}, err
	}
	if err := s.requireOverlayMode(ctx); err != nil {
		return Budget{}, err
	}
	if s.meter == nil {
		return Budget{}, fmt.Errorf("overlay: budget meter is not wired for this role")
	}
	return s.meter.Snapshot(ctx), nil
}
