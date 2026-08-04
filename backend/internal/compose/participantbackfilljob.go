// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The interaction-participant backfill as a background pass (ADR-0078).
//
// Capture stamps participants for new mail, but every message already in the
// timeline predates ACT-DDL-3. Until those are recovered, "who on our team
// knows this contact" reads empty on exactly the workspaces that have the most
// history — which, to the person looking at the screen, is indistinguishable
// from a broken feature.
//
// It is a job and not an UPDATE inside migration 0157 because a migration
// holds its lock for its whole duration: a workspace with a real mailbox has
// hundreds of thousands of activity rows, and a slow backfill inside the
// migration turns a deploy into an outage.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ParticipantBackfillArgs is the periodic pass's (empty) job payload.
type ParticipantBackfillArgs struct{}

// Kind is the River job kind for the participant backfill.
func (ParticipantBackfillArgs) Kind() string { return "participant_backfill" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (ParticipantBackfillArgs) FleetWide() {}

// participantBackfillBatch is how many activities one statement attributes.
// Modest on purpose: the pass holds a write transaction for its duration and
// competes with live capture for the same rows, so a large batch buys
// throughput nobody is waiting for at the cost of lock time capture IS waiting
// on.
const participantBackfillBatch = 500

// participantBackfillBatchesPerTick bounds one Work invocation. A tick that
// drains 25 batches recovers 12,500 activities and then yields, so a
// long-history workspace finishes over a few passes instead of monopolizing a
// worker slot — and no single transaction grows long enough to matter.
const participantBackfillBatchesPerTick = 25

// participantBackfillWorker recovers participants for one workspace at a time.
type participantBackfillWorker struct {
	pool  *pgxpool.Pool
	store *activities.Store
	log   *slog.Logger
}

func newParticipantBackfillWorker(pool *pgxpool.Pool, log *slog.Logger) *participantBackfillWorker {
	return &participantBackfillWorker{pool: pool, store: activities.NewStore(pool), log: log}
}

// Work sweeps every live workspace. A per-workspace fault is logged and never
// aborts the pass — one workspace's bad row must not starve the rest — and the
// next tick simply re-selects whatever this one did not finish, because the
// pass carries no cursor to lose.
func (w *participantBackfillWorker) Work(ctx context.Context, _ *river.Job[ParticipantBackfillArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(ParticipantBackfillWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return ParticipantBackfillWorkspaceArgs{Workspace: ws} }))
}

// ParticipantBackfillWorkspaceArgs is one workspace's participant backfill.
type ParticipantBackfillWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (ParticipantBackfillWorkspaceArgs) Kind() string { return "participant_backfill_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a ParticipantBackfillWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// participantBackfillWorkspaceWorker runs one workspace's pass. It reuses the dispatcher's
// wiring rather than a second copy of it.
type participantBackfillWorkspaceWorker struct {
	*participantBackfillWorker
}

func (w *participantBackfillWorkspaceWorker) Work(ctx context.Context, job *river.Job[ParticipantBackfillWorkspaceArgs]) error {
	if _, err := workspaceJobCtx(ctx, job.Args); err != nil {
		return jobs.FaultContext(ctx, err)
	}
	recovered, err := w.backfillWorkspace(ctx, job.Args.Workspace)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if recovered > 0 {
		w.log.InfoContext(ctx, "participant backfill: recovered interaction participants",
			"workspace", job.Args.Workspace.String(), "rows", recovered)
	}
	return nil
}

// backfillWorkspace drains up to a tick's worth of batches, stopping early the
// moment a batch reports no work — which is what makes a caught-up
// installation cost one probe per pass rather than a full drain.
func (w *participantBackfillWorker) backfillWorkspace(ctx context.Context, ws ids.UUID) (int, error) {
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:participant_backfill",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	total := 0
	for i := 0; i < participantBackfillBatchesPerTick; i++ {
		n, err := w.store.BackfillParticipantsBatch(wsCtx, participantBackfillBatch)
		if err != nil {
			return total, err
		}
		if n == 0 {
			break
		}
		total += n
	}
	replayed, err := w.replayWorkspace(wsCtx)
	return total + replayed, err
}

// replayReplayBatch is smaller than the two-end batch because this pass does
// real work per row — it parses a stored RFC822 message or calendar resource
// in Go rather than running one join in the database.
const participantReplayBatch = 100

// replayWorkspace re-reads the stored originals of activities that already
// have their two ends recorded, recovering the CCs and meeting attendees.
//
// It runs after the two-end backfill and never instead of it: an activity with
// no participants at all is the worse gap, and settling that first means a
// workspace part-way through recovery still answers "who was in this" with
// something true.
func (w *participantBackfillWorker) replayWorkspace(wsCtx context.Context) (int, error) {
	total := 0
	for i := 0; i < participantBackfillBatchesPerTick; i++ {
		n, err := replayParticipantsBatch(wsCtx, w.pool, participantReplayBatch, w.log)
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
		total += n
	}
	return total, nil
}
