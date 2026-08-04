// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the automation module's clock pass: a dispatcher that
// enumerates the fleet and a workspace worker that runs one tenant's scan.
// The adapters are the only code that knows about River; the module's
// TimeScanner stays the River-agnostic seam.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TimeScanArgs schedules one clock-trigger scan pass (Task 14a): the
// coarse ActivityScan pre-filter converging every CLOCK-triggered
// automation instance (no_activity_reminder today) onto runOne — the
// same dispatch path event triggers use.
type TimeScanArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (TimeScanArgs) Kind() string { return "time_scan" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (TimeScanArgs) FleetWide() {}

// timeScanWorker is the dispatcher: it enumerates the fleet and enqueues one
// scan per workspace, and touches no tenant data itself.
type timeScanWorker struct {
	pool *pgxpool.Pool
}

func (w *timeScanWorker) Work(ctx context.Context, _ *river.Job[TimeScanArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(river.QueueDefault, sweepWorkspaceMaxAttempts),
		func(ws ids.UUID) river.JobArgs { return TimeScanWorkspaceArgs{Workspace: ws} }))
}

// TimeScanWorkspaceArgs is one workspace's clock-trigger scan pass.
type TimeScanWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (TimeScanWorkspaceArgs) Kind() string { return "time_scan_workspace" }

// WorkspaceID binds this scan to its tenant (jobs.WorkspaceScoped).
func (a TimeScanWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// timeScanWorkspaceWorker delegates one workspace's pass to the automation
// module's TimeScanner — River-agnostic by construction (this file's own doc:
// the adapters are the only code that knows about River).
type timeScanWorkspaceWorker struct {
	scanner *automation.TimeScanner
}

func (w *timeScanWorkspaceWorker) Work(ctx context.Context, job *river.Job[TimeScanWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.scanner.ScanWorkspace(wsCtx, job.Args.Workspace))
}
