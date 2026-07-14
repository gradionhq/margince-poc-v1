// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the worker's scheduled passes: the job args and the
// worker adapters that delegate to the deals correctors, plus NewJobRunner
// which registers them as periodic jobs. The adapters are the only code
// that knows about River — the deals module's Sweep/Reconcile methods stay
// the River-agnostic seam, which is what makes swapping the old ticker
// loops for River behaviour-preserving.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// CloseDateSweepArgs schedules one close-date hygiene pass (INV-CLOSE-PAST).
type CloseDateSweepArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CloseDateSweepArgs) Kind() string { return "close_date_sweep" }

// FollowUpReconcileArgs schedules one overnight follow-up reconciliation
// pass (features/07 §8a).
type FollowUpReconcileArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (FollowUpReconcileArgs) Kind() string { return "follow_up_reconcile" }

// closeDateSweepWorker delegates a River job to the deals corrector.
type closeDateSweepWorker struct {
	river.WorkerDefaults[CloseDateSweepArgs]
	corrector *deals.CloseDateCorrector
}

func (w *closeDateSweepWorker) Work(ctx context.Context, _ *river.Job[CloseDateSweepArgs]) error {
	return w.corrector.Sweep(ctx)
}

// followUpReconcileWorker delegates a River job to the deals reconciler.
type followUpReconcileWorker struct {
	river.WorkerDefaults[FollowUpReconcileArgs]
	reconciler *deals.FollowUpReconciler
}

func (w *followUpReconcileWorker) Work(ctx context.Context, _ *river.Job[FollowUpReconcileArgs]) error {
	return w.reconciler.Reconcile(ctx)
}

// activeSweepStates is the uniqueness window for the periodic passes: a new
// tick is suppressed only while a prior run is still in flight (available,
// pending, running, scheduled, retryable) — reproducing the old ticker's
// one-pass-at-a-time, now enforced across replicas. It deliberately EXCLUDES
// completed: a completed sweep must NOT block the next scheduled run (the
// default ByState includes completed, which for a 24h cadence would stop the
// job firing until the completed row is cleaned out).
var activeSweepStates = []rivertype.JobState{
	rivertype.JobStateAvailable,
	rivertype.JobStatePending,
	rivertype.JobStateRunning,
	rivertype.JobStateScheduled,
	rivertype.JobStateRetryable,
}

// sweepInsertOpts is the shared insert policy for the periodic passes.
func sweepInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByState: activeSweepStates}}
}

// NewJobRunner wires the deals correctors into River periodic jobs for the
// worker process role. The intervals keep the operator-facing
// --close-date-interval / --reconcile-interval flags as the schedule source;
// RunOnStart preserves the old ticker's boot-time first pass.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, closeDateInterval, reconcileInterval time.Duration) (*jobs.Runner, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &closeDateSweepWorker{corrector: NewCloseDateCorrector(pool, log)})
	river.AddWorker(workers, &followUpReconcileWorker{reconciler: NewFollowUpReconciler(pool, log)})

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(closeDateInterval),
			func() (river.JobArgs, *river.InsertOpts) { return CloseDateSweepArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(reconcileInterval),
			func() (river.JobArgs, *river.InsertOpts) { return FollowUpReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}

	return jobs.New(pool, jobs.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers:      workers,
		PeriodicJobs: periodic,
	}, log)
}
