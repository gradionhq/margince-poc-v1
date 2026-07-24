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

// OverlayRefetchArgs is the webhook-as-signal targeted re-fetch (OVA-WIRE-10):
// a validly-signed, portal-bound webhook enqueues one of these to refresh the
// named record through the same idempotent ingest the poller uses. The args
// ARE the coalescing key — River's unique-by-args (OVA-PARAM-10, scheduled a
// short window ahead) collapses a record edited rapidly in the incumbent to
// ONE re-fetch rather than N. IncumbentClass is the HubSpot object class
// (contacts/companies/deals/leads); ExternalID is the mirror external id.
type OverlayRefetchArgs struct {
	Workspace      string `json:"workspace"`
	IncumbentClass string `json:"incumbent_class"`
	ExternalID     string `json:"external_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (OverlayRefetchArgs) Kind() string { return "overlay_refetch" }

// overlayRefetchWorker executes one webhook-driven single-record re-fetch: it
// resolves the workspace's active connection, builds a live incumbent adapter
// over its vaulted token, reads the one record, and ingests it through the
// fenced, resolver-bound store — the SAME idempotent, owner-revalidating path
// the reconcile sweep uses, so a webhook refresh and a poller sweep converge
// on one mirror state. The poller still heals any gap a signal misses.
type overlayRefetchWorker struct {
	river.WorkerDefaults[OverlayRefetchArgs]
	pool  *pgxpool.Pool
	vault keyvault.Vault
	ms    *overlay.MirrorStore
	// meter is the OVB budget. A webhook re-fetch is a live single-record REST
	// read-through — the same traffic category force-fresh meters, so it
	// reserves against SourceForceFresh before the incumbent read and SHEDS to
	// the poller when the budget is spent. A single-record GET is GATE-able
	// against the REST window (reserve/shed); the poller's Modified sweep, by
	// contrast, is a Search-API call PACED by the per-second search window with
	// its REST spend consumed unconditionally on SourcePoller — so reserve/shed
	// is the right shape here, force-fresh's shape, not the poller's. Without
	// this, a burst of signals would spend incumbent REST quota the OVB budget
	// never sees. A dedicated webhook source (admin-breakdown granularity) would
	// be an OVB-AC-5 spec change — a tracked follow-up, not needed for the
	// "account for every live call" invariant this closes.
	meter        *overlaybudget.Meter
	log          *slog.Logger
	newIncumbent func(region, token string) overlay.Incumbent
}

func (w *overlayRefetchWorker) Work(ctx context.Context, job *river.Job[OverlayRefetchArgs]) error {
	wsID, err := ids.ParseAs[ids.WorkspaceKind](job.Args.Workspace)
	if err != nil {
		// A malformed workspace id is a permanent defect, not a transient
		// failure — return nil so River does not retry an unfixable job.
		w.log.ErrorContext(ctx, "overlay refetch: unparseable workspace id in job args",
			"workspace", job.Args.Workspace, "err", err)
		return nil
	}
	wsCtx := reconcileWorkerCtx(ctx, wsID)
	conn, err := overlay.ActiveConnection(wsCtx, w.pool)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// The workspace disconnected since the signal arrived — nothing to
			// refresh, and teardown owns the mirror. Not a retryable failure.
			return nil
		}
		return fmt.Errorf("overlay refetch: reading the active connection: %w", err)
	}
	if conn.Incumbent != incumbentHubSpot {
		return nil
	}
	// A mirror halted by a ledger value-hash collision (OVA-AC-3) refuses all
	// sync — do not spend an incumbent read or ingest against a mirror we no
	// longer trust (the halt is cleared by disconnect today). This closes the
	// gap where a re-fetch enqueued before the halt (coalesced 5s ahead) would
	// otherwise still run: the halt is re-checked here, at execution.
	if halted, err := overlay.NewWriteLedger(w.pool).Halted(wsCtx); err != nil {
		return fmt.Errorf("overlay refetch: reading the mirror-halt flag: %w", err)
	} else if halted {
		w.log.WarnContext(wsCtx, "overlay refetch: mirror is halted (ledger collision), skipping",
			"workspace", job.Args.Workspace, "class", job.Args.IncumbentClass, "id", job.Args.ExternalID)
		return nil
	}
	token, err := w.vault.Get(wsCtx, conn.Workspace, conn.CredentialRef)
	if err != nil {
		return fmt.Errorf("overlay refetch: resolving the vaulted token: %w", err)
	}
	inc := w.newIncumbent(conn.Region, string(token))
	// Reserve one REST unit BEFORE the live read (OVB-AC-2/AC-5), so the
	// webhook lane's incumbent calls are accounted for like every other. On
	// shed skip the re-fetch — the signal is an optimization, and the poller
	// heals within its interval; never spend live quota we cannot account for.
	// A role wired without a configured meter gets the fail-closed placeholder
	// (nil Redis client) here, which sheds every reservation — so an
	// unaccountable read is skipped, never made. A meter error is transient —
	// retry.
	if allowed, err := w.meter.ReserveREST(wsCtx, conn.Incumbent, overlaybudget.SourceForceFresh, 1); err != nil {
		return fmt.Errorf("overlay refetch: reserving the incumbent budget: %w", err)
	} else if !allowed {
		w.log.InfoContext(wsCtx, "overlay refetch: incumbent budget shed, deferring to the poller",
			"workspace", job.Args.Workspace, "class", job.Args.IncumbentClass, "id", job.Args.ExternalID)
		return nil
	}
	rec, err := inc.Get(wsCtx, job.Args.IncumbentClass, job.Args.ExternalID)
	if err != nil {
		// A connection-level failure (rate-limit/auth/unreachable) is retryable
		// — return it so River backs off and retries. A record that is simply
		// gone or unmappable is not retryable: the deletion feed / poller
		// reconciles it, so log and drop rather than retry forever.
		if isConnectionLevelIncumbentError(err) {
			return fmt.Errorf("overlay refetch: reading %s/%s: %w", job.Args.IncumbentClass, job.Args.ExternalID, err)
		}
		w.log.WarnContext(wsCtx, "overlay refetch: record read failed (not retryable), leaving it to the poller",
			"workspace", job.Args.Workspace, "class", job.Args.IncumbentClass, "id", job.Args.ExternalID, "err", err)
		return nil
	}
	if err := w.ms.WithResolver(inc).WithFence().Ingest(wsCtx, rec); err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) {
			// Disconnected mid-refetch — the fence aborted the write, nothing
			// resurrected into a now-native workspace. Clean stop.
			return nil
		}
		return fmt.Errorf("overlay refetch: ingesting %s/%s: %w", job.Args.IncumbentClass, job.Args.ExternalID, err)
	}
	return nil
}

// overlayObjectClasses are the HubSpot object classes design.md §9 maps —
// the poller sweeps each, per due connection, resuming each object class's
// own persisted watermark. The five engagement classes
// (calls/meetings/emails/notes/tasks) are swept separately: HubSpot v3 has
// no generic engagements object, and each maps to its own activity kind
// (OVA-MAP-1).
var overlayObjectClasses = append([]string{
	overlay.IncumbentClassContacts, overlay.IncumbentClassCompanies,
	overlay.IncumbentClassDeals, overlay.IncumbentClassLeads,
}, overlay.IncumbentEngagementClasses()...)

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
	// The outcome-recording store is fenced too (WithFence): overlay_sync_state
	// is one of the tables teardown purges, so recording a backoff or success
	// against a workspace that disconnected before/after the sweep would
	// resurrect a purged row — the fence makes the recording abort with
	// ErrConnectionGone instead. A rate-limit/auth failure leaves the
	// connection row 'active' (only Disconnect revokes it), so the legitimate
	// backoff paths still record.
	recMS := w.ms.WithFence()
	for _, d := range due {
		wsCtx := reconcileWorkerCtx(ctx, d.Workspace)
		err := reconcileConnection(wsCtx, w.pool, w.vault, w.ms, w.meter, w.log, d, w.newIncumbent)
		// Record the sweep outcome so a connection-level failure backs the
		// next sweep off (overlay_sync_state), instead of re-sweeping a
		// revoked/rate-limited/unreachable connection hot every tick; one
		// clean sweep resets the backoff. Only the periodic poller schedules
		// backoff — the on-demand /overlay/reconcile handler returns its
		// error to the admin without touching the schedule.
		if errors.Is(err, overlay.ErrConnectionGone) {
			// The workspace was disconnected mid-sweep: every fenced write
			// aborted, so nothing was resurrected into the now-native
			// workspace. This is neither a failure to back off (the revoked
			// connection is already gone from the next due-scan) nor a success
			// to checkpoint — the overlay_sync_state row was purged by
			// teardown. Move on.
			w.log.DebugContext(wsCtx, "overlay reconcile: connection disconnected mid-sweep, stopping cleanly",
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
// credential and sweeps every overlayObjectClasses class for it —
// extracted so both the periodic fleet worker above (Work, one call per
// due connection, wrapped in its own synthesized system ctx) and
// overlay.go's on-demand overlayReconciler (the ReconcileOverlay
// handler, one call for the calling request's own workspace) drive the
// exact same sweep sequence rather than each keeping their own copy of
// it (the "fix the invariant, not the call site" rule: a future change
// to how a connection is swept — e.g. a second incumbent — must not risk
// updating one call site and missing the other). ctx is already scoped
// to d's own workspace and carries whatever actor/correlation the caller
// bound (a synthesized system principal for the periodic sweep, the
// calling admin's own principal for the on-demand one) — reconcileConnection
// itself makes no assumption about which. A per-object-class failure
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
	// Bind the store to THIS connection's live adapter so seeding,
	// UpsertUserMap's email re-verification, and Ingest's owner-change
	// revalidation all resolve against the incumbent's CURRENT owner
	// emails — the worker-level store carries only the read-path
	// placeholder resolver (compose/overlay.go), which cannot name an
	// owner.
	// WithFence engages the disconnect-race fence for the sweep's writes: if
	// this workspace is disconnected mid-sweep, every fenced write aborts
	// with overlay.ErrConnectionGone rather than resurrecting purged
	// incumbent-derived data into a now-native workspace (overlay's
	// disconnectfence.go). reconcileConnection and its callees treat that
	// signal as a clean stop.
	ms = ms.WithResolver(inc).WithFence()

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
		// A connection-level failure sweeping any class aborts the whole
		// sweep (the caller backs the connection off); a per-object failure
		// was already logged inside sweepObjectClass and skips only that
		// class, so the loop moves on.
		if err := sweepObjectClass(ctx, inc, ms, meter, log, d.Workspace.String(), objectClass); err != nil {
			return err
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
// stringified id, for logging only. Extracted from reconcileConnection so
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
func sweepObjectClass(ctx context.Context, inc overlay.Incumbent, ms *overlay.MirrorStore, meter *overlaybudget.Meter, log *slog.Logger, workspace, objectClass string) error {
	// Initial full load before the incremental sweep: Backfill lists the
	// object class id-cursor style AND fetches its associations (design.md
	// §4.4), checkpointing overlay_backfill_cursor so SyncStatus's
	// backfillComplete answers truthfully. It is a cheap no-op once its
	// cursor has converged, so every later sweep skips straight to the
	// Modified pass — the first sweep after a connect (via the poller, or
	// on-demand through POST /overlay/reconcile) does the load, the rest
	// ride the watermark.
	if err := overlay.Backfill(ctx, inc, ms, objectClass); err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) || isConnectionLevelIncumbentError(err) {
			return err
		}
		log.WarnContext(ctx, "overlay reconcile: backfill pass failed, skipping this object class this tick",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
	since, err := ms.LoadReconcileWatermark(ctx, objectClass)
	if err != nil {
		// A watermark read is a local DB call, not an incumbent one — a blip
		// here is not a connection-level failure, so skip this class rather
		// than back the whole connection off.
		log.WarnContext(ctx, "overlay reconcile: loading the persisted watermark failed, skipping this object class",
			"workspace", workspace, "object_class", objectClass, "err", err)
		return nil
	}
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
		if err := ms.SaveReconcileWatermark(ctx, objectClass, newWatermark); err != nil {
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
