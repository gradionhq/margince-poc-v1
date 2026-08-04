// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the deals module's two scheduled passes, alongside the
// per-module job files this package already keeps (jobs_capture.go,
// jobs_overlay.go). The adapters are the only code that knows about River;
// the deals correctors stay the River-agnostic seam.
//
// Each pass is a DISPATCHER plus a workspace worker: the dispatcher enumerates
// the fleet and enqueues one job per tenant, and the worker runs that tenant's
// pass. The provenance each worker binds is the provenance the old inline loop
// bound — the actor and the correlation id moved here verbatim, because they
// are what the audit rows this pass writes are recorded against.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// CloseDateSweepArgs schedules one close-date hygiene pass (INV-CLOSE-PAST).
type CloseDateSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CloseDateSweepArgs) Kind() string { return "close_date_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (CloseDateSweepArgs) FleetWide() {}

// CloseDateWorkspaceArgs is one workspace's close-date hygiene pass.
type CloseDateWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (CloseDateWorkspaceArgs) Kind() string { return "close_date_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a CloseDateWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// FollowUpReconcileArgs schedules one overnight follow-up reconciliation
// pass (features/07 §8a).
type FollowUpReconcileArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (FollowUpReconcileArgs) Kind() string { return "follow_up_reconcile" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (FollowUpReconcileArgs) FleetWide() {}

// FollowUpWorkspaceArgs is one workspace's follow-up reconciliation pass.
type FollowUpWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (FollowUpWorkspaceArgs) Kind() string { return "follow_up_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a FollowUpWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// dealsSweepMaxAttempts is larger than the shared default because both deals
// passes tick DAILY: a workspace that failed a transient blip would otherwise
// wait a full day for its next chance, and River's ladder spans minutes at
// these rungs — comfortably inside one tick.
const dealsSweepMaxAttempts = 5

// closeDateSweepWorker is the dispatcher: it enumerates and enqueues, and
// touches no tenant data itself.
type closeDateSweepWorker struct {
	pool *pgxpool.Pool
}

func (w *closeDateSweepWorker) Work(ctx context.Context, _ *river.Job[CloseDateSweepArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(river.QueueDefault, dealsSweepMaxAttempts),
		func(ws ids.UUID) river.JobArgs { return CloseDateWorkspaceArgs{Workspace: ws} }))
}

// closeDateWorkspaceWorker runs one workspace's pass.
type closeDateWorkspaceWorker struct {
	corrector *deals.CloseDateCorrector
}

func (w *closeDateWorkspaceWorker) Work(ctx context.Context, job *river.Job[CloseDateWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:close-date"})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	return jobs.FaultContext(ctx, w.corrector.SweepWorkspace(wsCtx))
}

// followUpReconcileWorker is the dispatcher for the overnight pass.
type followUpReconcileWorker struct {
	pool *pgxpool.Pool
}

func (w *followUpReconcileWorker) Work(ctx context.Context, _ *river.Job[FollowUpReconcileArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(river.QueueDefault, dealsSweepMaxAttempts),
		func(ws ids.UUID) river.JobArgs { return FollowUpWorkspaceArgs{Workspace: ws} }))
}

// followUpWorkspaceWorker runs one workspace's overnight pass.
type followUpWorkspaceWorker struct {
	reconciler *deals.FollowUpReconciler
}

func (w *followUpWorkspaceWorker) Work(ctx context.Context, job *river.Job[FollowUpWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// The overnight agent is the acting principal — its writes and stagings
	// carry agent:overnight provenance (features/07 §8a), and every read is
	// workspace-bound by RLS through the GUC the binding above set.
	wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "agent:overnight"})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
	return jobs.FaultContext(ctx, w.reconciler.ReconcileWorkspace(wsCtx))
}
