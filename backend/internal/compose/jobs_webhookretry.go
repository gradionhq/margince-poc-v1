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
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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
	// its derived timeout and the job census keeps the two equal.
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
	// the gmail_sync dispatcher's declared scan and is not derived from it — the
	// two are separate passes with separate costs, and moving one does not move
	// the other.
	//
	// Non-positive schedules no retry dispatch; api/jobs.yaml declares it.
	Interval time.Duration
	// Deliverer is the delivery engine one workspace's pass re-attempts
	// through — the SAME instance the role's cg:webhooks consumer fans out
	// with, so a deployment holds one signing cipher and one outbound
	// transport rather than two that could drift apart.
	//
	// Nil is a role with no signing key, and there is then no way to sign a
	// re-attempt: absent by omission, the posture JobRunnerConfig states.
	Deliverer func(*database.DB) *webhooks.Deliverer
}

// addWebhookRetryJobs registers the retry worker and returns its periodic
// schedule for the caller to append. A non-positive interval registers the
// worker but no schedule — the posture the declaration states and
// jobschedule.go resolves.
func addWebhookRetryJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig) []*river.PeriodicJob {
	if cfg.WebhookRetry.Deliverer == nil {
		return nil
	}
	addDeclaredWorker[WebhookRetryArgs](reg, &webhookRetryWorker{pool: pool, deliverer: cfg.WebhookRetry.Deliverer})
	return periodicFor(cfg, WebhookRetryArgs{})
}

// WebhookRetryArgs is one pass over the installation's due retries.
//
// It used to be a dispatcher that enumerated live workspaces and enqueued one
// child per tenant. Under one installation that enumeration reads a single row,
// so the pair collapsed into this (ADR-0091 §5, ADR-0103 §1).
type WebhookRetryArgs struct{}

// Kind is the stable job identifier River persists in river_job. It is the
// DISPATCHER's old kind, deliberately: it is the name operators alert on, and
// the child's `_workspace` kind is the one that had to go.
func (WebhookRetryArgs) Kind() string { return "webhook_retry" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own. Without
// it this pass would take River's silent 25-rung ladder against a 26-minute
// timeout — three attempts is what the fan-out child was governed by, and the
// number is held equal to api/jobs.yaml by
// TestArgsOwnedAttemptCapsMatchTheirDeclaration.
func (WebhookRetryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       webhookRetryQueue,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// webhookRetryWorker re-attempts the due deliveries.
type webhookRetryWorker struct {
	pool      *pgxpool.Pool
	deliverer func(*database.DB) *webhooks.Deliverer
}

// Work resolves no principal and writes no audited row of its own: the sweep
// re-sends deliveries that were already authorized against their owner's scope
// when they were enqueued.
func (w *webhookRetryWorker) Work(ctx context.Context, _ *river.Job[WebhookRetryArgs]) error {
	return jobs.FaultContext(ctx, w.deliverer(InstallationDB(w.pool)).SweepOnce(ctx))
}
