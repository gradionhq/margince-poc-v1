// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the outbound-webhook retry sweep (E10/B-E10.13c): a
// dispatcher over every LIVE workspace and a worker that re-attempts one
// tenant's due deliveries. It registers itself — workers and periodic
// schedule together — so jobs.go, which owns the runner's assembly, grows one
// line as this surface does.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	// webhookRetryQueue keeps the sweep off the default queue. One
	// workspace's pass makes up to a full batch of SEQUENTIAL outbound
	// attempts against endpoints this deployment does not control, so a
	// fanned-out fleet landing on default would hold its five workers for
	// minutes at a time and stall the short maintenance jobs beside them.
	webhookRetryQueue = "webhook_retry"
	// Three workers, not one: a pass is bound by how long a receiver takes
	// to answer, not by anything this process does, so a tenant whose
	// endpoint hangs to its full attempt budget must not hold every other
	// tenant's due retries behind it.
	webhookRetryMaxWorkers = 3
	// webhookRetrySweepTimeout is the arithmetic behind
	// webhook_retry_workspace's declared timeout: webhooks.MaxSweepDuration is
	// the pass's own ceiling (its batch bound times its per-attempt bound), and
	// the margin covers the per-delivery database round trips between attempts,
	// which the pool bounds rather than this cap.
	//
	// api/jobs.yaml carries the value River is actually handed, so moving this
	// number alone moves no wall clock; the declaration names this constant in
	// its derived timeout and TestEveryTranscribedTimeoutStillEqualsItsGoConstant
	// keeps the two equal.
	webhookRetrySweepTimeout = webhooks.MaxSweepDuration + 5*time.Minute
)

// WebhookRetryConfig is the retry sweep's slice of the runner's boot
// configuration.
type WebhookRetryConfig struct {
	// Interval is the dispatcher's cadence — the operator-facing
	// --webhook-retry-interval, taken verbatim as the River schedule. Nothing
	// here clamps it: whatever an operator sets is what River schedules on.
	//
	// It paces the FLEET fan-out, not one delivery's backoff: the per-delivery
	// schedule is the exponential ladder the delivery engine already owns, and
	// this dial only decides how promptly an elapsed backoff is noticed. Every
	// tick inserts one row per live workspace whether or not that workspace has
	// anything due, which is why its DEFAULT is tens of seconds rather than the
	// few a single tenant's ticker could afford. That default happens to equal
	// dispatchScanInterval and is not derived from it — the two are separate
	// passes with separate costs, and moving one does not move the other.
	//
	// Non-positive schedules no retry dispatch (periodicWhenPositive).
	Interval time.Duration
	// Deliverer is the delivery engine one workspace's pass re-attempts
	// through — the SAME instance the role's cg:webhooks consumer fans out
	// with, so a deployment holds one signing cipher and one outbound
	// transport rather than two that could drift apart.
	//
	// Nil is a role with no signing key, and there is then no way to sign a
	// re-attempt: absent by omission, the posture JobRunnerConfig states.
	Deliverer *webhooks.Deliverer
}

// addWebhookRetryJobs registers the retry workers and returns the dispatcher's
// periodic schedule for the caller to append. A non-positive interval registers
// the workers but no schedule, for periodicWhenPositive's reasons.
func addWebhookRetryJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig) []*river.PeriodicJob {
	if cfg.WebhookRetry.Deliverer == nil {
		return nil
	}
	addDeclaredWorker[WebhookRetryArgs](reg, &webhookRetryWorker{pool: pool})
	addDeclaredWorker[WebhookRetryWorkspaceArgs](reg, &webhookRetryWorkspaceWorker{deliverer: cfg.WebhookRetry.Deliverer})
	return periodicWhenPositive(cfg.WebhookRetry.Interval,
		func() (river.JobArgs, *river.InsertOpts) { return WebhookRetryArgs{}, sweepInsertOpts() },
		// Run-on-start because a restart must not add a whole interval to a
		// backoff that already elapsed: the deliveries parked when the process
		// went down are due the moment it is back.
		&river.PeriodicJobOpts{RunOnStart: true})
}

// WebhookRetryArgs schedules one fleet-wide pass over due retries.
type WebhookRetryArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (WebhookRetryArgs) Kind() string { return "webhook_retry" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (WebhookRetryArgs) FleetWide() {}

// webhookRetryWorker is the dispatcher. It enumerates the LIVE workspaces
// only: a delivery parked in an archived tenant belongs to a subscription
// nobody is listening on any more, so re-attempting it spends outbound
// attempts on work nobody wants.
type webhookRetryWorker struct {
	pool *pgxpool.Pool
}

func (w *webhookRetryWorker) Work(ctx context.Context, _ *river.Job[WebhookRetryArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		// The dispatcher's tick is the real retry cadence for a failed
		// workspace — it re-enqueues the tenant on the next pass — so River's
		// ladder is here only to ride out a transient blip inside one tick.
		workspaceSweepOpts(webhookRetryQueue, sweepWorkspaceMaxAttempts),
		func(ws ids.UUID) river.JobArgs { return WebhookRetryWorkspaceArgs{Workspace: ws} }))
}

// WebhookRetryWorkspaceArgs re-attempts one workspace's due deliveries.
type WebhookRetryWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (WebhookRetryWorkspaceArgs) Kind() string { return "webhook_retry_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a WebhookRetryWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// webhookRetryWorkspaceWorker sweeps one workspace.
type webhookRetryWorkspaceWorker struct {
	deliverer *webhooks.Deliverer
}

// Work binds the tenant and nothing else: the sweep re-sends deliveries that
// were already authorized against their owner's scope when they were enqueued,
// so it resolves no principal and writes no audited row of its own.
func (w *webhookRetryWorkspaceWorker) Work(ctx context.Context, job *river.Job[WebhookRetryWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.deliverer.SweepOnce(wsCtx))
}
