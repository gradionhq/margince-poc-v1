// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the GDPR retention pass (data-model §3.4, ADR-0011): a
// dispatcher over EVERY workspace and a worker that evaluates one. It
// registers itself — workers and periodic schedule together — so jobs.go,
// which owns the runner's assembly, grows one line as this surface does.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// privacyRetentionQueue keeps the retention pass off the default queue. One
	// workspace's pass applies up to a full batch per policy, each record in its
	// own multi-statement audited transaction, so a fanned-out fleet landing on
	// default would hold its five workers for minutes at a time and stall the
	// short maintenance jobs beside them.
	privacyRetentionQueue = "privacy_retention"
	// Two workers. A pass is bound by the database it already shares with every
	// other job on this process, not by anything outbound, so more workers buy
	// contention on the same pool rather than throughput — while two still keeps
	// one tenant's years of backlog from being the whole fleet's retention
	// latency.
	privacyRetentionMaxWorkers = 2
)

// privacyRetentionPassTimeout is the arithmetic behind
// privacy_retention_workspace's declared timeout, the heaviest of the
// fanned-out passes: privacy.MaxPassDuration is the pass's own ceiling (its
// stage count times its batch bound times its per-record allowance), and the
// margin covers the policy read and the per-stage due-list reads between
// records, which the pool bounds rather than this cap. Anything shorter cancels
// a tenant with a real backlog mid-record on every attempt until the row
// discards, leaving a permanently failing job row on the one obligation whose
// entire point is auditability.
//
// api/jobs.yaml carries the value River is actually handed, so moving this
// number alone moves no wall clock; the declaration names this constant in its
// derived timeout and the job census keeps the two equal.
//
// A var rather than a const because the engine derives its stage count from its
// own selector table instead of hand-counting it.
var privacyRetentionPassTimeout = privacy.MaxPassDuration + 5*time.Minute

// PrivacyRetentionConfig is the retention pass's slice of the runner's boot
// configuration.
type PrivacyRetentionConfig struct {
	// Interval is the dispatcher's cadence — the operator-facing
	// --retention-interval, which stays the schedule source it always was.
	// Non-positive schedules no retention dispatch; api/jobs.yaml declares it.
	Interval time.Duration
}

// addPrivacyRetentionJobs registers the retention workers and returns the
// dispatcher's periodic schedule for the caller to append.
//
// The service is built HERE rather than handed in because two of its
// dependencies come from opposite sides of the tree: the object store is the
// runner's own (Art. 17 reaches the attachment bytes), and the edge invalidator
// is a search closure, which a module may not import from privacy.
//
// A non-positive interval registers the workers but no schedule — the posture
// the declaration states and jobschedule.go resolves. It cannot reach a
// deployment that owes the storage-limitation obligation: --retention-interval
// carries a positive default and cmd/worker's validateSchedulerIntervals
// refuses a non-positive one at boot. The omission serves the callers that
// wire a runner for a few named passes and never meant to run this one.
func addPrivacyRetentionJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	// Retention removes the interactions the relationship graph is folded
	// from, so it carries the fold with it — in the same transaction, not on
	// the bus. Without the blobstore its erase action leaves the attachment
	// objects behind; without the invalidator the aggregates keep counting
	// interactions that no longer exist.
	retention := privacy.NewRetentionService(pool, cfg.Blobstore, log).
		WithEdgeInvalidator(func(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
			return search.RecomputeEdgesForActivities(ctx, tx, []ids.UUID{activityID})
		})
	addDeclaredWorker[PrivacyRetentionArgs](reg, &privacyRetentionWorker{pool: pool})
	addDeclaredWorker[PrivacyRetentionWorkspaceArgs](reg, &privacyRetentionWorkspaceWorker{retention: retention})
	return periodicFor(cfg, PrivacyRetentionArgs{})
}

// PrivacyRetentionArgs schedules one fleet-wide retention pass.
type PrivacyRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (PrivacyRetentionArgs) Kind() string { return "privacy_retention" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (PrivacyRetentionArgs) FleetWide() {}

// privacyRetentionWorker is the dispatcher. It enumerates EVERY workspace,
// archived ones included: archiving a workspace does not un-store the subject
// data inside it, and storage limitation is owed on the tenants nobody looks at
// any more exactly as much as on the ones in daily use.
type privacyRetentionWorker struct {
	pool *pgxpool.Pool
}

func (w *privacyRetentionWorker) Work(ctx context.Context, _ *river.Job[PrivacyRetentionArgs]) error {
	workspaces, err := enumerateEveryWorkspace(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx),
		workspaceSweepOpts(PrivacyRetentionWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return PrivacyRetentionWorkspaceArgs{Workspace: ws} }))
}

// PrivacyRetentionWorkspaceArgs evaluates one workspace's retention policies.
type PrivacyRetentionWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (PrivacyRetentionWorkspaceArgs) Kind() string { return "privacy_retention_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a PrivacyRetentionWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// privacyRetentionWorkspaceWorker evaluates one workspace.
type privacyRetentionWorkspaceWorker struct {
	retention *privacy.RetentionService
}

func (w *privacyRetentionWorkspaceWorker) Work(ctx context.Context, job *river.Job[PrivacyRetentionWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.retention.EvaluateWorkspace(retentionPassProvenance(wsCtx)))
}

// retentionPassProvenance names who acted and under which pass. The engine
// writes an audit row and an outbox event per record it retires, so without
// this those rows would carry no actor and no correlation id — the workspace
// binding alone says which tenant's data moved, never that the machine moved
// it on a schedule, which is the whole answer a retention audit is read for.
func retentionPassProvenance(ctx context.Context) context.Context {
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
