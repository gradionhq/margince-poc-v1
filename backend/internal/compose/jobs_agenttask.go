// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//nolint:dupl // per-kind River wiring; see below for why the shape cannot be factored out
package compose

// River wiring for the MCP task-retention pass: a dispatcher over EVERY
// workspace and a worker that purges one. It sits beside the other per-concern
// job files rather than in jobs.go, which owns the runner's assembly.
//
// It reads almost exactly like jobs_retention.go, and that similarity cannot be
// factored away — hence the file-wide waiver above. addDeclaredWorker is generic
// over the CLOSED set of declared args types, so each kind needs its own
// concrete pair for the registration to compile at all; a shared helper would
// have to be generic over both the args type and its sweeper, and would then be
// one indirection wrapping two lines. It is why every jobs_*.go file in this
// package repeats the shape.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// AgentTaskRetentionArgs schedules one purge of MCP tasks past their expiry.
// Always-on: a completed task stores a verbatim record read-back, so retaining
// it past the poll it exists to answer is subject data kept for no purpose.
type AgentTaskRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (AgentTaskRetentionArgs) Kind() string { return "agent_task_retention" }

// FleetWide marks this a dispatcher: it enumerates and enqueues, and does no
// tenant work of its own (jobs.FleetWide).
func (AgentTaskRetentionArgs) FleetWide() {}

// agentTaskRetentionWorker is the dispatcher. It enumerates EVERY workspace,
// archived ones included: archiving does not un-store the results inside one,
// and agent_task.workspace_id is ON DELETE RESTRICT, so leftovers would also
// refuse the eventual hard delete.
type agentTaskRetentionWorker struct {
	pool *pgxpool.Pool
}

func (w *agentTaskRetentionWorker) Work(ctx context.Context, _ *river.Job[AgentTaskRetentionArgs]) error {
	workspaces, err := enumerateEveryWorkspace(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx),
		workspaceSweepOpts(AgentTaskRetentionWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return AgentTaskRetentionWorkspaceArgs{Workspace: ws} }))
}

// AgentTaskRetentionWorkspaceArgs purges one workspace's expired tasks.
type AgentTaskRetentionWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (AgentTaskRetentionWorkspaceArgs) Kind() string { return "agent_task_retention_workspace" }

// WorkspaceID binds this purge to its tenant (jobs.WorkspaceScoped).
func (a AgentTaskRetentionWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// agentTaskRetentionWorkspaceWorker purges one workspace.
type agentTaskRetentionWorkspaceWorker struct {
	sweeper *AgentTaskRetentionSweeper
}

func (w *agentTaskRetentionWorkspaceWorker) Work(ctx context.Context, job *river.Job[AgentTaskRetentionWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.sweeper.SweepWorkspace(wsCtx))
}
