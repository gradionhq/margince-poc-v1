// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// This file owns the webhook-as-signal targeted re-fetch (OVA-WIRE-10): the
// job args, the worker, and its pre-flight/fetch-and-ingest split.
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
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
	wsCtx, conn, ok, err := w.resolveRefetchTarget(ctx, job)
	if err != nil || !ok {
		return err
	}
	return w.refetchAndIngest(wsCtx, conn, job)
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
	wsID, err := ids.ParseAs[ids.WorkspaceKind](job.Args.Workspace)
	if err != nil {
		w.log.ErrorContext(ctx, "overlay refetch: unparseable workspace id in job args",
			"workspace", job.Args.Workspace, "err", err)
		return nil, overlay.DueOverlayConnection{}, false, nil
	}
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
	if halted, err := overlay.NewWriteLedger(w.pool).Halted(wsCtx); err != nil {
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
	// WithFenceIdentity on conn's OWN connected_at: a signal that outlives a
	// disconnect+reconnect (coalesced 5s ahead, OVA-PARAM-10) must not ingest
	// under the connection it was enqueued for once a NEW one is live.
	if err := w.ms.WithResolver(inc).WithFenceIdentity(conn.ConnectedAt).Ingest(wsCtx, rec); err != nil {
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
