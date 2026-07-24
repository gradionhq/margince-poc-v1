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
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// autoEnrichDailyCap is the per-workspace ceiling on auto deep reads the sweep
// starts in one UTC day (ADR-0072: N=10) — the ADR-0020 budget guardrail for
// the fan-out. Reserved atomically so two replicas never both slip past it.
const autoEnrichDailyCap = 10

// autoEnrichRetryBackoff is how long a triggered read's cursor is armed before
// the sweep may reconsider the org: long enough that an in-flight or
// just-failed read is not re-driven prematurely (ADR-0072: 7 days).
const autoEnrichRetryBackoff = 7 * 24 * time.Hour

// CaptureAutoEnrichSweepArgs is the periodic sweep's (empty) job payload.
type CaptureAutoEnrichSweepArgs struct{}

// Kind is the River job kind for the auto-enrich sweep.
func (CaptureAutoEnrichSweepArgs) Kind() string { return "capture_auto_enrich_sweep" }

// captureAutoEnrichSweepWorker runs one sweep pass across every workspace.
type captureAutoEnrichSweepWorker struct {
	river.WorkerDefaults[CaptureAutoEnrichSweepArgs]
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

// Work sweeps every live workspace. Per-workspace faults are logged and never
// abort the pass — one workspace's bad row must not starve the rest.
func (w *captureAutoEnrichSweepWorker) Work(ctx context.Context, _ *river.Job[CaptureAutoEnrichSweepArgs]) error {
	rows, err := w.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	var workspaces []ids.WorkspaceID
	for rows.Next() {
		var id ids.WorkspaceID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workspaces = append(workspaces, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ws := range workspaces {
		if err := w.sweepWorkspace(ctx, ws); err != nil {
			w.log.WarnContext(ctx, "capture auto-enrich: workspace sweep failed",
				"workspace", ws.String(), "err", err)
		}
	}
	return nil
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
	if !settings.AutoEnrich {
		return nil
	}
	due, err := w.autoEnrich.ListDueOrgs(wsCtx, w.dailyCap)
	if err != nil {
		return err
	}
	for _, org := range due {
		reserved, err := w.autoEnrich.ReserveBudget(wsCtx, w.dailyCap)
		if err != nil {
			return err
		}
		if !reserved {
			// The day's cap is spent — stop; the rest wait for tomorrow's pass.
			return nil
		}
		if err := w.triggerEnrich(wsCtx, org); err != nil {
			// A single org's trigger fault must not consume the pass; log it
			// and move on. The cursor stays due (nothing was queued), so the
			// next pass retries — but the reserved budget slot is spent, a
			// conservative under-spend, never an over-spend.
			w.log.WarnContext(wsCtx, "capture auto-enrich: trigger failed",
				"org", org.OrganizationID.String(), "err", err)
			continue
		}
	}
	return nil
}

// triggerEnrich starts a system-requested deep read for one org and arms its
// cursor. The dossier and the River job are one transaction (StartSiteReadQueued
// + InsertTx), so a crash can never leave a dossier without its job; the
// in-flight uniqueness index dedupes a concurrent start.
func (w *captureAutoEnrichSweepWorker) triggerEnrich(ctx context.Context, org capture.DueOrg) error {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	seedURL := "https://" + org.Domain
	_, _, err = w.people.StartSiteReadQueued(ctx, org.OrganizationID, seedURL, systemAutoEnrichActor,
		func(ctx context.Context, tx pgx.Tx, read people.SiteRead) error {
			_, insErr := client.InsertTx(ctx, tx, SiteDeepReadArgs{
				WorkspaceID:    storekit.MustWorkspace(ctx),
				OrganizationID: org.OrganizationID.UUID,
				SiteReadID:     read.ID,
				SeedURL:        read.SeedURL,
				RequestedBy:    read.RequestedBy,
			}, siteDeepReadInsertOpts())
			return insErr
		})
	if err != nil {
		return err
	}
	return w.autoEnrich.MarkQueued(ctx, org.OrganizationID, autoEnrichRetryBackoff)
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
