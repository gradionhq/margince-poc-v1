// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"log/slog"

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
// shape).
//
// The guard here is STRICTER than the declaration's when: [Embedder], and
// deliberately so. A configured embed lane with an empty identity — what
// --ai-fake leaves behind — seeds no binding marker, so there is nothing for
// the sweep to compare a row against and no store to heal; that is a property
// of the bound lane rather than of the configuration, so no config field can
// express it. periodicFor still resolves the cadence and the nil-embedder
// posture, which is what keeps this to ONE extra condition.
func addEmbedDriftSweepJob(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	embedder := cfg.Embedder
	if embedder == nil {
		return nil
	}
	if identity, _ := embedder.EmbedIdentity(); identity == "" {
		return nil
	}
	addDeclaredWorker[EmbedDriftSweepArgs](reg, &embedDriftSweepWorker{pool: pool})
	addDeclaredWorker[EmbedDriftWorkspaceArgs](reg, &embedDriftWorkspaceWorker{store: search.NewStore(pool), embedder: embedder, log: log})
	return periodicFor(cfg, EmbedDriftSweepArgs{})
}
