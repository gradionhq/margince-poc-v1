// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The captured-organization auto-enrich sweep (CAP-PARAM-7, ADR-0072/A118):
// a leader-elected periodic pass (run-on-start + daily) that gives every
// surviving auto-created company a governed web dossier. Per workspace, when
// the capture_auto_enrich flag is on, it enqueues a deep read
// (system:capture_auto_enrich, auto-applied on completion) for each due
// domain-named org — newest first, under an atomically-reserved daily cap. It
// is the ONE trigger and the self-healing reconciler in one: a missed org is
// simply picked up next pass. The deep-read worker's auto-apply lane
// (deepread.go) records the terminal outcome on the sweep's cursor.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// autoEnrichDailyCap is the per-workspace ceiling on auto deep reads started in
// one UTC day. Reserved atomically, so two replicas never both slip past it.
//
// It is the THIRD bound on this fan-out, not the only one, and knowing what the
// other two already do is what sets the number:
//
//   - Concurrency is bounded by deepReadMaxWorkers (2): a burst never occupies
//     more than two workers however long the queue is.
//   - Money is bounded by the ADR-0020 AI budget: background model calls defer
//     to the next window at the monthly cap, whatever this counter says.
//   - And a read only ever happens for a company the workspace CREATED, which
//     the tiered gate allows only for an address the owner has corresponded with
//     or already has a person for. A stranger's mail defers to the ledger and
//     mints nothing, so an outsider cannot aim this at a domain of their choosing.
//
// What is left for this counter is PACING, and 500 is sized for the moment that
// decides whether the product is believed: a first backfill mints hundreds of
// companies at once, and what it is demonstrating is that the CRM fills itself
// (P5). A ceiling below that arrival rate turns the demonstration into a trickle
// and teaches the opposite, while a ceiling above it buys nothing the three
// bounds above do not already give.
const autoEnrichDailyCap = 500

// autoEnrichRetryBackoff is how long a triggered read's cursor is armed before
// the sweep may reconsider the org: long enough that an in-flight or
// just-failed read is not re-driven prematurely (ADR-0072: 7 days).
const autoEnrichRetryBackoff = 7 * 24 * time.Hour

// CaptureAutoEnrichSweepArgs is the periodic sweep's (empty) job payload.
type CaptureAutoEnrichSweepArgs struct{}

// Kind is the River job kind for the auto-enrich sweep.
func (CaptureAutoEnrichSweepArgs) Kind() string { return "capture_auto_enrich_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (CaptureAutoEnrichSweepArgs) FleetWide() {}

// captureAutoEnrichSweepWorker runs one sweep pass across every workspace.
type captureAutoEnrichSweepWorker struct {
	pool       *pgxpool.Pool
	people     *people.Store
	settings   *capture.SettingsStore
	autoEnrich *capture.AutoEnrichStore
	dailyCap   int
	log        *slog.Logger
}

// newCaptureAutoEnrichSweepWorker builds the sweep worker over the pool.
func newCaptureAutoEnrichSweepWorker(pool *pgxpool.Pool, log *slog.Logger) *captureAutoEnrichSweepWorker {
	return &captureAutoEnrichSweepWorker{
		pool:       pool,
		people:     people.NewStore(pool),
		settings:   capture.NewSettings(pool),
		autoEnrich: capture.NewAutoEnrichStore(pool),
		dailyCap:   autoEnrichDailyCap,
		log:        log,
	}
}

// Work is the DISPATCHER: it enumerates the fleet and enqueues one sweep per
// workspace, and touches no tenant data itself.
func (w *captureAutoEnrichSweepWorker) Work(ctx context.Context, _ *river.Job[CaptureAutoEnrichSweepArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(CaptureAutoEnrichWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return CaptureAutoEnrichWorkspaceArgs{Workspace: ws} }))
}

// CaptureAutoEnrichWorkspaceArgs is one workspace's auto-enrich pass.
type CaptureAutoEnrichWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureAutoEnrichWorkspaceArgs) Kind() string { return "capture_auto_enrich_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a CaptureAutoEnrichWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// captureAutoEnrichWorkspaceWorker runs one workspace's pass. It reuses the
// dispatcher's wiring rather than a second copy: the stores, the daily cap and
// the actor are the pass's, not the fan-out's.
type captureAutoEnrichWorkspaceWorker struct {
	sweeper *captureAutoEnrichSweepWorker
}

func (w *captureAutoEnrichWorkspaceWorker) Work(ctx context.Context, job *river.Job[CaptureAutoEnrichWorkspaceArgs]) error {
	if _, err := workspaceJobCtx(ctx, job.Args); err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.sweeper.sweepWorkspace(ctx, ids.From[ids.WorkspaceKind](job.Args.Workspace)))
}

// sweepWorkspace enriches the due orgs of one workspace, respecting the flag
// and the daily cap. The flag is re-read at the top of every pass, so toggling
// it off stops new reads on the next sweep even for already-queued work.
func (w *captureAutoEnrichSweepWorker) sweepWorkspace(ctx context.Context, ws ids.WorkspaceID) error {
	wsCtx := w.workspaceCtx(ctx, ws)
	settings, err := w.settings.Get(wsCtx)
	if err != nil {
		return err
	}
	// The open domain questions are swept whatever the setting says. With
	// crawling off the worker settles each one from what the workspace already
	// knows; skipping them would leave every person captured under that setting
	// without a company, permanently.
	if err := w.sweepDomainTriage(wsCtx, w.dailyCap); err != nil {
		return err
	}
	if !settings.AutoEnrich {
		return nil
	}
	// Retire any cursors that have used every attempt, so they leave the due
	// index instead of being re-scanned every pass (the real 'exhausted' state).
	if err := w.autoEnrich.ExpireExhausted(wsCtx); err != nil {
		return err
	}
	due, err := w.autoEnrich.ListDueOrgs(wsCtx, w.dailyCap)
	if err != nil {
		return err
	}
	for _, org := range due {
		slot, err := w.autoEnrich.ReserveBudget(wsCtx, w.dailyCap)
		if err != nil {
			return err
		}
		if !slot.Reserved {
			// The day's cap is spent — stop; the rest wait for tomorrow's pass.
			return nil
		}
		if err := w.triggerEnrich(wsCtx, org, slot); err != nil {
			// A single org's trigger fault must not consume the pass; log it
			// and move on. The cursor stays due (nothing was queued), so the
			// next pass retries, and the slot it reserved has already gone back.
			w.log.WarnContext(wsCtx, "capture auto-enrich: trigger failed",
				"org", org.OrganizationID.String(), "err", err)
			continue
		}
	}
	return nil
}

// triggerEnrich starts a system-requested deep read for one org and arms its
// cursor, returning the reserved slot when no read came of it.
func (w *captureAutoEnrichSweepWorker) triggerEnrich(ctx context.Context, org capture.DueOrg, slot capture.BudgetSlot) error {
	return startEnrichOrRefund(ctx, w.people, w.autoEnrich, org.OrganizationID, org.Domain, slot)
}

// refundTimeout bounds the compensating write. Short: it is one indexed UPDATE,
// and a refund that hangs would hold the pass or the capture it runs on.
const refundTimeout = 5 * time.Second

// startEnrichOrRefund is the whole rule about a reserved slot, in one place
// because both callers own the same invariant: a slot buys a read or it goes
// back.
//
// The ordering is load-bearing. `started` is checked BEFORE `err`, because
// startAutoEnrichRead reports (true, err) for a read that was queued and whose
// cursor could not then be armed — refunding there would pay for that crawl
// twice, returning the slot to the day while the read still runs.
//
// The refund detaches from the caller's context, for the case that matters most:
// when the start failed BECAUSE that context was cancelled, the refund is owed
// and the context it would run on is already dead. Compensating on the dying
// context leaks the slot exactly when it is most likely to leak. Same
// detach-and-deadline shape the connector teardown and the AI tracer use.
func startEnrichOrRefund(ctx context.Context, peopleStore *people.Store,
	autoEnrich *capture.AutoEnrichStore, orgID ids.OrganizationID, domain string, slot capture.BudgetSlot,
) error {
	started, err := startAutoEnrichRead(ctx, peopleStore, autoEnrich, orgID, domain)
	if started {
		return err
	}
	refundCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundTimeout)
	defer cancel()
	refundErr := autoEnrich.ReleaseBudget(refundCtx, slot)
	if err != nil {
		return errors.Join(err, refundErr)
	}
	return refundErr
}

// startAutoEnrichRead queues ONE governed deep read for one organization and
// arms its sweep cursor. Shared by the periodic sweep and the on-capture
// trigger, because they differ only in what made the organization interesting —
// the read they ask for, the ceiling it runs under, the principal it is
// attributed to, and the cursor that stops it being asked for twice must all be
// the one spelling.
//
// The dossier and the River job are one transaction (StartSiteReadQueued +
// InsertTx), so a crash can never leave a dossier without its job; the in-flight
// uniqueness index dedupes a concurrent start.
//
// It does NOT crawl. This queues work and returns — the pages are fetched and
// the model is called in the deep-read worker, on its own job.
//
// It reports whether a read was actually STARTED, as opposed to joined onto one
// already in flight. Both callers reserve a budget slot before calling, and a
// join means that slot bought nothing: without the distinction, a sweep and a
// capture racing on one organization spend two of the day's reads on a single
// crawl and charge that organization two of its bounded attempts. The
// cursor is armed only by the caller that started something, for the same
// reason.
func startAutoEnrichRead(ctx context.Context, peopleStore *people.Store,
	autoEnrich *capture.AutoEnrichStore, orgID ids.OrganizationID, domain string,
) (bool, error) {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return false, err
	}
	seedURL := "https://" + domain
	_, joined, err := peopleStore.StartSiteReadQueued(ctx, orgID, seedURL, systemAutoEnrichActor,
		func(ctx context.Context, tx pgx.Tx, read people.SiteRead) error {
			_, insErr := client.InsertTx(ctx, tx, SiteDeepReadArgs{
				Workspace:      storekit.MustWorkspace(ctx),
				OrganizationID: orgID.UUID,
				SiteReadID:     read.ID,
				SeedURL:        read.SeedURL,
				RequestedBy:    read.RequestedBy,
				// Declared in the payload as well as enforced at the worker: a
				// job carries what it was queued to cost, so an operator
				// reading river_job sees the ceiling without inferring it.
				MaxPages: autoEnrichMaxPages,
			}, siteDeepReadInsertOpts())
			return insErr
		})
	if err != nil {
		return false, err
	}
	if joined {
		// Someone else's read is already in flight for this organization; the
		// uniqueness index arbitrated it. Arming the cursor again would charge a
		// second attempt for one crawl.
		return false, nil
	}
	return true, autoEnrich.MarkQueued(ctx, orgID, autoEnrichRetryBackoff)
}

// workspaceCtx binds the sweep's system principal on the given workspace. A
// PrincipalSystem is unbounded (auth.Unbounded), so it passes the
// organization-update/visibility gates StartSiteReadQueued and the settings read
// enforce, without impersonating any human.
func (w *captureAutoEnrichSweepWorker) workspaceCtx(ctx context.Context, ws ids.WorkspaceID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws.UUID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: systemAutoEnrichActor,
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
