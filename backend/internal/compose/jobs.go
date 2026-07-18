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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// dispatchScanInterval is the due-scan cadence — an indexed one-row-per-due
// query, deliberately decoupled from per-connection pacing (the sidecar's
// next_sync_at owns that).
const dispatchScanInterval = 30 * time.Second

// GmailWatchConfig configures the Gmail push-watch maintenance pass. Topic is
// the Pub/Sub topic Gmail publishes change notifications to (empty disables the
// pass entirely — capture stays on the poll); Interval is the scan cadence; and
// RenewWithin is how far ahead of a watch's expiry it is re-registered.
type GmailWatchConfig struct {
	Topic       string
	Interval    time.Duration
	RenewWithin time.Duration
}

// GmailSyncArgs schedules one DISPATCH pass: scan the fleet for due Gmail
// connections (the sidecar's backoff/pacing gate, ADR-0063) and enqueue one
// CaptureSyncArgs job per connection. The dispatcher never syncs inline —
// per-connection jobs isolate failures and kill head-of-line blocking.
type GmailSyncArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GmailSyncArgs) Kind() string { return "gmail_sync" }

// gmailSyncWorker is the dispatcher: due-scan, then one insert per
// connection. Uniqueness on the connection id means a still-running or
// already-queued sync is not double-enqueued; only a fleet-enumeration
// failure is returned (so River retries the tick).
type gmailSyncWorker struct {
	river.WorkerDefaults[GmailSyncArgs]
	registry *capture.Registry
	log      *slog.Logger
}

func (w *gmailSyncWorker) Work(ctx context.Context, _ *river.Job[GmailSyncArgs]) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	var enumErr error
	for _, desc := range w.registry.Connectors() {
		due, err := w.registry.DueConnections(ctx, desc.Name)
		if err != nil {
			enumErr = errors.Join(enumErr, err)
		}
		for _, d := range due {
			if _, err := client.Insert(ctx, CaptureSyncArgs{
				Workspace:    d.Workspace.String(),
				ConnectionID: d.ID.String(),
				Provider:     desc.Name,
			}, &river.InsertOpts{
				UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeSweepStates},
			}); err != nil {
				w.log.WarnContext(ctx, "capture sync enqueue failed", "connection", d.ID.String(), "provider", desc.Name, "err", err)
			}
		}
	}
	return enumErr
}

// CaptureSyncArgs syncs ONE connection. Unique by args while incomplete, so
// the dispatcher and the (future) push webhook can both enqueue without
// double-running a mailbox.
type CaptureSyncArgs struct {
	Workspace    string `json:"workspace"`
	ConnectionID string `json:"connection_id"`
	Provider     string `json:"provider"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureSyncArgs) Kind() string { return "capture_sync" }

// captureSyncWorker runs one SyncOnce under the connection's workspace. A
// sync failure returns nil after the registry has recorded it: the sidecar's
// backoff owns the retry cadence (ADR-0063) — a River retry would bypass it.
type captureSyncWorker struct {
	river.WorkerDefaults[CaptureSyncArgs]
	registry *capture.Registry
	log      *slog.Logger
}

func (w *captureSyncWorker) Work(ctx context.Context, job *river.Job[CaptureSyncArgs]) error {
	ws, err := ids.Parse(job.Args.Workspace)
	if err != nil {
		return fmt.Errorf("capture_sync: workspace id: %w", err)
	}
	conn, err := ids.Parse(job.Args.ConnectionID)
	if err != nil {
		return fmt.Errorf("capture_sync: connection id: %w", err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := w.registry.SyncOnce(wsCtx, conn); err != nil {
		w.log.WarnContext(ctx, "capture connection sync failed",
			"connection", job.Args.ConnectionID, "provider", job.Args.Provider, "err", err)
	}
	return nil
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
	GmailWatch        GmailWatchConfig
	// ClassifyBrain is the capture-classify model lane (the worker's
	// modelPath.CaptureClassify). Nil = no AI configured — the label pass
	// is absent by omission and mail simply stays unlabeled (honest no-op).
	ClassifyBrain completer
	// DeepReadBrain is the model lane the site deep-read job extracts with
	// (the worker's modelPath.SiteExtract — the crawl's own routing
	// dial). May be nil: the deep-read worker still registers, so a
	// queued read on a brainless worker finishes failed with an actionable
	// log instead of sitting queued forever behind a job no one works.
	DeepReadBrain completer
	// DeepReadFactBrain serves the page-parallel fact lane
	// (modelPath.SiteFactExtract); nil falls back to DeepReadBrain.
	DeepReadFactBrain completer
	// DeepReadCaps bounds each deep-read crawl; the zero value takes the
	// compose defaults (CrawlCaps.withDefaults).
	DeepReadCaps CrawlCaps
}

// NewJobRunner wires the deals correctors and the automation time-scan
// into River periodic jobs for the worker process role. The intervals
// keep the operator-facing --close-date-interval / --reconcile-interval /
// --time-scan-interval flags as the schedule source; RunOnStart preserves
// the old ticker's boot-time first pass.
//
// When cfg.GmailRegistry is non-nil (the deployment configured the Gmail
// OAuth app), the sync DISPATCHER is added on a fixed 30s scan — a cheap
// indexed due-scan enqueueing one per-connection job per due row; the
// per-connection pacing (--gmail-sync-interval) lives in the registry's
// scheduling sidecar, so a frequent scan never means frequent provider
// calls. Leader-elected like the sweeps, so replicas never double-poll. When a
// Pub/Sub topic is also configured (cfg.GmailWatch.Topic != ""), a push-watch
// maintenance pass is added on cfg.GmailWatch.Interval that registers/renews
// Gmail watches nearing expiry; without a topic the watch job is absent by
// omission and capture stays on the poll.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, cfg JobRunnerConfig) (*jobs.Runner, error) {
	workers := river.NewWorkers()
	// The deep read is not periodic — the api enqueues one job per started
	// dossier; the worker role only needs the worker registered.
	river.AddWorker(workers, newSiteDeepReadWorker(pool, cfg.DeepReadBrain, cfg.DeepReadFactBrain, log, cfg.DeepReadCaps))
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

	if cfg.ClassifyBrain != nil {
		river.AddWorker(workers, &captureClassifyWorker{
			classifier: NewCaptureClassifier(pool, cfg.ClassifyBrain, log),
		})
		// The hourly catch-up pass (ADR-0063): the nightly suite reruns the
		// same engine; the backlog index makes an empty pass one cheap probe.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureClassifyArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	if cfg.GmailRegistry != nil {
		river.AddWorker(workers, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
		river.AddWorker(workers, &captureSyncWorker{registry: cfg.GmailRegistry, log: log})
		// Backfill jobs are enqueued by the api (start op); the worker role
		// only needs the pager registered.
		river.AddWorker(workers, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log})
		// The dispatcher tick is a cheap indexed due-scan; per-connection
		// pacing lives in the sidecar (next_sync_at = success + interval),
		// so a frequent scan does not mean frequent provider calls.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(dispatchScanInterval),
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

// CaptureBackfillArgs pages ONE bounded backfill run (ADR-0063). Unique by
// args while incomplete: start and any retry converge on one job.
type CaptureBackfillArgs struct {
	Workspace  string `json:"workspace"`
	BackfillID string `json:"backfill_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (CaptureBackfillArgs) Kind() string { return "capture_backfill" }

// captureBackfillWorker pages a run to completion, yielding between pages
// (snooze) so a long mailbox never monopolizes a worker slot. A page error
// returns nil after the engine recorded it — the run row owns its state.
type captureBackfillWorker struct {
	river.WorkerDefaults[CaptureBackfillArgs]
	registry *capture.Registry
	log      *slog.Logger
}

// backfillPagesPerTick bounds how many pages one Work invocation walks
// before yielding the worker slot.
const backfillPagesPerTick = 10

func (w *captureBackfillWorker) Work(ctx context.Context, job *river.Job[CaptureBackfillArgs]) error {
	ws, err := ids.Parse(job.Args.Workspace)
	if err != nil {
		return fmt.Errorf("capture_backfill: workspace id: %w", err)
	}
	bfID, err := ids.Parse(job.Args.BackfillID)
	if err != nil {
		return fmt.Errorf("capture_backfill: backfill id: %w", err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	for i := 0; i < backfillPagesPerTick; i++ {
		done, err := w.registry.RunBackfillStep(wsCtx, bfID)
		if err != nil {
			// The engine recorded the failure class on the run; the log
			// carries the detail. The row owns retry policy, not River.
			w.log.WarnContext(ctx, "capture backfill page failed", "backfill", job.Args.BackfillID, "err", err)
			return nil
		}
		if done {
			return nil
		}
	}
	return river.JobSnooze(time.Second)
}

// CaptureClassifyArgs runs one catch-up classify pass (ADR-0063; §2.8).
type CaptureClassifyArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (CaptureClassifyArgs) Kind() string { return "capture_classify" }

// captureClassifyWorker drives the batched label engine; the engine
// commits per model call, so a mid-pass crash or budget stop loses
// nothing and the next tick resumes from the shrunken backlog.
type captureClassifyWorker struct {
	river.WorkerDefaults[CaptureClassifyArgs]
	classifier *CaptureClassifier
}

func (w *captureClassifyWorker) Work(ctx context.Context, _ *river.Job[CaptureClassifyArgs]) error {
	return w.classifier.Run(ctx, 0)
}
