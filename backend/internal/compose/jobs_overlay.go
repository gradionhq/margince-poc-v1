// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
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

// overlayObjectClasses are the five HubSpot object classes design.md §9
// maps — the poller sweeps each, per due connection, resuming each
// object class's own persisted watermark.
var overlayObjectClasses = []string{
	overlay.IncumbentClassContacts, overlay.IncumbentClassCompanies, overlay.IncumbentClassDeals,
	overlay.IncumbentClassEngagements, overlay.IncumbentClassLeads,
}

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
	meter        *overlay.Meter
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
		if err := reconcileConnection(wsCtx, w.vault, w.ms, w.meter, w.log, d, w.newIncumbent); err != nil {
			w.log.WarnContext(wsCtx, "overlay reconcile: sweeping this workspace's connection failed",
				"workspace", d.Workspace.String(), "err", err)
		}
	}
	return enumErr
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
// is logged and skipped, never aborting the rest of the classes; only an
// unsupported incumbent or a failed vault resolution — both mean there is
// no adapter to sweep ANYTHING with — stop this connection's sweep
// entirely and return an error to the caller.
func reconcileConnection(ctx context.Context, vault keyvault.Vault, ms *overlay.MirrorStore, meter *overlay.Meter, log *slog.Logger, d overlay.DueOverlayConnection, newIncumbent func(region, token string) overlay.Incumbent) error {
	if d.Incumbent != "hubspot" {
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
	// Bind the store to THIS connection's live adapter so seeding,
	// UpsertUserMap's email re-verification, and Ingest's owner-change
	// revalidation all resolve against the incumbent's CURRENT owner
	// emails — the worker-level store carries only the read-path
	// placeholder resolver (compose/overlay.go), which cannot name an
	// owner.
	ms = ms.WithResolver(inc)

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
		log.WarnContext(ctx, "overlay reconcile: fetching the owners directory to seed mirror_user_map failed",
			"workspace", d.Workspace.String(), "err", err)
	} else if err := ms.SeedUserMap(ctx, d.Incumbent, owners); err != nil {
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
		log.WarnContext(ctx, "overlay reconcile: periodic email-mapping revalidation failed",
			"workspace", d.Workspace.String(), "err", err)
	}

	for _, objectClass := range overlayObjectClasses {
		// Initial full load before the incremental sweep: Backfill lists
		// the object class id-cursor style AND fetches its associations
		// (design.md §4.4), checkpointing overlay_backfill_cursor so
		// SyncStatus's backfillComplete answers truthfully. It is a cheap
		// no-op once its cursor has converged, so every later sweep skips
		// straight to the Modified pass below — the first sweep after a
		// connect (via the poller, or on-demand through POST
		// /overlay/reconcile) does the load, the rest ride the watermark.
		// A backfill failure is logged and skips only this class's sweep
		// this tick (the NEXT sweep resumes from the checkpoint), never
		// aborting the other classes.
		if err := overlay.Backfill(ctx, inc, ms, objectClass); err != nil {
			log.WarnContext(ctx, "overlay reconcile: backfill pass failed, skipping this object class this tick",
				"workspace", d.Workspace.String(), "object_class", objectClass, "err", err)
			continue
		}
		since, err := ms.LoadReconcileWatermark(ctx, objectClass)
		if err != nil {
			log.WarnContext(ctx, "overlay reconcile: loading the persisted watermark failed, skipping this object class",
				"workspace", d.Workspace.String(), "object_class", objectClass, "err", err)
			continue
		}
		newWatermark, err := overlay.Reconcile(ctx, inc, ms, meter, objectClass, since)
		if err != nil {
			log.WarnContext(ctx, "overlay reconcile sweep failed",
				"workspace", d.Workspace.String(), "object_class", objectClass, "err", err)
			continue
		}
		if newWatermark.After(since) {
			if err := ms.SaveReconcileWatermark(ctx, objectClass, newWatermark); err != nil {
				log.WarnContext(ctx, "overlay reconcile: persisting the new watermark failed",
					"workspace", d.Workspace.String(), "object_class", objectClass, "err", err)
			}
		}
	}
	return nil
}
