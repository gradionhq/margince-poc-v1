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

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// GmailWatchConfig configures the Gmail push-watch maintenance pass. Topic is
// the Pub/Sub topic Gmail publishes change notifications to (empty disables the
// pass entirely — capture stays on the poll); Interval is the scan cadence; and
// RenewWithin is how far ahead of a watch's expiry it is re-registered.
type GmailWatchConfig struct {
	Topic       string
	Interval    time.Duration
	RenewWithin time.Duration
}

// GmailSyncArgs schedules one incremental-sync pass over every active Gmail
// connection (capture.md CAP-WIRE-N-1: capture rides provider delta, driven
// here by a poll rather than a push watch in this slice).
type GmailSyncArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GmailSyncArgs) Kind() string { return "gmail_sync" }

// gmailSyncWorker walks the fleet's active Gmail connections and runs one
// incremental SyncOnce per connection under that connection's workspace. A
// single connection's failure is logged and skipped, never aborting the pass;
// only a fleet-enumeration failure is returned (so River retries the tick).
type gmailSyncWorker struct {
	river.WorkerDefaults[GmailSyncArgs]
	registry *capture.Registry
	log      *slog.Logger
}

func (w *gmailSyncWorker) Work(ctx context.Context, _ *river.Job[GmailSyncArgs]) error {
	due, enumErr := w.registry.DueConnections(ctx, "gmail")
	for _, d := range due {
		wsCtx := principal.WithWorkspaceID(ctx, d.Workspace.UUID)
		if err := w.registry.SyncOnce(wsCtx, d.ID); err != nil {
			w.log.WarnContext(ctx, "gmail connection sync failed", "connection", d.ID.String(), "err", err)
		}
	}
	return enumErr
}

// TimeScanArgs schedules one clock-trigger scan pass (Task 14a): the
// coarse ActivityScan pre-filter converging every CLOCK-triggered
// automation instance (no_activity_reminder today) onto runOne — the
// same dispatch path event triggers use.
type TimeScanArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (TimeScanArgs) Kind() string { return "time_scan" }

// timeScanWorker delegates a River job to the automation module's
// TimeScanner — River-agnostic by construction (this file's own doc: the
// adapters are the only code that knows about River).
type timeScanWorker struct {
	river.WorkerDefaults[TimeScanArgs]
	scanner *automation.TimeScanner
}

func (w *timeScanWorker) Work(ctx context.Context, _ *river.Job[TimeScanArgs]) error {
	return w.scanner.Scan(ctx)
}

// GmailWatchArgs schedules one push-watch maintenance pass: register a Gmail
// users.watch for every active connection that has none yet and renew any
// nearing its 7-day expiry (capture.md CAP-DDL-2). Scheduled only when a
// Pub/Sub topic is configured; without one, no watch job runs and capture stays
// on the poll (GmailSyncArgs).
type GmailWatchArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GmailWatchArgs) Kind() string { return "gmail_watch_renew" }

// gmailWatchWorker walks the fleet's active Gmail connections whose watch is
// missing or nearing expiry and registers/renews each against the configured
// Pub/Sub topic, advancing watch_expires_at. One connection's failure is logged
// and skipped (a revoked mailbox must not force the whole pass to retry); only a
// fleet-enumeration failure is returned (so River retries the tick). It mirrors
// gmailSyncWorker — the same DueConnections-shaped walk, keyed on the renewal
// deadline instead of the sync cursor.
type gmailWatchWorker struct {
	river.WorkerDefaults[GmailWatchArgs]
	registry    *capture.Registry
	topic       string
	renewWithin time.Duration
	log         *slog.Logger
}

func (w *gmailWatchWorker) Work(ctx context.Context, _ *river.Job[GmailWatchArgs]) error {
	due, enumErr := w.registry.DueWatches(ctx, "gmail", w.renewWithin)
	for _, d := range due {
		wsCtx := principal.WithWorkspaceID(ctx, d.Workspace.UUID)
		if err := w.registry.RenewWatch(wsCtx, d.ID, w.topic); err != nil {
			w.log.WarnContext(ctx, "gmail watch renewal failed", "connection", d.ID.String(), "err", err)
		}
	}
	return enumErr
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

// JobRunnerConfig is NewJobRunner's boot configuration: the three
// always-on periodic passes' intervals, the optional Gmail poll (added
// only when GmailRegistry is non-nil), and the optional Gmail push-watch
// maintenance pass (added only when GmailRegistry is non-nil AND
// GmailWatch.Topic is set).
type JobRunnerConfig struct {
	CloseDateInterval time.Duration
	ReconcileInterval time.Duration
	TimeScanInterval  time.Duration
	GmailRegistry     *capture.Registry
	GmailInterval     time.Duration
	GmailWatch        GmailWatchConfig
	// DeepReadBrain is the model lane the site deep-read job extracts with
	// (the worker's modelPath.ColdStart — the same lane the api's quick
	// scrape uses). May be nil: the deep-read worker still registers, so a
	// queued read on a brainless worker finishes failed with an actionable
	// log instead of sitting queued forever behind a job no one works.
	DeepReadBrain completer
}

// NewJobRunner wires the deals correctors and the automation time-scan
// into River periodic jobs for the worker process role. The intervals
// keep the operator-facing --close-date-interval / --reconcile-interval /
// --time-scan-interval flags as the schedule source; RunOnStart preserves
// the old ticker's boot-time first pass.
//
// When cfg.GmailRegistry is non-nil (the deployment configured the Gmail
// OAuth app), a Gmail incremental-sync poll is added on cfg.GmailInterval —
// leader-elected like the sweeps, so replicas never double-poll. When a
// Pub/Sub topic is also configured (cfg.GmailWatch.Topic != ""), a push-watch
// maintenance pass is added on cfg.GmailWatch.Interval that registers/renews
// Gmail watches nearing expiry; without a topic the watch job is absent by
// omission and capture stays on the poll.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, cfg JobRunnerConfig) (*jobs.Runner, error) {
	workers := river.NewWorkers()
	// The deep read is not periodic — the api enqueues one job per started
	// dossier; the worker role only needs the worker registered.
	river.AddWorker(workers, newSiteDeepReadWorker(pool, cfg.DeepReadBrain, log))
	river.AddWorker(workers, &closeDateSweepWorker{corrector: NewCloseDateCorrector(pool, log)})
	river.AddWorker(workers, &followUpReconcileWorker{reconciler: NewFollowUpReconciler(pool, log)})
	river.AddWorker(workers, &timeScanWorker{scanner: NewTimeScanner(pool, log)})

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(cfg.CloseDateInterval),
			func() (river.JobArgs, *river.InsertOpts) { return CloseDateSweepArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(cfg.ReconcileInterval),
			func() (river.JobArgs, *river.InsertOpts) { return FollowUpReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
		river.NewPeriodicJob(
			river.PeriodicInterval(cfg.TimeScanInterval),
			func() (river.JobArgs, *river.InsertOpts) { return TimeScanArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}

	if cfg.GmailRegistry != nil {
		river.AddWorker(workers, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(cfg.GmailInterval),
			func() (river.JobArgs, *river.InsertOpts) { return GmailSyncArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
		if cfg.GmailWatch.Topic != "" {
			river.AddWorker(workers, &gmailWatchWorker{
				registry: cfg.GmailRegistry, topic: cfg.GmailWatch.Topic, renewWithin: cfg.GmailWatch.RenewWithin, log: log,
			})
			periodic = append(periodic, river.NewPeriodicJob(
				river.PeriodicInterval(cfg.GmailWatch.Interval),
				func() (river.JobArgs, *river.InsertOpts) { return GmailWatchArgs{}, sweepInsertOpts() },
				&river.PeriodicJobOpts{RunOnStart: true},
			))
		}
	}

	return jobs.New(pool, jobs.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
			// Deep reads run on their own bounded pool so long crawls cannot
			// evict the short maintenance jobs from the default queue.
			deepReadQueue: {MaxWorkers: deepReadMaxWorkers},
		},
		Workers:      workers,
		PeriodicJobs: periodic,
	}, log)
}
