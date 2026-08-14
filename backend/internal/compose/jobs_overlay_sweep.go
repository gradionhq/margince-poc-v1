// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The overlay sweep's per-object-class phases: backfill, then modified, then
// deletion, then re-projection. Split out of jobs_overlay.go, which owns the
// reconcile job's dispatcher/worker pair and the connection-level
// orchestration these phases run under.

// sweepDeps bundles sweepObjectClass's per-connection collaborators — the
// live incumbent adapter, the identity-fenced store, the OVB meter, the
// re-fetch enqueuer, and the logger — so the phase functions below (and
// reconcileConnection's call site) pass one value instead of five positional
// ones.
type sweepDeps struct {
	inc   overlay.Incumbent
	ms    *overlay.MirrorStore
	meter *overlaybudget.Meter
	// enqueue schedules the re-projection phase's re-fetches. It is the
	// webhook receiver's own narrow insert surface (refetchEnqueuer,
	// overlaywebhook.go): both lanes ask for exactly one record to be read
	// again through the ingest the poller uses, so they enqueue the same job
	// the same way. Injected rather than reached for, so a test drives the
	// phase without standing up a River client.
	enqueue refetchEnqueuer
	log     *slog.Logger
}

// ambientRefetchEnqueuer schedules a re-fetch through the River client that is
// working the sweep job, so the poller needs no second client of its own — the
// posture ambientTelegramEnqueuer takes for the same reason. A context with no
// client is reported, never swallowed: the phase logs it and the next pass
// re-enqueues, rather than silently converging nothing.
type ambientRefetchEnqueuer struct{}

func (ambientRefetchEnqueuer) Enqueue(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) error {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return fmt.Errorf("overlay reconcile: no River client on the sweep's context: %w", err)
	}
	if _, err := client.Insert(ctx, args, opts); err != nil {
		return fmt.Errorf("overlay reconcile: enqueueing %s: %w", args.Kind(), err)
	}
	return nil
}

// sweepMustStop reports whether err is the disconnect-race fence's clean-stop
// signal (overlay.ErrConnectionGone), a sealed snapshot
// (overlay.ErrMirrorFrozen), or a connection-level incumbent failure
// (isConnectionLevelIncumbentError) — the three conditions
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
// converged), the incremental modified-record sweep, the opposite-direction
// deletion sweep — each on its own persisted watermark — and finally the
// re-projection of rows an older mapping declaration produced, each its own
// phase function below. Any phase's failure is logged and
// skips the REST of this class's sweep this tick (the next tick resumes from
// the checkpoint), never aborting the other classes on its own — that
// distinction is sweepMustStop's, below. workspace names the tenant this
// sweep runs for; connectedAt floors the incremental sweep of a class
// that has no watermark yet.
//
// It returns a non-nil error only when sweepMustStop says so — a
// connection-level incumbent failure or overlay.ErrConnectionGone — the
// signal reconcileConnection propagates to abort the sweep and back the
// connection off (or, for ErrConnectionGone, turns into a no-backoff clean
// stop). A per-object failure (a mapping/data defect, a DB read/write blip)
// is logged and skips the rest of THIS class with a nil return, so the
// connection-level loop moves on to the next class.
func sweepObjectClass(ctx context.Context, deps sweepDeps, workspace ids.WorkspaceID, objectClass string, connectedAt time.Time) error {
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
	if err := sweepDeletionPhase(ctx, deps, workspace, objectClass); err != nil {
		return err
	}
	sweepReprojectionPhase(ctx, deps, workspace, objectClass)
	return nil
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
func sweepBackfillPhase(ctx context.Context, deps sweepDeps, workspace ids.WorkspaceID, objectClass string, connectedAt time.Time) (proceed bool, err error) {
	truncated, err := overlay.Backfill(ctx, deps.inc, deps.ms, objectClass, connectedAt)
	if err != nil {
		if sweepMustStop(err) {
			return false, err
		}
		deps.log.WarnContext(ctx, "overlay reconcile: backfill pass failed, skipping this object class this tick",
			"workspace", workspace.String(), "object_class", objectClass, "err", err)
		return false, nil
	}
	if truncated {
		deps.log.WarnContext(ctx, "overlay reconcile: backfill capped by MARGINCE_OVERLAY_BACKFILL_LIMIT; this object class will report backfill-complete=false until its overlay_backfill_cursor row is cleared (unsetting the cap alone does not resume it)",
			"workspace", workspace.String(), "object_class", objectClass)
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
func sweepModifiedPhase(ctx context.Context, deps sweepDeps, workspace ids.WorkspaceID, objectClass string, connectedAt time.Time) (proceed bool, err error) {
	watermark, err := deps.ms.LoadReconcileWatermark(ctx, objectClass)
	if err != nil {
		// A watermark read is a local DB call, not an incumbent one — a blip
		// here is not a connection-level failure, so skip this class rather
		// than back the whole connection off.
		deps.log.WarnContext(ctx, "overlay reconcile: loading the persisted watermark failed, skipping this object class",
			"workspace", workspace.String(), "object_class", objectClass, "err", err)
		return false, nil
	}
	newWatermark, err := overlay.Reconcile(ctx, deps.inc, deps.ms, deps.meter, objectClass, watermark, connectedAt)
	if err != nil {
		if sweepMustStop(err) {
			return false, err
		}
		deps.log.WarnContext(ctx, "overlay reconcile sweep failed",
			"workspace", workspace.String(), "object_class", objectClass, "err", err)
		return false, nil
	}
	if newWatermark.After(watermark) {
		if err := deps.ms.SaveReconcileWatermark(ctx, objectClass, newWatermark, connectedAt); err != nil {
			if errors.Is(err, overlay.ErrConnectionGone) {
				return false, err
			}
			deps.log.WarnContext(ctx, "overlay reconcile: persisting the new watermark failed",
				"workspace", workspace.String(), "object_class", objectClass, "err", err)
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
func sweepDeletionPhase(ctx context.Context, deps sweepDeps, workspace ids.WorkspaceID, objectClass string) error {
	if err := overlay.ReconcileDeletions(ctx, deps.inc, deps.ms, deps.meter, objectClass); err != nil {
		if sweepMustStop(err) {
			return err
		}
		deps.log.WarnContext(ctx, "overlay reconcile: deletion sweep failed",
			"workspace", workspace.String(), "object_class", objectClass, "err", err)
		return nil
	}
	return nil
}

// reprojectionEnqueueLimit bounds how many rows one pass re-projects per
// object class. A declaration change puts EVERY row of a class out of date at
// once, and an estate can hold far more of them than a tick's worth of
// incumbent quota could ever re-read: unbounded, one pass would enqueue a
// re-fetch per row, and the queue — not the budget the meter guards at
// execution — would carry the whole estate. Bounded, a pass takes a prefix and
// leaves the rest for the next one, which is what makes convergence a matter
// of passes rather than of one burst.
//
// It is deliberately NOT MARGINCE_OVERLAY_BACKFILL_LIMIT: that knob is a
// dev/demo cap on the initial load and is unset in production, so bounding
// re-projection by it would mean "unbounded" in exactly the deployment that
// needs the bound.
//
// 200 bounds the QUEUE, and is not a throughput promise: what governs how fast
// a class converges is the incumbent's DAILY REST allocation. Every re-fetch
// enqueued here reserves one REST unit against SourceForceFresh before its live
// read (jobs_overlay_refetch.go), and the meter sheds on the whole UTC day's
// REST total across every source — with the built-in HubSpot budget
// (deployconfig/overlaybudget.go: a 90,000 cap, shed at 0.90 of it) that is
// ~81,000 live reads a day, shared with the poller's own spend and with
// interactive force-fresh. So a class holding more stale rows than the day's
// remaining allocation cannot converge inside that day however often the sweep
// ticks, and a mapping change is felt past the flip: it drives the shared daily
// total toward the shed band, where interactive force-fresh degrades to
// mirror-with-staleness (AC-OV-7) for the rest of the UTC day. What the bound
// buys is that the spend arrives as a trickle the meter can arbitrate against
// live reads, rather than as a queue the sweep has already committed to.
const reprojectionEnqueueLimit = 200

// sweepReprojectionPhase re-fetches the rows an OLDER mapping declaration
// projected. A mirror row holds a projection and is re-projected only when the
// incumbent's own baseline advances, so a mapping change leaves every
// already-mirrored row holding a payload today's declaration would never
// produce — and the flip freezes the mirror and writes whatever a row holds as
// a durable native row. The flip preflight refuses exactly these rows
// (overlay's projectionstaleness.go); this phase is what clears them, so that
// block is temporary rather than permanent.
//
// It runs LAST, after the watermark phases, so freshness and the incumbent
// budget keep priority: an estate under budget pressure stays fresh while it
// converges slowly, and the flip stays blocked meanwhile, which is correct.
//
// It needs no cursor of its own. Ingest stamps the fingerprint of the
// declaration that produced the record, so a row leaves the stale set as soon
// as its re-fetch lands; successive passes select the next rows still in it,
// and the phase becomes a no-op once the class has converged.
//
// It reports nothing to its caller. Every failure here belongs to this object
// class alone — an undeclared class, a failed read, a refused enqueue — and
// the next pass retries it, so there is no later phase to gate and no
// connection-level condition to propagate.
func sweepReprojectionPhase(ctx context.Context, deps sweepDeps, workspace ids.WorkspaceID, objectClass string) {
	m, ok := hubspot.Mapping(objectClass)
	if !ok {
		// Unreachable while overlayObjectClasses is exactly the set the
		// registry declares, and handled rather than assumed away because the
		// two are separate lists: an undeclared class has no fingerprint to
		// judge its rows against, and calling them all stale would re-read the
		// class every pass forever.
		deps.log.WarnContext(ctx, "overlay reconcile: no mapping declaration for this object class, skipping re-projection",
			"workspace", workspace.String(), "object_class", objectClass)
		return
	}
	stale, err := deps.ms.StaleProjections(ctx, m, reprojectionEnqueueLimit)
	if err != nil {
		deps.log.WarnContext(ctx, "overlay reconcile: listing the rows an older declaration projected failed",
			"workspace", workspace.String(), "object_class", objectClass, "err", err)
		return
	}
	for _, externalID := range stale {
		if err := deps.enqueue.Enqueue(ctx, OverlayRefetchArgs{
			Workspace:      workspace.UUID,
			IncumbentClass: objectClass,
			ExternalID:     externalID,
		}, reprojectionInsertOpts()); err != nil {
			// One refused insert says the queue is unavailable, not that this
			// row is special: stop this class here rather than logging the
			// same failure once per row, and let the next pass re-enqueue.
			deps.log.WarnContext(ctx, "overlay reconcile: enqueueing a re-projection re-fetch failed",
				"workspace", workspace.String(), "object_class", objectClass, "external_id", externalID, "err", err)
			return
		}
	}
}

// reprojectionInsertOpts is unique-by-args over the states a job passes
// through before it is worked: a sweep tick that runs again while an earlier
// re-fetch is still queued must not stack a second read of the same record —
// the args ARE the coalescing key, exactly as they are for the webhook lane's
// signal-driven re-fetch (overlaywebhook.go).
func reprojectionInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates}}
}
