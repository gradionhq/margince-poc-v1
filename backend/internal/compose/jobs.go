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

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// IdempotencyRetentionArgs schedules one purge of replay claims past the
// window. Always-on: the claim bodies are record snapshots, so retaining them
// past the retry they protect is subject data kept for no purpose.
type IdempotencyRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (IdempotencyRetentionArgs) Kind() string { return "idempotency_retention" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (IdempotencyRetentionArgs) FleetWide() {}

// idempotencyRetentionWorker delegates a River job to the compose sweeper.
type idempotencyRetentionWorker struct {
	river.WorkerDefaults[IdempotencyRetentionArgs]
	sweeper *IdempotencyRetentionSweeper
}

func (w *idempotencyRetentionWorker) Work(ctx context.Context, _ *river.Job[IdempotencyRetentionArgs]) error {
	return jobs.FaultContext(ctx, w.sweeper.Sweep(ctx))
}

// dispatchScanInterval is the due-scan cadence — an indexed one-row-per-due
// query, deliberately decoupled from per-connection pacing (the sidecar's
// next_sync_at owns that).
const dispatchScanInterval = 30 * time.Second

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
// only when GmailRegistry is non-nil), the optional Gmail push-watch
// maintenance pass (added only when GmailRegistry is non-nil AND
// GmailWatch.Topic is set), and the optional overlay reconcile poller
// (added only when OverlayVault is non-nil).
type JobRunnerConfig struct {
	// SendPacing bounds how fast one mailbox transmits and how long a
	// delivery may be deferred before it parks; the zero value takes the
	// documented defaults (SendPacing.withDefaults).
	SendPacing SendPacing
	// SendRegistry resolves the transmitting mailbox for a staged delivery.
	// Nil means this role registers no send worker at all: a delivery it
	// picked up could only fail on every attempt, and a queued send is better
	// left for a role that can actually resolve a mailbox.
	SendRegistry *capture.Registry

	CloseDateInterval time.Duration
	ReconcileInterval time.Duration
	TimeScanInterval  time.Duration
	GmailRegistry     *capture.Registry
	GmailWatch        GmailWatchConfig
	// ChannelVault is the custodian of a channel connection's sealed bot token.
	// Nil means this role registers no Telegram poller at all: a poll cannot
	// authenticate without the token, so a dispatcher wired without a vault
	// could only fail every job it enqueued. Declared by omission, the posture
	// GmailRegistry and OverlayVault already take.
	ChannelVault keyvault.Vault
	// ChannelAPI is the Telegram Bot API seam the poller dials out through. Nil
	// takes the real client, which is what every process role passes; the
	// acceptance suites substitute a fake, because a poller left on the real
	// client would reach api.telegram.org from a test run.
	ChannelAPI telegram.API
	// CaptureConfig is the deployment's capture suppression-list config
	// (CAP-PARAM-5/6). The Telegram ingest worker needs it to build the
	// IDENTICAL guarded Sink every other capture path shares
	// (newCaptureSink) rather than a second, divergently-configured one —
	// the zero value is the pinned baselines, so an unset field still
	// yields a working (if unconfigured) Sink rather than none.
	CaptureConfig CaptureConfig
	// ClassifyBrain is the capture-classify model lane (the worker's
	// modelPath.CaptureClassify). Nil = no AI configured — the label pass
	// is absent by omission and mail simply stays unlabeled (honest no-op).
	ClassifyBrain completer
	// EnrichBrain is the signature-enrich lane; nil = the pass is absent
	// by omission and connector-created people keep their empty fields.
	EnrichBrain completer
	// VerdictBrain is the ADR-0072 counterparty-verdict lane. Nil = no AI
	// configured, and the consequence is deliberate: deferred senders stay
	// deferred rather than being created on sight. An installation without a
	// model keeps the old junk OUT, it does not fall back to letting it in.
	VerdictBrain    completer
	OverlayVault    keyvault.Vault
	OverlayInterval time.Duration
	// OverlayMeter is the poller's OVB meter — built by cmd/worker over the
	// SAME Redis the api's force-fresh meter uses, so both lanes share one
	// per-workspace-per-incumbent count (keeping the raw-Redis dependency in
	// the cmd tier, never compose). Nil makes the poller fail-closed (it
	// still mirrors; its Consume* calls are silent no-ops with no Redis, so a
	// nil meter means unmetered recording, never a refused sweep).
	OverlayMeter *overlaybudget.Meter
	// OverlayBackfillLimit bounds the initial mirror backfill at this many
	// records per object class (dev/demo — MARGINCE_OVERLAY_BACKFILL_LIMIT);
	// 0 (the default) is uncapped.
	OverlayBackfillLimit int
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
	// Blobstore holds the logo bytes a deep read resolves from the site it
	// crawls (A55). Nil is a worker role with no object store: reads still
	// run and still land their facts, and every company keeps the monogram
	// the render layer draws when no logo is on file.
	Blobstore blobstore.Store
	// Embedder is the retrieval embed lane (ModelPath.Embedder) the
	// embed-reindex worker re-embeds under. The worker registers
	// regardless of whether this is nil: a picked-up embed_reindex job
	// on a brainless worker role fails clearly (embedReindexWorker.Work)
	// rather than sitting queued forever behind a job no one can work —
	// the same posture as DeepReadBrain.
	Embedder search.Embedder
	// VoiceBrain is the voice-build model lane (the worker's
	// modelPath.VoiceBuild). May be nil: the build worker still registers,
	// so a queued build on a brainless worker finishes failed with an
	// actionable message instead of sitting queued forever.
	VoiceBrain completer
	// FxSourceURL is the URL of the rates page the fx-rate refresh fetches and
	// AI-extracts from; empty = the worker registers but the producer no-ops
	// (honest: no source configured, nothing to propose).
	FxSourceURL string
	// FxBootstrapCurrencies is the candidate foreign-currency set the fx-rate
	// refresh proposes when the sheet is still empty (a fresh install has no
	// tracked currency to derive symbols from). Empty ⇒ an empty sheet stays a
	// no-op; a human still approves every bootstrapped proposal.
	FxBootstrapCurrencies []string
	// FxExtractBrain is the model lane the fx-rate refresh extracts with
	// (modelPath.RateExtract, shared with the model-cost refresh); nil = the
	// worker registers but the producer no-ops (same posture as RateExtractBrain).
	FxExtractBrain completer
	// RateExtractBrain is the model lane the model-cost refresh job extracts
	// pricing with (modelPath.RateExtract); nil = the worker registers but
	// the producer no-ops (same posture as the deep-read brain).
	RateExtractBrain completer
	// ModelPricingSources binds provider names to pricing-page URLs the
	// model-cost refresh crawls; empty = no-op.
	ModelPricingSources []pricingSource
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
//
// When cfg.OverlayVault is non-nil (the deployment configured a secret
// vault), the overlay reconcile poller is added on cfg.OverlayInterval —
// leader-elected the same way, sweeping every overlay-mode workspace's
// active incumbent connection. Without a vault there is no credential
// custodian to resolve a connection's sealed token through, so the poller
// stays off by omission, the same posture cfg.GmailRegistry==nil takes for
// the Gmail poll.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, cfg JobRunnerConfig) (*jobs.Runner, error) {
	workers := river.NewWorkers()
	// The deep read is not periodic — the api enqueues one job per started
	// dossier; the worker role only needs the worker registered.
	river.AddWorker(workers, newSiteDeepReadWorker(pool, cfg.DeepReadBrain, cfg.DeepReadFactBrain, log, cfg.DeepReadCaps, cfg.Blobstore))
	// The voice build is not periodic — the api enqueues one job per created
	// build; only the deferred-retry sweep ticks. Both register even with a
	// nil brain so a queued build fails actionably instead of rotting.
	river.AddWorker(workers, newVoiceBuildWorker(pool, cfg.VoiceBrain, log))
	river.AddWorker(workers, &voiceBuildRetryWorker{store: ai.NewVoiceStore(pool), log: log})
	// Each scheduled pass is a dispatcher plus a workspace worker. Only the
	// dispatcher gets a periodic entry below; the workspace worker is enqueued,
	// never ticked.
	river.AddWorker(workers, &closeDateSweepWorker{pool: pool})
	river.AddWorker(workers, &closeDateWorkspaceWorker{corrector: NewCloseDateCorrector(pool, log)})
	river.AddWorker(workers, &followUpReconcileWorker{pool: pool})
	river.AddWorker(workers, &followUpWorkspaceWorker{reconciler: NewFollowUpReconciler(pool, log)})
	river.AddWorker(workers, &timeScanWorker{pool: pool})
	river.AddWorker(workers, &timeScanWorkspaceWorker{scanner: NewTimeScanner(pool, log)})
	river.AddWorker(workers, &idempotencyRetentionWorker{sweeper: NewIdempotencyRetentionSweeper(pool, log)})
	// The Telegram ingest job is not periodic — a poll enqueues one per accepted
	// update in the same transaction as the raw capture row; the worker role only
	// needs the worker registered,
	// same posture as the deep-read and embed-reindex workers. Registered
	// unconditionally: unlike Gmail/Graph, a channel connection carries its
	// own per-connection credential (no deployment-wide OAuth app to gate
	// on), so there is nothing to check for before wiring it up.
	river.AddWorker(workers, newTelegramIngestWorker(pool, cfg.CaptureConfig, log))
	// The embed-reindex job is not periodic — the api enqueues one job per
	// confirmed reindex (embedreindextransport.go); the worker role only
	// needs the worker registered, same posture as the deep-read worker
	// above.
	river.AddWorker(workers, &embedReindexWorker{store: search.NewStore(pool), embedder: cfg.Embedder})
	// The rate-refresh jobs are not periodic — the api enqueues one per admin
	// "Refresh from sources" click; the worker registers regardless of whether
	// a source is configured (a nil brain / empty url no-ops honestly).
	river.AddWorker(workers, newFxRefreshWorker(pool, cfg.FxExtractBrain, cfg.FxSourceURL, cfg.FxBootstrapCurrencies, log))
	river.AddWorker(workers, newModelCostRefreshWorker(pool, cfg.RateExtractBrain, cfg.ModelPricingSources, log))
	// The captured-organization auto-enrich sweep (ADR-0072/A118): always
	// registered, it enqueues system deep reads the worker above applies.
	river.AddWorker(workers, newCaptureAutoEnrichSweepWorker(pool, log))
	// The outbound send is not periodic — the api stages one job per accepted
	// message, in the same transaction as the activity; this role only needs
	// the worker registered.
	if cfg.SendRegistry != nil {
		river.AddWorker(workers, newSendWorker(pool, cfg.SendRegistry, cfg.SendPacing))
	}

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
		river.NewPeriodicJob(
			river.PeriodicInterval(voiceBuildRetryInterval),
			func() (river.JobArgs, *river.InsertOpts) { return VoiceBuildRetryArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		),
		// The captured-organization auto-enrich sweep (ADR-0072/A118): daily,
		// run-on-start to enrich the existing captured backlog on a fresh boot.
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureAutoEnrichSweepArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true}),
		// Idempotency retention: hourly rather than daily, because the rows
		// hold verbatim record snapshots and the replay window they serve is
		// only 24h — a daily pass would leave a day's worth of expired PII
		// sitting past its purpose. Run-on-start clears the backlog an
		// installation upgrading into this sweep already carries.
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return IdempotencyRetentionArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
	// The ADR-0078 relationship-graph passes register themselves, so this
	// wiring stays one line as that surface grows (jobs_graph.go).
	periodic = append(periodic, addGraphJobs(workers, pool, log)...)
	// The ADR-0069 §3a embed drift sweep registers itself the same way
	// (embeddriftsweep.go) — worker + tick only when an embed lane is bound.
	periodic = append(periodic, addEmbedDriftSweepJob(workers, pool, cfg.Embedder, log)...)

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

	if cfg.EnrichBrain != nil {
		river.AddWorker(workers, &captureEnrichWorker{
			enricher: NewCaptureEnricher(pool, cfg.EnrichBrain, log),
		})
		// Daily (the ADR-0063 nightly cadence rides the same job until the
		// nightly dispatcher lands); run-on-start clears any backlog early.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureEnrichArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	{
		// Registered unconditionally: the promotion weighs evidence rows the
		// enrich pass already wrote, so it needs no model. Gating it on a brain
		// would leave an AI-less deployment unable to act on signatures it had
		// already collected.
		river.AddWorker(workers, &orgNamePromotionWorker{promoter: NewOrgNamePromoter(pool, log)})
		// Daily, after the enrich pass has had a night to collect signatures;
		// run-on-start so a deployment with a backlog acts on it immediately.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return OrgNamePromotionArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	{
		// Registered unconditionally: only the JUDGING stage needs a model, and
		// the worker skips it when none is configured. Gating the whole worker on
		// a brain would mean an AI-less deployment never staged a review for an
		// existing unsure row and never redacted mail it had already hidden.
		verdicts := NewCounterpartyVerdictEngine(pool, cfg.VerdictBrain, log)
		river.AddWorker(workers, &counterpartyVerdictWorker{engine: verdicts})
		// Hourly, like classify: the ledger's due-index makes an empty pass one
		// cheap probe, and a deferred sender should not wait a day to become a
		// record. The one job runs every stage in dependency order —
		// judging fills the unsure backlog that staging then offers, and
		// redaction only ever acts on windows that closed before this tick.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CounterpartyVerdictArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	// Telegram ingress PULLS (telegrampoll.go), and a poll needs the vault that
	// holds the bot's sealed token — so a role without one registers neither half.
	periodic = registerTelegramPoll(workers, periodic, pool, cfg.ChannelVault, cfg.ChannelAPI, log)

	if cfg.GmailRegistry != nil {
		river.AddWorker(workers, &captureDigestWorker{registry: cfg.GmailRegistry, pool: pool, log: log})
		// The digest builds daily after the overnight passes; run-on-start
		// backfills a missed night so mornings are never silently empty.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureDigestArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
		river.AddWorker(workers, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
		river.AddWorker(workers, &captureSyncWorker{registry: cfg.GmailRegistry, log: log})
		// Backfill jobs are enqueued by the api (start op); the worker role
		// only needs the pager registered.
		river.AddWorker(workers, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log})
		// The dispatcher tick is a cheap indexed due-scan; per-connection
		// pacing lives in the sidecar (next_sync_at = success + interval),
		// so a frequent scan does not mean frequent provider calls. It scans
		// every registered connector, so a Google Calendar connection (the same
		// Google OAuth app) syncs on the identical per-connection path a mailbox
		// does — no gcal-specific job.
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

	if cfg.OverlayVault != nil {
		ms := overlay.NewMirrorStore(pool, unresolvedOwnerEmails{})
		// cmd/worker built the meter over the shared Redis (so the poller's
		// spend and the api's force-fresh spend land on ONE count); fall back
		// to a fail-closed meter if a role wired the poller without one.
		meter := cfg.OverlayMeter
		if meter == nil {
			meter = failClosedOverlayMeter()
		}
		river.AddWorker(workers, &overlayReconcileWorker{pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log, newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit)})
		// The webhook-as-signal re-fetch worker (OVA-WIRE-10): consumes the
		// coalesced OverlayRefetchArgs the receiver enqueues, refreshing one
		// record through the same store the poller uses. Registered whenever
		// the overlay vault is present (the receiver only enqueues when the api
		// role has the app secret wired).
		river.AddWorker(workers, &overlayRefetchWorker{pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log, newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit)})
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(cfg.OverlayInterval),
			func() (river.JobArgs, *river.InsertOpts) { return OverlayReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	return jobs.New(pool, jobs.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
			// Deep reads run on their own bounded pool so long crawls cannot
			// evict the short maintenance jobs from the default queue.
			deepReadQueue: {MaxWorkers: deepReadMaxWorkers},
			// Rate refreshes (FX fetch + pricing-page crawl+LLM extract) are
			// likewise long; their own bounded pool keeps a multi-workspace
			// burst from starving close-date, reconcile, and capture jobs.
			rateRefreshQueue: {MaxWorkers: rateRefreshMaxWorkers},
			// The AI-backed capture passes make serial model calls, so a
			// fanned-out fleet of them would occupy every default worker and
			// delay sends, Telegram polls, and capture syncs. Same species as
			// deep reads — long and model-bound — so the same posture.
			aiCaptureQueue: {MaxWorkers: aiCaptureMaxWorkers},
			// Overlay reconcile is SERIAL by design. overlaybudget.ConsumeSearch
			// counts but does not pace, and its keys are per workspace, so it
			// cannot bound a provider-level burst: a concurrent fan-out could
			// exceed the incumbent's per-second Search limit. Each workspace
			// still gets its own job row, which is the observability this phase
			// is after; per-workspace PARALLELISM is not.
			overlayReconcileQueue: {MaxWorkers: 1},
		},
		Workers:      workers,
		PeriodicJobs: periodic,
	}, log)
}
