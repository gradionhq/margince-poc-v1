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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (OverlayReconcileArgs) FleetWide() {}

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
	pool *pgxpool.Pool
	log  *slog.Logger
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

// Work is the DISPATCHER: it runs the due-scan (whose next_sweep_at gate is
// the backoff) and enqueues one reconcile per due connection. It sweeps
// nothing itself.
//
// The fan-out lands on a SERIAL queue. overlaybudget.ConsumeSearch counts but
// does not pace, and its keys are per workspace, so it cannot bound a
// provider-level burst; running these concurrently could exceed the
// incumbent's per-second Search limit. Each connection still gets its own job
// row, which is the visibility this phase is after.
func (w *overlayReconcileWorker) Work(ctx context.Context, _ *river.Job[OverlayReconcileArgs]) error {
	due, enumErr := overlay.DueOverlayConnections(ctx, w.pool)
	if len(due) == 0 {
		// Nothing to fan out. The client is resolved only past this point
		// because ClientFromContext panics when there is none, and a tick with
		// no due connection has no insert to make.
		return jobs.FaultContext(ctx, enumErr)
	}
	client := river.ClientFromContext[pgx.Tx](ctx)
	for _, d := range due {
		if _, err := client.Insert(ctx, OverlayReconcileWorkspaceArgs{Workspace: d.Workspace.UUID},
			workspaceSweepOpts(overlayReconcileQueue, overlaySweepMaxAttempts)); err != nil {
			// A refused enqueue means this workspace gets no sweep at all, so
			// it fails the DISPATCHER rather than being logged past: a green
			// tick over a tenant nobody swept is the shape this phase removes.
			enumErr = errors.Join(enumErr, fmt.Errorf("enqueueing the reconcile for workspace %s: %w", d.Workspace, err))
		}
	}
	return jobs.FaultContext(ctx, enumErr)
}

// overlaySweepMaxAttempts is ONE: this kind's retry is the sweep backoff the
// worker itself records (overlay_sync_state.next_sweep_at — at least minutes
// out, and longer for a rate limit), and the dispatcher's due-scan is gated on
// it. River's own ladder does not read that column, so a second rung would
// re-sweep a throttled incumbent seconds after the worker deliberately paced
// it. The failed row stays failed and visible; the next due tick is the retry.
const overlaySweepMaxAttempts = 1

// OverlayReconcileWorkspaceArgs reconciles ONE workspace's incumbent mirror.
// It names the workspace and nothing else: the connection's incumbent, region,
// credential ref and connected-at are re-read at work time, so a connection
// that changed between the due-scan and the sweep is swept under its CURRENT
// identity rather than the scan's snapshot of it.
type OverlayReconcileWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (OverlayReconcileWorkspaceArgs) Kind() string { return "overlay_reconcile_workspace" }

// WorkspaceID binds this reconcile to its tenant (jobs.WorkspaceScoped).
func (a OverlayReconcileWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// overlayReconcileWorkspaceWorker runs one workspace's reconcile and records
// its outcome against the sweep backoff.
type overlayReconcileWorkspaceWorker struct {
	river.WorkerDefaults[OverlayReconcileWorkspaceArgs]
	pool         *pgxpool.Pool
	vault        keyvault.Vault
	ms           *overlay.MirrorStore
	meter        *overlaybudget.Meter
	newIncumbent func(region, token string) overlay.Incumbent
	log          *slog.Logger
}

func (w *overlayReconcileWorkspaceWorker) Work(ctx context.Context, job *river.Job[OverlayReconcileWorkspaceArgs]) error {
	bound, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx := reconcileWorkerCtx(bound, ids.From[ids.WorkspaceKind](job.Args.Workspace))

	d, err := overlay.DueConnection(wsCtx, w.pool)
	if errors.Is(err, apperrors.ErrNotFound) {
		// Disconnected, backed off, or frozen between the dispatcher's scan and
		// this job — the ordinary race, and there is nothing left to reconcile.
		return nil
	}
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}

	// The outcome-recording store is fenced on the connection's OWN identity
	// (WithFenceIdentity): overlay_sync_state is one of the tables teardown
	// purges, so recording a backoff or success against a workspace that
	// disconnected — or disconnected AND reconnected — mid-sweep would
	// resurrect or misattribute a purged row; the fence makes the recording
	// abort with ErrConnectionGone instead. A rate-limit/auth failure leaves
	// the connection row 'active' (only Disconnect revokes it), so the
	// legitimate backoff paths still record.
	recMS := w.ms.WithFenceIdentity(d.ConnectedAt)
	sweepErr := reconcileConnection(wsCtx, w.pool, w.vault, w.ms, w.meter, w.log, d, w.newIncumbent)
	if errors.Is(sweepErr, overlay.ErrConnectionGone) {
		// The connection was disconnected, or disconnected AND reconnected,
		// mid-sweep: every fenced write aborted, so nothing was resurrected
		// into the now-native workspace or misattributed to a connection this
		// sweep never actually swept for. Neither a failure to back off (the
		// next due-scan reflects the current state) nor a success to
		// checkpoint against a now-stale identity.
		w.log.DebugContext(wsCtx, "overlay reconcile: connection generation changed mid-sweep, stopping cleanly",
			"workspace", d.Workspace.String())
		return nil
	}
	// Record the sweep outcome so a connection-level failure backs the next
	// sweep off (overlay_sync_state), instead of re-sweeping a revoked,
	// rate-limited or unreachable connection hot every tick; one clean sweep
	// resets the backoff. Only the periodic poller schedules backoff — the
	// on-demand /overlay/reconcile handler returns its error to the admin
	// without touching the schedule.
	//
	// A fenced ErrConnectionGone in the RECORDING means the connection was
	// revoked between the sweep and this write — benign, nothing to pace.
	if sweepErr != nil {
		if recErr := recMS.RecordSweepFailure(wsCtx, sweepErr, time.Now()); recErr != nil && !errors.Is(recErr, overlay.ErrConnectionGone) {
			w.log.WarnContext(wsCtx, "overlay reconcile: recording the sweep-failure backoff failed",
				"workspace", d.Workspace.String(), "err", recErr)
		}
		return jobs.FaultContext(ctx, sweepErr)
	}
	if recErr := recMS.RecordSweepSuccess(wsCtx, time.Now()); recErr != nil && !errors.Is(recErr, overlay.ErrConnectionGone) {
		w.log.WarnContext(wsCtx, "overlay reconcile: resetting the sweep backoff after success failed",
			"workspace", d.Workspace.String(), "err", recErr)
	}
	return nil
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
		// portal every tick (overlay.Reconcile's internal reconcileFloor).
		// Refuse rather than burn the quota.
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
	if err := overlay.BackfillPortalBinding(ctx, pool, inc, d.ConnectedAt); err != nil {
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
		if errors.Is(err, overlay.ErrConnectionGone) {
			return err
		}
		if isConnectionLevelIncumbentError(err) {
			return fmt.Errorf("overlay reconcile: email-mapping revalidation failed: %w", err)
		}
		log.WarnContext(ctx, "overlay reconcile: periodic email-mapping revalidation failed",
			"workspace", d.Workspace.String(), "err", err)
	}

	deps := sweepDeps{inc: inc, ms: ms, meter: meter, log: log}
	for _, objectClass := range overlayObjectClasses {
		// A connection-level failure sweeping a SCOPE-BACKED class
		// (contacts/companies/deals) aborts the whole sweep (the caller backs
		// the connection off); a per-object failure was already logged inside
		// sweepObjectClass and skips only that class. leads and the engagement
		// classes are swept best-effort with no requested scope, so a portal
		// that gates one of them (a 403/404 for that object alone) skips just
		// that class here — overlaySweepAborts encodes the distinction.
		if err := sweepObjectClass(ctx, deps, d.Workspace.String(), objectClass, d.ConnectedAt); err != nil {
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
