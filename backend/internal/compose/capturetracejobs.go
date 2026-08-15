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
func (CaptureTraceSweepArgs) FleetWide() {
	// Intentionally empty: jobs.FleetWide is a marker interface, and the method
	// exists to be satisfied rather than called. The dispatcher's work is
	// Work(), which enumerates the fleet.
}

type captureTraceSweepWorker struct {
	pool *pgxpool.Pool
}

func (w *captureTraceSweepWorker) Work(ctx context.Context, _ *river.Job[CaptureTraceSweepArgs]) error {
	// EVERY workspace, archived ones included — the enumeration the other
	// retention passes use, and for the reason its own comment gives: archiving
	// a workspace does not un-store the data inside it, and storage limitation
	// does not pause because a tenant stopped logging in.
	//
	// It matters more here than for most passes. Under the trace_payloads
	// posture these rows hold correspondence content, so skipping archived
	// tenants would keep it past the 24-hour retention this feature promises,
	// in exactly the workspaces nobody looks at any more.
	workspaces, err := enumerateEveryWorkspace(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx),
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

	// workspaceJobDB, NOT InstallationDB: the installation resolver answers
	// ErrMultipleWorkspaces the moment a database holds more than one live
	// tenant, so every child would fail, exhaust its attempts, and delete
	// nothing — while the read kept filtering to 24 hours and the table grew
	// invisibly behind it. Under the payload posture those rows are addresses
	// and subject lines, so the retention this feature promises would stop
	// holding with no symptom at all.
	db, err := workspaceJobDB(w.pool, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	_, err = capture.NewTraceStore(db).SweepOlderThan(wsCtx, traceRetention)
	return jobs.FaultContext(ctx, err)
}

// traceSweepActorID names the sweep in whatever it touches.
const traceSweepActorID = "system:capture-trace-sweep"
