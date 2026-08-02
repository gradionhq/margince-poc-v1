// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// The overlay sweep's per-object-class phases: backfill, then modified, then
// deletion. Split out of jobs_overlay.go, which owns the reconcile job's
// dispatcher/worker pair and the connection-level orchestration these phases
// run under.

// sweepDeps bundles sweepObjectClass's per-connection collaborators — the
// live incumbent adapter, the identity-fenced store, the OVB meter, and the
// logger — so the phase functions below (and reconcileConnection's call
// site) pass one value instead of four positional ones.
type sweepDeps struct {
	inc   overlay.Incumbent
	ms    *overlay.MirrorStore
	meter *overlaybudget.Meter
	log   *slog.Logger
}

// sweepMustStop reports whether err is either the disconnect-race fence's
// clean-stop signal (overlay.ErrConnectionGone) or a connection-level
// incumbent failure (isConnectionLevelIncumbentError) — the two conditions
// every phase of sweepObjectClass propagates to abort the whole sweep,
// rather than logging and skipping just this object class. One predicate so
// every phase checks the identical condition, rather than each phase
// spelling out its own copy that could silently drift from its siblings.
func sweepMustStop(err error) bool {
	return errors.Is(err, overlay.ErrConnectionGone) || errors.Is(err, overlay.ErrMirrorFrozen) ||
		isConnectionLevelIncumbentError(err)
}

// sweepObjectClass runs one object class's full convergence for a
// connection: the initial backfill (a cheap no-op once its cursor has
// converged), the incremental modified-record sweep, then the
// opposite-direction deletion sweep — each on its own persisted watermark,
// each its own phase function below. Any phase's failure is logged and
// skips the REST of this class's sweep this tick (the next tick resumes from
// the checkpoint), never aborting the other classes on its own — that
// distinction is sweepMustStop's, below. workspace is the stringified id,
// for logging only; connectedAt floors the incremental sweep of a class
// that has no watermark yet.
//
// It returns a non-nil error only when sweepMustStop says so — a
// connection-level incumbent failure or overlay.ErrConnectionGone — the
// signal reconcileConnection propagates to abort the sweep and back the
// connection off (or, for ErrConnectionGone, turns into a no-backoff clean
// stop). A per-object failure (a mapping/data defect, a DB read/write blip)
// is logged and skips the rest of THIS class with a nil return, so the
// connection-level loop moves on to the next class.
func sweepObjectClass(ctx context.Context, deps sweepDeps, workspace, objectClass string, connectedAt time.Time) error {
	proceed, err := sweepBackfillPhase(ctx, deps, workspace, objectClass, connectedAt)
	if err != nil {
		return err
	}
	if !proceed {
		return nil // the phase already logged and skipped (backfill pass failed)
	}
	proceed, err = sweepModifiedPhase(ctx, deps, workspace, objectClass, connectedAt)
	if err != nil {
		return err
	}
	if !proceed {
		return nil // the phase already logged and skipped (watermark read failed)
	}
	return sweepDeletionPhase(ctx, deps, workspace, objectClass)
}

// sweepBackfillPhase runs the initial full load before the incremental
// sweep: Backfill lists the object class id-cursor style AND fetches its
// associations (design.md §4.4), checkpointing overlay_backfill_cursor so
// SyncStatus's backfillComplete answers truthfully. It is a cheap no-op
// once its cursor has converged, so every later sweep skips straight to
// the Modified pass — the first sweep after a connect (via the poller, or
// on-demand through POST /overlay/reconcile) does the load, the rest ride
// the watermark. proceed is false when the backfill pass itself failed
// (already logged): the Modified and deletion phases must not spend
// incumbent quota sweeping a class whose initial load never converged this
// tick — the same "stop the rest of this class, not the others" contract
// every phase here honors.
func sweepBackfillPhase(ctx context.Context, deps sweepDeps, workspace, objectClass string, connectedAt time.Time) (proceed bool, err error) {
	truncated, err := overlay.Backfill(ctx, deps.inc, deps.ms, objectClass, connectedAt)
	if err != nil {
		if sweepMustStop(err) {
			return false, err
		}
		deps.log.WarnContext(ctx, "overlay reconcile: backfill pass failed, skipping this object class this tick",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return false, nil
	}
	if truncated {
		deps.log.WarnContext(ctx, "overlay reconcile: backfill capped by MARGINCE_OVERLAY_BACKFILL_LIMIT; this object class will report backfill-complete=false until its overlay_backfill_cursor row is cleared (unsetting the cap alone does not resume it)",
			"workspace", workspace, "object_class", objectClass)
	}
	return true, nil
}

// sweepModifiedPhase runs the incremental modified-record sweep: load the
// persisted watermark, then let Reconcile itself raise it through the floor
// (an unfloored class sweeps from the zero time — the incumbent's entire
// portal — which would undo the backfill cap) before sweeping, and
// checkpoint the advanced watermark. proceed is true only when the sweep
// genuinely converged and the deletion phase after it may safely run — false
// covers both "already logged and skipped, nothing more to do this class
// this tick" outcomes (an unreadable watermark, a failed Reconcile pass),
// matching the original single-function behavior where either failure
// returned before ever reaching ReconcileDeletions.
func sweepModifiedPhase(ctx context.Context, deps sweepDeps, workspace, objectClass string, connectedAt time.Time) (proceed bool, err error) {
	watermark, err := deps.ms.LoadReconcileWatermark(ctx, objectClass)
	if err != nil {
		// A watermark read is a local DB call, not an incumbent one — a blip
		// here is not a connection-level failure, so skip this class rather
		// than back the whole connection off.
		deps.log.WarnContext(ctx, "overlay reconcile: loading the persisted watermark failed, skipping this object class",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return false, nil
	}
	newWatermark, err := overlay.Reconcile(ctx, deps.inc, deps.ms, deps.meter, objectClass, watermark, connectedAt)
	if err != nil {
		if sweepMustStop(err) {
			return false, err
		}
		deps.log.WarnContext(ctx, "overlay reconcile sweep failed",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return false, nil
	}
	if newWatermark.After(watermark) {
		if err := deps.ms.SaveReconcileWatermark(ctx, objectClass, newWatermark, connectedAt); err != nil {
			if errors.Is(err, overlay.ErrConnectionGone) {
				return false, err
			}
			deps.log.WarnContext(ctx, "overlay reconcile: persisting the new watermark failed",
				"workspace", workspace, "object_class", objectClass, "err", err)
		}
	}
	return true, nil
}

// sweepDeletionPhase converges the OTHER direction: purge records the
// incumbent has deleted so they stop being readable from the mirror
// (branch-1b deletion feed). Run AFTER the Modified sweep within the same
// tick so a live-record page already fetched this pass can never
// resurrect a record this sweep just purged — HubSpot excludes archived
// records from the Modified/Search feed, so the two do not fight over the
// same row. The sweep full-scans the archived feed each pass and purges
// idempotently (ReconcileDeletions' own doc explains why a watermark would
// be unsound over HubSpot's unordered archived feed).
func sweepDeletionPhase(ctx context.Context, deps sweepDeps, workspace, objectClass string) error {
	if err := overlay.ReconcileDeletions(ctx, deps.inc, deps.ms, deps.meter, objectClass); err != nil {
		if sweepMustStop(err) {
			return err
		}
		deps.log.WarnContext(ctx, "overlay reconcile: deletion sweep failed",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
	return nil
}
