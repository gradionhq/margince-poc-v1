// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// This file owns the targeted single-record re-fetch (OVA-WIRE-10) both of its
// producers enqueue — a webhook signal and the sweep's re-projection phase:
// the job args, the worker, and its pre-flight/fetch-and-ingest split.
// reconcileWorkerCtx and isConnectionLevelIncumbentError stay in
// jobs_overlay.go, which owns the periodic reconcile sweep: both are shared
// with it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// OverlayRefetchArgs is a targeted single-record re-fetch, enqueued by two
// producers: a validly-signed, portal-bound webhook (webhook-as-signal,
// OVA-WIRE-10) and the sweep's re-projection phase (jobs_overlay_sweep.go),
// which names the rows an older mapping declaration projected. Both refresh
// the named record through the same idempotent ingest the poller uses. The args
// ARE the coalescing key — River's unique-by-args (OVA-PARAM-10, scheduled a
// short window ahead) collapses a record edited rapidly in the incumbent to
// ONE re-fetch rather than N. IncumbentClass is the HubSpot object class the
// record is read back under — any of the four record classes
// (contacts/companies/deals/leads) or the five engagement classes
// (calls/meetings/emails/notes/tasks); ExternalID is the mirror external id,
// which for an engagement carries its own class namespace ("calls:123",
// OVA-MAP-7) and is bare otherwise.
type OverlayRefetchArgs struct {
	Workspace      ids.UUID `json:"workspace_id"`
	IncumbentClass string   `json:"incumbent_class"`
	ExternalID     string   `json:"external_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (OverlayRefetchArgs) Kind() string { return "overlay_refetch" }

// WorkspaceID binds this mirror re-fetch to its tenant (jobs.WorkspaceScoped).
func (a OverlayRefetchArgs) WorkspaceID() ids.UUID { return a.Workspace }

// overlayRefetchWorker executes one targeted single-record re-fetch: it
// resolves the workspace's active connection, builds a live incumbent adapter
// over its vaulted token, reads the one record, and ingests it through the
// fenced, resolver-bound store — the SAME idempotent, owner-revalidating path
// the reconcile sweep uses, so a webhook refresh and a poller sweep converge
// on one mirror state. A dropped job is not a lost record either way: a signal
// this lane drops is healed by the poller's next watermark pass, and a
// re-projection it drops is named again by the next sweep's re-projection
// phase, since the row keeps the declaration it was projected under — with one
// exception, dropFailedRead's: a record this build's declaration cannot project
// records the declaration it could not reach and is spared until that
// declaration changes, because re-reading it would spend an incumbent call on
// an answer nothing but a new declaration can change.
type overlayRefetchWorker struct {
	pool  *pgxpool.Pool
	vault keyvault.Vault
	// meter is the OVB budget. A targeted re-fetch is a live single-record REST
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
	wsCtx, conn, ok, err := w.resolveRefetchTarget(ctx, job)
	if err != nil || !ok {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.refetchAndIngest(wsCtx, conn, job))
}

// resolveRefetchTarget resolves the job's workspace-scoped context and
// active connection, and applies every pre-flight check that makes the
// read+ingest below either safe or a clean no-op: an unparseable workspace
// id (a permanent defect, never retried), a workspace that has since
// disconnected, an incumbent other than HubSpot, and a mirror halted by a
// write-ledger value-hash collision (OVA-AC-3) — re-checked here, at
// execution, so a re-fetch enqueued before the halt (coalesced 5s ahead)
// never still runs. ok=false means Work should return err as-is (nil for a
// clean stop, non-nil for a retryable failure) without reaching the
// fetch/ingest step.
func (w *overlayRefetchWorker) resolveRefetchTarget(ctx context.Context, job *river.Job[OverlayRefetchArgs]) (wsCtx context.Context, conn overlay.DueOverlayConnection, ok bool, err error) {
	if _, bindErr := workspaceJobCtx(ctx, job.Args); bindErr != nil {
		// CANCELLED, not completed and not retried. Args that name no workspace
		// are a permanent defect — three attempts change nothing — but
		// returning nil would record a green row over a re-fetch that never
		// happened, which is the shape the binding guard exists to make loud.
		// A cancel is the one disposition that is both terminal and visible.
		return nil, overlay.DueOverlayConnection{}, false, river.JobCancel(bindErr)
	}
	wsID := ids.From[ids.WorkspaceKind](job.Args.Workspace)
	wsCtx = reconcileWorkerCtx(ctx, wsID)
	conn, err = overlay.ActiveConnection(wsCtx, w.pool)
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			// The workspace disconnected since the signal arrived — nothing to
			// refresh, and teardown owns the mirror. Not a retryable failure.
			return nil, overlay.DueOverlayConnection{}, false, nil
		}
		return nil, overlay.DueOverlayConnection{}, false, fmt.Errorf("overlay refetch: reading the active connection: %w", err)
	}
	if conn.Incumbent != incumbentHubSpot {
		return nil, overlay.DueOverlayConnection{}, false, nil
	}
	if halted, err := overlay.NewWriteLedger(database.BindTo(w.pool, ids.From[ids.WorkspaceKind](job.Args.Workspace))).Halted(wsCtx); err != nil {
		return nil, overlay.DueOverlayConnection{}, false, fmt.Errorf("overlay refetch: reading the mirror-halt flag: %w", err)
	} else if halted {
		w.log.WarnContext(wsCtx, "overlay refetch: mirror is halted (ledger collision), skipping",
			"workspace", job.Args.Workspace, "class", job.Args.IncumbentClass, "id", job.Args.ExternalID)
		return nil, overlay.DueOverlayConnection{}, false, nil
	}
	return wsCtx, conn, true, nil
}

// refetchAndIngest resolves conn's vaulted token, builds a live incumbent
// adapter, reserves the incumbent budget, reads the one record, and ingests
// it through the fenced, resolver-bound store.
func (w *overlayRefetchWorker) refetchAndIngest(wsCtx context.Context, conn overlay.DueOverlayConnection, job *river.Job[OverlayRefetchArgs]) error {
	token, err := w.vault.Get(wsCtx, conn.Workspace, conn.CredentialRef)
	if err != nil {
		return fmt.Errorf("overlay refetch: resolving the vaulted token: %w", err)
	}
	inc := w.newIncumbent(conn.Region, string(token))
	// Reserve one REST unit BEFORE the live read (OVB-AC-2/AC-5), so this lane's
	// incumbent calls are accounted for like every other. On shed skip the
	// re-fetch: never spend live quota we cannot account for. Dropping it costs
	// nothing durable either way, though for different reasons per producer — a
	// webhook is an optimization the watermark poller heals within its interval,
	// while a re-projection is NOT something the poller heals (it is
	// watermark-driven and never re-reads a record the incumbent has not
	// touched); the row simply stays in the stale set until a re-fetch lands, so
	// the next re-projection pass names it again.
	// A role wired without a configured meter gets the fail-closed placeholder
	// (nil Redis client) here, which sheds every reservation — so an
	// unaccountable read is skipped, never made. A meter error is transient —
	// retry.
	if allowed, err := w.meter.ReserveREST(wsCtx, conn.Incumbent, overlaybudget.SourceForceFresh, 1); err != nil {
		return fmt.Errorf("overlay refetch: reserving the incumbent budget: %w", err)
	} else if !allowed {
		w.log.InfoContext(wsCtx, "overlay refetch: incumbent budget shed, skipping the live read",
			"workspace", job.Args.Workspace, "class", job.Args.IncumbentClass, "id", job.Args.ExternalID)
		return nil
	}
	// Built for the workspace THIS re-fetch names: the mirror store writes
	// tenant rows over a workspace-bound handle, so one instance cannot serve
	// the workspaces a role re-fetches for (ADR-0091 §9 step 3).
	ms := overlay.NewMirrorStore(database.BindTo(w.pool, conn.Workspace), unresolvedOwnerEmails{})
	rec, err := inc.Get(wsCtx, job.Args.IncumbentClass, job.Args.ExternalID)
	if err != nil {
		// A connection-level failure (rate-limit/auth/unreachable) is retryable
		// — return it so River backs off and retries. Every other read failure
		// leaves the mirror holding what it already held, and dropping the job
		// costs nothing durable: the row stays in the stale set and the next
		// re-projection pass names it again.
		if isConnectionLevelIncumbentError(err) {
			return fmt.Errorf("overlay refetch: reading %s/%s: %w", job.Args.IncumbentClass, job.Args.ExternalID, err)
		}
		w.dropFailedRead(wsCtx, ms, job.Args, err)
		return nil
	}
	// WithFenceIdentity on conn's OWN connected_at: a signal that outlives a
	// disconnect+reconnect (coalesced 5s ahead, OVA-PARAM-10) must not ingest
	// under the connection it was enqueued for once a NEW one is live.
	if err := ms.WithResolver(inc).WithFenceIdentity(conn.ConnectedAt).Ingest(wsCtx, rec); err != nil {
		if errors.Is(err, overlay.ErrConnectionGone) {
			// Disconnected (or disconnected+reconnected) mid-refetch — the
			// fence aborted the write, nothing resurrected or misattributed.
			// Clean stop.
			return nil
		}
		return fmt.Errorf("overlay refetch: ingesting %s/%s: %w", job.Args.IncumbentClass, job.Args.ExternalID, err)
	}
	return nil
}

// dropFailedRead disposes of a single-record read that failed for a reason
// other than connection health, and decides the one thing that disposal can
// get wrong: whether to record the declaration the re-projection could not
// reach. A record RETIRES the row from the re-projection sweep until this
// class's declaration changes, so it may only be written when the answer is
// fixed for as long as that declaration is — hubspot.ErrUnmappable, the read
// that came back whole and could not be projected.
//
// Everything else is left unrecorded on purpose, because the same read can
// come back differently: HubSpot answers a partial batch 207 MULTI_STATUS,
// which is a success, so an empty result is as often one object momentarily
// withheld as an absent record, and a 409 is a passing state conflict.
// Recording on those would retire a row the incumbent would have served on the
// next tick, and retire it invisibly — the row keeps counting stale, so the
// flip stays blocked while nothing is left retrying it. Leaving them
// unrecorded costs one re-fetch per sweep tick and self-heals.
//
// An archived record (apperrors.ErrNotFound) is terminal too, but recording it
// would be moot: the deletion feed purges the row, and a record on a row that
// is about to be deleted spares nothing.
//
// A class this build declares no mapping for is settled before either of those
// questions: nothing can project such a record, so there is no declaration to
// record and none to record it against, whatever the read failure was.
func (w *overlayRefetchWorker) dropFailedRead(ctx context.Context, ms *overlay.MirrorStore, args OverlayRefetchArgs, readErr error) {
	// The declaration is resolved for EVERY failed read, not only the one that
	// records, because a class this build declares no mapping for is the case
	// where there is no declaration to record and none to record it against,
	// whatever the read failure was. A job naming such a class is not exotic:
	// river_job outlives a deploy, so a re-fetch enqueued under a mapping a
	// later build retires arrives here, and the read that brought it here failed
	// at the adapter's own declaration lookup rather than at the incumbent.
	// Reported rather than dropped silently, for the reason the sweep's own
	// lookup reports (jobs_overlay_sweep.go): the classes the webhook lane
	// recognises and the declared mappings are separate lists, and a row nothing
	// re-fetches and nothing reports is invisible.
	m, declared := hubspot.Mapping(args.IncumbentClass)
	if !declared {
		w.log.WarnContext(ctx, "overlay refetch: no mapping declaration for this object class, so nothing can project this record and there is no declaration a re-projection could have failed to reach",
			"workspace", args.Workspace, "class", args.IncumbentClass, "id", args.ExternalID, "err", readErr)
		return
	}
	if !errors.Is(readErr, hubspot.ErrUnmappable) {
		w.log.WarnContext(ctx, "overlay refetch: the incumbent did not hand back this record; the mirror keeps what it holds and the next re-projection pass names the row again",
			"workspace", args.Workspace, "class", args.IncumbentClass, "id", args.ExternalID, "err", readErr)
		return
	}
	// The mirror is keyed by the CANONICAL class the declaration projects onto,
	// never the incumbent's own name for it, and the fingerprint recorded is
	// the one StaleProjections derives its comparison from — so the record and
	// the skip agree by construction.
	fingerprint := overlay.Fingerprint(m)
	// Bookkeeping, not the job's purpose: this read has already failed in a way
	// no retry changes, and failing the job to report that the note could not be
	// written would turn a bounded waste into a retried one.
	if recErr := ms.RecordReprojectionFailure(ctx, m.Target, args.ExternalID, fingerprint); recErr != nil {
		w.log.WarnContext(ctx, "overlay refetch: recording the re-projection failure failed; the row stays in the stale set and the next pass names it again",
			"workspace", args.Workspace, "class", args.IncumbentClass, "id", args.ExternalID,
			"declaration", fingerprint, "read_err", readErr, "err", recErr)
		return
	}
	// The only trace this outcome leaves. The row is not re-fetched again and
	// the recorded declaration has no read surface, so a line that reported
	// only the failure would leave an operator watching a blocked flip with
	// nothing converging and nothing saying why. Counting these rows on the
	// flip preflight and the sync status needs a contract field and is #1221.
	w.log.WarnContext(ctx, "overlay refetch: this build's declaration cannot project the record the incumbent holds; "+
		"the row keeps the projection it has and is not re-fetched again until this class's declaration changes, "+
		"and it keeps counting stale meanwhile, so the flip stays blocked until a build ships a repaired declaration for this class",
		"workspace", args.Workspace, "class", args.IncumbentClass, "id", args.ExternalID,
		"declaration", fingerprint, "err", readErr)
}
