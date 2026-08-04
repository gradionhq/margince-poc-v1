// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The embed drift sweep (ADR-0069 §3a, SEARCH-AC-13): the at-least-once
// bus loses embed events — a worker dies between ack and write — and the
// lost entities would otherwise sit invisible to semantic search until a
// human confirmed a reindex they never caused. Re-embedding them is the
// same spend class as the event lane that missed them, so the sweep heals
// them without a confirm. The binding-change case (configured identity ≠
// populated identity) is NOT this sweep's to touch — the store method
// no-ops there and the preview→confirm flow in embedreindextransport.go
// keeps its human consent.

// embedDriftSweepInterval paces the drift sweep. An empty pass is six
// indexed NOT-EXISTS probes per workspace, so a short cadence is cheap;
// fifteen minutes bounds how long a lost embed event keeps a record out
// of semantic search.
const embedDriftSweepInterval = 15 * time.Minute

// EmbedDriftSweepArgs is the periodic drift-sweep job (no payload — the
// sweep derives everything from the binding marker and the pending scan).
type EmbedDriftSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (EmbedDriftSweepArgs) Kind() string { return "embed_drift_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (EmbedDriftSweepArgs) FleetWide() {}

// embedDriftSweepWorker is the dispatcher: it enumerates the fleet and
// enqueues one sweep per workspace, and heals nothing itself.
type embedDriftSweepWorker struct {
	pool *pgxpool.Pool
}

func (w *embedDriftSweepWorker) Work(ctx context.Context, _ *river.Job[EmbedDriftSweepArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(EmbedDriftWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return EmbedDriftWorkspaceArgs{Workspace: ws} }))
}

// EmbedDriftWorkspaceArgs is one workspace's drift sweep.
type EmbedDriftWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (EmbedDriftWorkspaceArgs) Kind() string { return "embed_drift_workspace" }

// WorkspaceID binds this sweep to its tenant (jobs.WorkspaceScoped).
func (a EmbedDriftWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

type embedDriftWorkspaceWorker struct {
	store    *search.Store
	embedder search.Embedder
	log      *slog.Logger
}

func (w *embedDriftWorkspaceWorker) Work(ctx context.Context, job *river.Job[EmbedDriftWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	healed, err := w.store.SweepWorkspaceEmbeddingDrift(wsCtx, ids.From[ids.WorkspaceKind](job.Args.Workspace), w.embedder)
	if healed > 0 {
		w.log.InfoContext(wsCtx, "embed drift sweep healed entities", "healed", healed)
	}
	return jobs.FaultContext(ctx, err)
}

// addEmbedDriftSweepJob registers the sweep worker and its periodic tick
// (NewJobRunner appends what it returns — addGraphJobs' self-registration
// shape). Gated on a bound embed lane: with no embedder, or --ai-fake's
// empty identity, there is no store to heal and no marker seeded to read —
// the WithEmbedReindex posture. Run-on-start so a backlog accumulated
// while the worker was down is healed at boot, not one interval later.
func addEmbedDriftSweepJob(reg *jobRegistry, pool *pgxpool.Pool, embedder search.Embedder, log *slog.Logger) []*river.PeriodicJob {
	if embedder == nil {
		return nil
	}
	if identity, _ := embedder.EmbedIdentity(); identity == "" {
		return nil
	}
	addDeclaredWorker[EmbedDriftSweepArgs](reg, &embedDriftSweepWorker{pool: pool})
	addDeclaredWorker[EmbedDriftWorkspaceArgs](reg, &embedDriftWorkspaceWorker{store: search.NewStore(pool), embedder: embedder, log: log})
	return []*river.PeriodicJob{river.NewPeriodicJob(
		river.PeriodicInterval(embedDriftSweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return EmbedDriftSweepArgs{}, sweepInsertOpts() },
		&river.PeriodicJobOpts{RunOnStart: true},
	)}
}
