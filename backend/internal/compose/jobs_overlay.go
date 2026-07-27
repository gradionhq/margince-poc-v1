// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OverlayReconcileArgs schedules one incremental reconcile pass across
// every workspace running in overlay mode (design.md §4.4: "Pull always
// runs" — branch 1's one continuous-sync trigger; the webhook-as-signal
// push lane is deferred to branch 1b).
type OverlayReconcileArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (OverlayReconcileArgs) Kind() string { return "overlay_reconcile" }

// overlayReconcileWorker walks every overlay-mode workspace's active
// incumbent connection (overlay.DueOverlayConnections — the same
// fleet-walk shape gmailSyncWorker drives via capture.Registry.
// DueConnections) and runs overlay.Reconcile per object class. A single
// workspace's or object class's failure is logged and skipped, never
// aborting the pass; only a fleet-enumeration failure is returned (so
// River retries the tick, mirroring gmailSyncWorker's own contract).
//
// Building the per-workspace incumbent adapter HERE — via newIncumbent
// (hubspotIncumbentFactory in production) from the due connection's own
// vaulted token + region — answers the seam left open by
// compose/overlay.go's NewOverlayProvider (which wires FreshnessReader
// with inc:nil, "a per-workspace credential lookup the Dispatcher — one
// process-wide instance shared by every workspace — has no seam to
// thread per call"). That gap is NOT closed by this worker:
// NewOverlayProvider serves cmd/api's live HTTP reads under ONE shared
// Provider/FreshnessReader instance across every tenant, so a genuinely
// per-request-workspace adapter there needs its own construction-time
// change, out of scope for the poller. This worker's adapter is built
// fresh per due connection, per tick, and discarded after — it never
// leaks into cmd/api's force-fresh path. The factory is injected (not a
// hardcoded hubspot.NewAdapter) so the whole sweep is drivable against a
// fake incumbent in a test.
type overlayReconcileWorker struct {
	river.WorkerDefaults[OverlayReconcileArgs]
	pool         *pgxpool.Pool
	vault        keyvault.Vault
	ms           *overlay.MirrorStore
	meter        *overlaybudget.Meter
	log          *slog.Logger
	newIncumbent func(region, token string) overlay.Incumbent
}

// reconcileWorkerCtx builds the per-workspace scope one due connection's
// sweep runs under. Reconcile's emit path (overlay/reconcile.go's
// emitMirrorConflict, via storekit.LogSystem/Emit) requires a bound
// actor AND correlation id — WorkspaceID alone is not enough. Mirrors
// deals.CloseDateCorrector.Sweep's own per-workspace scope construction
// (closedatesweep.go) exactly, the sibling system job that already
// carries this same requirement. Extracted to its own function (rather
// than inlined in Work's loop) so a unit test can assert the binding
// directly, without standing up River or a due-connections fixture.
func reconcileWorkerCtx(ctx context.Context, workspaceID ids.WorkspaceID) context.Context {
	wsCtx := principal.WithWorkspaceID(ctx, workspaceID.UUID)
	wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:overlay-reconcile"})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	return wsCtx
}

func (w *overlayReconcileWorker) Work(ctx context.Context, _ *river.Job[OverlayReconcileArgs]) error {
	due, enumErr := overlay.DueOverlayConnections(ctx, w.pool)
	for _, d := range due {
		wsCtx := reconcileWorkerCtx(ctx, d.Workspace)
		// The outcome-recording store is fenced on d's OWN connection identity
		// (WithFenceIdentity): overlay_sync_state is one of the tables teardown
		// purges, so recording a backoff or success against a workspace that
		// disconnected — or disconnected AND reconnected — before/after the
		// sweep would resurrect/misattribute a purged row; the fence makes the
		// recording abort with ErrConnectionGone instead. A rate-limit/auth
		// failure leaves the connection row 'active' (only Disconnect revokes
		// it), so the legitimate backoff paths still record.
		recMS := w.ms.WithFenceIdentity(d.ConnectedAt)
		err := reconcileConnection(wsCtx, w.pool, w.vault, w.ms, w.meter, w.log, d, w.newIncumbent)
		// Record the sweep outcome so a connection-level failure backs the
		// next sweep off (overlay_sync_state), instead of re-sweeping a
		// revoked/rate-limited/unreachable connection hot every tick; one
		// clean sweep resets the backoff. Only the periodic poller schedules
		// backoff — the on-demand /overlay/reconcile handler returns its
		// error to the admin without touching the schedule.
		if errors.Is(err, overlay.ErrConnectionGone) {
			// The connection was disconnected, or disconnected AND reconnected,
			// mid-sweep: every fenced write aborted, so nothing was resurrected
			// into the now-native workspace or misattributed to a connection
			// this sweep never actually swept for. This is neither a failure to
			// back off (the fresh due-scan already reflects the current state —
			// gone, or due again under the new connection) nor a success to
			// checkpoint against d's now-stale identity. Move on.
			w.log.DebugContext(wsCtx, "overlay reconcile: connection generation changed mid-sweep, stopping cleanly",
				"workspace", d.Workspace.String())
			continue
		}
		if err != nil {
			w.log.WarnContext(wsCtx, "overlay reconcile: sweeping this workspace's connection failed",
				"workspace", d.Workspace.String(), "err", err)
			// A fenced ErrConnectionGone here means the connection was revoked
			// between the sweep and this recording — benign, nothing to pace.
			if recErr := recMS.RecordSweepFailure(wsCtx, err, time.Now()); recErr != nil && !errors.Is(recErr, overlay.ErrConnectionGone) {
				w.log.WarnContext(wsCtx, "overlay reconcile: recording the sweep-failure backoff failed",
					"workspace", d.Workspace.String(), "err", recErr)
			}
			continue
		}
		if recErr := recMS.RecordSweepSuccess(wsCtx, time.Now()); recErr != nil && !errors.Is(recErr, overlay.ErrConnectionGone) {
			w.log.WarnContext(wsCtx, "overlay reconcile: resetting the sweep backoff after success failed",
				"workspace", d.Workspace.String(), "err", recErr)
		}
	}
	return enumErr
}

// isConnectionLevelIncumbentError reports whether err is a WHOLE-connection
// incumbent health failure — a rate limit, an auth rejection, or an
// unreachable incumbent — as opposed to one object class's mapping/data
// defect. Only connection-level failures abort the sweep and back the
// connection off; a per-object failure is logged and the sweep moves on, so
// one bad object never quarantines a whole workspace. It lives in compose,
// not overlay, because it names hubspot.ErrUnreachable, which the overlay
// package cannot import without a cycle.
func isConnectionLevelIncumbentError(err error) bool {
	return errors.Is(err, apperrors.ErrIncumbentBudgetExhausted) ||
		errors.Is(err, apperrors.ErrPermissionDenied) ||
		errors.Is(err, hubspot.ErrUnreachable)
}

// reconcileConnection builds a live incumbent adapter over d's vaulted
// credential and sweeps every overlayObjectClasses class for it — the
// periodic fleet worker's (Work, above) per-connection sweep body, kept as
// its own function so the "resolve the vaulted token, build a live adapter,
// sweep every object class" sequence has one place to change (the "fix the
// invariant, not the call site" rule). ctx is already scoped to d's own
// workspace and carries the synthesized system principal Work bound;
// reconcileConnection itself makes no assumption about that. The on-demand
// /overlay/reconcile request (overlay.Service.RequestSweep) does not call
// this at all — it only marks the workspace due, and this same periodic
// worker picks the sweep up on its next tick. A per-object-class failure
// (unreadable watermark, a failed sweep page, a failed watermark save)
// is logged and skipped, never aborting the rest of the classes. A
// CONNECTION-level failure — an unsupported incumbent, a failed vault
// resolution, or an incumbent call that comes back rate-limited / auth-
// rejected / unreachable (isConnectionLevelIncumbentError) — stops the
// sweep and returns an error, which the periodic caller records as a
// backoff (overlay_sync_state) so a dead or throttled connection is not
// re-swept hot every tick.
func reconcileConnection(ctx context.Context, pool *pgxpool.Pool, vault keyvault.Vault, ms *overlay.MirrorStore, meter *overlaybudget.Meter, log *slog.Logger, d overlay.DueOverlayConnection, newIncumbent func(region, token string) overlay.Incumbent) error {
	if d.Incumbent != incumbentHubSpot {
		// Branch 1 wires only HubSpot (design.md §2 D2/D3) — a connection
		// row naming any other incumbent has no adapter here; an honest,
		// named gap, never a guessed adapter.
		return fmt.Errorf("overlay reconcile: no adapter for incumbent %q", d.Incumbent)
	}
	if d.ConnectedAt.IsZero() {
		// connected_at is NOT NULL, so a zero means this struct was built without
		// it — and sweeping would then floor at the zero time, i.e. the whole
		// portal every tick (ReconcileFloor). Refuse rather than burn the quota.
		return fmt.Errorf("overlay reconcile: connection for workspace %s carries no connected_at; refusing to sweep from the epoch", d.Workspace)
	}
	token, err := vault.Get(ctx, d.Workspace, d.CredentialRef)
	if err != nil {
		return fmt.Errorf("overlay reconcile: resolving the vaulted token: %w", err)
	}
	// newIncumbent builds THIS connection's adapter from its own vaulted
	// region+token — injected (hubspotIncumbentFactory in production) so
	// the whole sweep is drivable against a fake incumbent in a test,
	// rather than reaching a real HubSpot over the network.
	inc := newIncumbent(d.Region, string(token))
	// Self-heal the webhook tenant binding (OVA-DDL-3): if the connect-time
	// portal fetch failed (best-effort, left null), fill it from this sweep's
	// live adapter so the webhook lane can bind that portal — a transient
	// connect-time blip no longer permanently disables push refresh. Gated on
	// the binding being unset, so a bound connection pays no per-sweep call.
	// Best-effort: a failure here never aborts the record sweep below.
	if err := overlay.BackfillPortalBinding(ctx, pool, inc); err != nil {
		log.WarnContext(ctx, "overlay reconcile: backfilling the webhook portal binding failed",
			"workspace", d.Workspace.String(), "err", err)
	}
	// Prune expired echo-ledger entries (OVA-DDL-6 hygiene): bounds the table's
	// growth and does not retain a value_canonical past the window. Best-effort
	// — correctness never depends on it (Classify already filters by the open
	// window), so a failure never aborts the record sweep.
	if _, err := overlay.NewWriteLedger(pool).PruneExpired(ctx); err != nil {
		log.WarnContext(ctx, "overlay reconcile: pruning expired write-ledger entries failed",
			"workspace", d.Workspace.String(), "err", err)
	}
	// Bind the store to THIS connection's live adapter so seeding,
	// UpsertUserMap's email re-verification, and Ingest's owner-change
	// revalidation all resolve against the incumbent's CURRENT owner
	// emails — the worker-level store carries only the read-path
	// placeholder resolver (compose/overlay.go), which cannot name an
	// owner.
	// WithFenceIdentity engages the disconnect-race fence for the sweep's
	// writes on d's OWN connection identity: if this workspace is
	// disconnected mid-sweep — or disconnected AND reconnected, so an active
	// row exists again but under a NEW generation — every fenced write aborts
	// with overlay.ErrConnectionGone rather than resurrecting purged
	// incumbent-derived data, or landing it under a connection this sweep
	// never actually swept for (overlay's disconnectfence.go). reconcileConnection
	// and its callees treat that signal as a clean stop.
	ms = ms.WithResolver(inc).WithFenceIdentity(d.ConnectedAt)

	// Seed mirror_user_map from the incumbent's owners directory each
	// sweep: match every incumbent owner's email to an existing workspace
	// app_user and write the email-sourced mapping (design.md §4.6 — a
	// MATCH, never an import). Running it per sweep (not only on connect)
	// catches users who joined the workspace after connect and owners
	// added incumbent-side since. Best-effort: a directory-fetch or
	// per-owner match failure is logged and does not abort the record
	// sweep below — an unseeded mapping is a fail-closed-eventually gap
	// (the NEXT sweep retries), never a reason to stop syncing records.
	if owners, err := inc.Owners(ctx); err != nil {
		// The owners fetch is the sweep's first incumbent call. A
		// connection-level failure here (auth revoked, rate-limited,
		// unreachable) means every later call fails too, so abort and let
		// the caller back the connection off rather than hammering it. A
		// non-connection-level error stays best-effort (seeding is; the
		// record sweep can still proceed).
		if isConnectionLevelIncumbentError(err) {
			return fmt.Errorf("overlay reconcile: owners directory fetch failed: %w", err)
		}
		log.WarnContext(ctx, "overlay reconcile: fetching the owners directory to seed mirror_user_map failed",
			"workspace", d.Workspace.String(), "err", err)
	} else if err := ms.SeedUserMap(ctx, d.Incumbent, owners); err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) {
			return err
		}
		log.WarnContext(ctx, "overlay reconcile: seeding mirror_user_map from the owners directory failed",
			"workspace", d.Workspace.String(), "err", err)
	}

	// Periodic realization of design.md §4.6 rule 5: an owner's email can
	// change with NO record ever getting reassigned, so Ingest's own
	// reassignment-triggered revalidateEmailMapping call (mirrorstore.go)
	// never gets a chance to run for that owner. Once per sweep, per
	// connection, re-check every email-sourced mapping this workspace has
	// against inc's CURRENT owner emails — bounded to the distinct set of
	// already-mapped owners, not a per-record scan. A failure here is
	// logged and does not abort the object-class sweep below: a stale
	// mapping is a fail-closed-eventually gap (the NEXT sweep tries
	// again), not a reason to stop syncing records this tick.
	if err := ms.RevalidateEmailMappings(ctx, inc); err != nil {
		// RevalidateEmailMappings is intentionally unfenced (it only
		// revalidates/clears, never resurrects — visibility.go), so it never
		// surfaces ErrConnectionGone; no clean-stop branch belongs here.
		if isConnectionLevelIncumbentError(err) {
			return fmt.Errorf("overlay reconcile: email-mapping revalidation failed: %w", err)
		}
		log.WarnContext(ctx, "overlay reconcile: periodic email-mapping revalidation failed",
			"workspace", d.Workspace.String(), "err", err)
	}

	for _, objectClass := range overlayObjectClasses {
		// A connection-level failure sweeping a SCOPE-BACKED class
		// (contacts/companies/deals) aborts the whole sweep (the caller backs
		// the connection off); a per-object failure was already logged inside
		// sweepObjectClass and skips only that class. leads and the engagement
		// classes are swept best-effort with no requested scope, so a portal
		// that gates one of them (a 403/404 for that object alone) skips just
		// that class here — overlaySweepAborts encodes the distinction.
		if err := sweepObjectClass(ctx, inc, ms, meter, log, d.Workspace.String(), objectClass, d.ConnectedAt); err != nil {
			if overlaySweepAborts(objectClass, err) {
				return err
			}
			// A best-effort class (leads/engagements, swept with no requested
			// scope) failed on a per-object condition — a missing scope, an
			// absent object, or a portal-shaped validation error. Log the full
			// err (the cause varies) and move on; it never breaks the
			// scope-backed classes.
			log.WarnContext(ctx, "overlay reconcile: best-effort object class sweep failed, skipping it",
				"workspace", d.Workspace.String(), "object_class", objectClass, "err", err)
		}
	}
	return nil
}

// sweepObjectClass runs one object class's full convergence for a
// connection: the initial backfill (a cheap no-op once its cursor has
// converged), the incremental modified-record sweep, then the
// opposite-direction deletion sweep — each on its own persisted watermark.
// Any step's failure is logged and skips the REST of this class's sweep
// this tick (the next tick resumes from the checkpoint), never aborting the
// other classes — which is why it returns nothing. workspace is the
// stringified id, for logging only; connectedAt floors the incremental sweep
// of a class that has no watermark yet. Extracted from reconcileConnection so
// the per-class sequence reads as one unit and the connection-level loop
// stays short.
// It returns a non-nil error only for a connection-level incumbent failure
// (isConnectionLevelIncumbentError) — the signal reconcileConnection
// propagates to abort the sweep and back the connection off — or
// overlay.ErrConnectionGone, the disconnect-race fence's clean-stop signal
// reconcileConnection turns into a no-backoff stop. A per-object failure (a
// mapping/data defect, a DB read/write blip) is logged and skips the rest of
// THIS class with a nil return, so the connection-level loop moves on to the
// next class.
func sweepObjectClass(ctx context.Context, inc overlay.Incumbent, ms *overlay.MirrorStore, meter *overlaybudget.Meter, log *slog.Logger, workspace, objectClass string, connectedAt time.Time) error {
	// Initial full load before the incremental sweep: Backfill lists the
	// object class id-cursor style AND fetches its associations (design.md
	// §4.4), checkpointing overlay_backfill_cursor so SyncStatus's
	// backfillComplete answers truthfully. It is a cheap no-op once its
	// cursor has converged, so every later sweep skips straight to the
	// Modified pass — the first sweep after a connect (via the poller, or
	// on-demand through POST /overlay/reconcile) does the load, the rest
	// ride the watermark.
	if truncated, err := overlay.Backfill(ctx, inc, ms, objectClass, connectedAt); err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) || isConnectionLevelIncumbentError(err) {
			return err
		}
		log.WarnContext(ctx, "overlay reconcile: backfill pass failed, skipping this object class this tick",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	} else if truncated {
		log.WarnContext(ctx, "overlay reconcile: backfill capped by MARGINCE_OVERLAY_BACKFILL_LIMIT; this object class will report backfill-complete=false until its overlay_backfill_cursor row is cleared (unsetting the cap alone does not resume it)",
			"workspace", workspace, "object_class", objectClass)
	}
	watermark, err := ms.LoadReconcileWatermark(ctx, objectClass)
	if err != nil {
		// A watermark read is a local DB call, not an incumbent one — a blip
		// here is not a connection-level failure, so skip this class rather
		// than back the whole connection off.
		log.WarnContext(ctx, "overlay reconcile: loading the persisted watermark failed, skipping this object class",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
	// An unfloored class sweeps from the zero time — the incumbent's entire
	// portal (see ReconcileFloor); raising it keeps the cap from being undone.
	since := overlay.ReconcileFloor(watermark, connectedAt)
	newWatermark, err := overlay.Reconcile(ctx, inc, ms, meter, objectClass, since)
	if err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) || isConnectionLevelIncumbentError(err) {
			return err
		}
		log.WarnContext(ctx, "overlay reconcile sweep failed",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
	if newWatermark.After(since) {
		if err := ms.SaveReconcileWatermark(ctx, objectClass, newWatermark, connectedAt); err != nil {
			if errors.Is(err, overlay.ErrConnectionGone) {
				return err
			}
			log.WarnContext(ctx, "overlay reconcile: persisting the new watermark failed",
				"workspace", workspace, "object_class", objectClass, "err", err)
		}
	}

	// Converge the OTHER direction: purge records the incumbent has deleted
	// so they stop being readable from the mirror (branch-1b deletion feed).
	// Run AFTER the Modified sweep within the same tick so a live-record
	// page already fetched this pass can never resurrect a record this sweep
	// just purged — HubSpot excludes archived records from the
	// Modified/Search feed, so the two do not fight over the same row. The
	// sweep full-scans the archived feed each pass and purges idempotently
	// (ReconcileDeletions' own doc explains why a watermark would be unsound
	// over HubSpot's unordered archived feed).
	if err := overlay.ReconcileDeletions(ctx, inc, ms, meter, objectClass); err != nil {
		if isConnectionLevelIncumbentError(err) {
			return err
		}
		log.WarnContext(ctx, "overlay reconcile: deletion sweep failed",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
	return nil
}
