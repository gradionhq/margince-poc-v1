// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The trace sweep's two job kinds: a cadenced dispatcher that enumerates the
// live fleet, and a workspace child that deletes one tenant's tail.
//
// Two rather than one for the reason every fan-out here is two: a kind that
// both ticks on a clock and carries a tenant has no honest answer for whose
// data the tick touched.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// traceRetention is the window the sweep enforces, and the same one the read
// applies. They are one constant so a change cannot leave the API showing rows
// the sweep has already decided to delete, or hiding rows it still holds.
const traceRetention = capture.TraceWindowHours * time.Hour

// CaptureTraceSweepArgs runs one fleet-wide trace sweep.
type CaptureTraceSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CaptureTraceSweepArgs) Kind() string { return "capture_trace_sweep" }

// FleetWide marks this a dispatcher: it enumerates and enqueues, and does no
// tenant work of its own (jobs.FleetWide).
func (CaptureTraceSweepArgs) FleetWide() {}

type captureTraceSweepWorker struct {
	pool *pgxpool.Pool
}

func (w *captureTraceSweepWorker) Work(ctx context.Context, _ *river.Job[CaptureTraceSweepArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(CaptureTraceSweepWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return CaptureTraceSweepWorkspaceArgs{Workspace: ws} }))
}

// CaptureTraceSweepWorkspaceArgs sweeps one workspace's trace tail.
type CaptureTraceSweepWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureTraceSweepWorkspaceArgs) Kind() string { return "capture_trace_sweep_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a CaptureTraceSweepWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

type captureTraceSweepWorkspaceWorker struct {
	pool *pgxpool.Pool
}

func (w *captureTraceSweepWorkspaceWorker) Work(ctx context.Context, job *river.Job[CaptureTraceSweepWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	// Bound HERE rather than in a helper, and assigned rather than called: a
	// queue carries no principal to inherit, and both of these return a new
	// context and mutate nothing, so an unassigned call reads like a binding and
	// leaves the store holding a context with no actor in it.
	//
	// A SYSTEM principal rather than any member's: expiring a diagnostic trace
	// is the installation keeping its own retention promise, and there is no
	// human on whose authority one of these rows should or should not go.
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: traceSweepActorID,
	})
	wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())

	_, err = capture.NewTraceStore(InstallationDB(w.pool)).SweepOlderThan(wsCtx, traceRetention)
	return jobs.FaultContext(ctx, err)
}

// traceSweepActorID names the sweep in whatever it touches.
const traceSweepActorID = "system:capture-trace-sweep"
