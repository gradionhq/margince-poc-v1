// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The job runner's assembly: NewJobRunner, the queue set, the uniqueness
// window every periodic pass shares, and the wiring of each module's workers
// and ticks. The per-concern job files beside it (jobs_deals.go,
// jobs_capture.go, jobs_overlay.go, jobs_automation.go, jobs_retention.go)
// own the args types and worker adapters themselves; those adapters are the
// only code in the tree that knows about River, which is what keeps every
// module's own pass River-agnostic.

import (
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

// JobRunnerConfig is NewJobRunner's boot configuration: the interval of every
// pass whose cadence an operator sets (close-date, follow-up reconcile,
// automation time-scan, GDPR retention), the optional Gmail poll (added
// only when GmailRegistry is non-nil), the optional Gmail push-watch
// maintenance pass (added only when GmailRegistry is non-nil AND
// GmailWatch.Topic is set), and the optional overlay reconcile poller
// (added only when OverlayVault is non-nil).
//
// A nil dependency below takes one of exactly two postures, and each field says
// which: ABSENT BY OMISSION registers nothing, so a row nothing here could work
// is never queued at all (GmailRegistry, ChannelVault, OverlayVault,
// WebhookRetry.Deliverer, AgentScheduler.Service); REGISTERED ANYWAY keeps the
// worker so a picked-up row fails with an actionable message instead of rotting
// queued (DeepReadBrain, Embedder, VoiceBrain).
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
	// PrivacyRetention carries the GDPR retention dispatcher's cadence
	// (jobs_privacyretention.go).
	PrivacyRetention PrivacyRetentionConfig
	// WebhookRetry carries the retry dispatcher's cadence and the delivery
	// engine one workspace's pass re-attempts through (jobs_webhookretry.go).
	WebhookRetry WebhookRetryConfig
	// AgentScheduler carries the Surface-B dispatcher's cadence and the runner
	// one workspace's pass ticks (jobs_agentscheduler.go).
	AgentScheduler AgentSchedulerConfig
	GmailRegistry  *capture.Registry
	GmailWatch     GmailWatchConfig
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
	// DeepReadTriageBrain serves the domain-triage classification
	// (modelPath.SiteTriage). Nil is a role that cannot classify: a triage read
	// then settles its domain from what the workspace already knows rather than
	// leaving the question open forever.
	DeepReadTriageBrain completer
	// DeepReadCaps bounds each deep-read crawl; the zero value takes the
	// compose defaults (CrawlCaps.withDefaults).
	DeepReadCaps CrawlCaps
	// Blobstore holds the logo bytes a deep read resolves from the site it
	// crawls (A55). Nil is a worker role with no object store: reads still
	// run and still land their facts, and every company keeps the monogram
	// the render layer draws when no logo is on file.
	Blobstore blobstore.Store
	// Embedder is the retrieval embed lane (ModelPath.Embedder) the
	// embed-reindex workspace worker re-embeds under. The workers register
	// regardless of whether this is nil: a picked-up reindex job on a
	// brainless worker role fails clearly (jobs_embedreindex.go)
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

// NewJobRunner wires the deals correctors, the automation time-scan and the
// GDPR retention fan-out into River periodic jobs for the worker process role.
// The intervals keep the operator-facing --close-date-interval /
// --reconcile-interval / --time-scan-interval / --retention-interval flags as
// the schedule source; RunOnStart preserves the old ticker's boot-time first
// pass.
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
	reg := newJobRegistry()
	// The deep read is not periodic — the api enqueues one job per started
	// dossier; the worker role only needs the worker registered. It is also the
	// one kind whose timeout the file cannot state, because the crawl wall it
	// is built from is an operator's (deepReadTimeout).
	addGovernedWorker[SiteDeepReadArgs](reg,
		newSiteDeepReadWorker(pool, cfg.DeepReadBrain, cfg.DeepReadFactBrain, cfg.DeepReadTriageBrain, log, cfg.DeepReadCaps, cfg.Blobstore),
		deepReadTimeout(cfg.DeepReadCaps))
	// The voice build is not periodic — the api enqueues one job per created
	// build; only the deferred-retry sweep ticks. Both register even with a
	// nil brain so a queued build fails actionably instead of rotting.
	addGovernedWorker[VoiceBuildArgs](reg, newVoiceBuildWorker(pool, cfg.VoiceBrain, log), 0)
	addGovernedWorker[VoiceBuildRetryArgs](reg, &voiceBuildRetryWorker{store: ai.NewVoiceStore(pool), log: log}, 0)
	// Each scheduled pass is a dispatcher plus a workspace worker. Only the
	// dispatcher gets a periodic entry below; the workspace worker is enqueued,
	// never ticked.
	addGovernedWorker[CloseDateSweepArgs](reg, &closeDateSweepWorker{pool: pool}, 0)
	addGovernedWorker[CloseDateWorkspaceArgs](reg, &closeDateWorkspaceWorker{corrector: NewCloseDateCorrector(pool, log)}, 0)
	addGovernedWorker[FollowUpReconcileArgs](reg, &followUpReconcileWorker{pool: pool}, 0)
	addGovernedWorker[FollowUpWorkspaceArgs](reg, &followUpWorkspaceWorker{reconciler: NewFollowUpReconciler(pool, log)}, 0)
	addGovernedWorker[TimeScanArgs](reg, &timeScanWorker{pool: pool}, 0)
	addGovernedWorker[TimeScanWorkspaceArgs](reg, &timeScanWorkspaceWorker{scanner: NewTimeScanner(pool, log)}, 0)
	addGovernedWorker[IdempotencyRetentionArgs](reg, &idempotencyRetentionWorker{pool: pool}, 0)
	addGovernedWorker[IdempotencyRetentionWorkspaceArgs](reg, &idempotencyRetentionWorkspaceWorker{sweeper: NewIdempotencyRetentionSweeper(pool, log)}, 0)
	// The Telegram ingest job is not periodic — a poll enqueues one per accepted
	// update in the same transaction as the raw capture row; the worker role only
	// needs the worker registered,
	// same posture as the deep-read and embed-reindex workers. Registered
	// unconditionally: unlike Gmail/Graph, a channel connection carries its
	// own per-connection credential (no deployment-wide OAuth app to gate
	// on), so there is nothing to check for before wiring it up.
	addGovernedWorker[TelegramIngestArgs](reg, newTelegramIngestWorker(pool, cfg.CaptureConfig, log), 0)
	// The embed reindex registers itself the same way, workers only: it is not
	// periodic, because the api enqueues its dispatcher once per confirmed
	// reindex (jobs_embedreindex.go).
	addEmbedReindexJobs(reg, pool, cfg.Embedder)
	// The rate-refresh jobs are not periodic — the api enqueues one per admin
	// "Refresh from sources" click; the worker registers regardless of whether
	// a source is configured (a nil brain / empty url no-ops honestly).
	addGovernedWorker[FxRateRefreshArgs](reg, newFxRefreshWorker(pool, cfg.FxExtractBrain, cfg.FxSourceURL, cfg.FxBootstrapCurrencies, log), 0)
	addGovernedWorker[AiModelRateRefreshArgs](reg, newModelCostRefreshWorker(pool, cfg.RateExtractBrain, cfg.ModelPricingSources, log), 0)
	// The captured-organization auto-enrich sweep (ADR-0072/A118): always
	// registered, it enqueues system deep reads the worker above applies.
	autoEnrich := newCaptureAutoEnrichSweepWorker(pool, log)
	addGovernedWorker[CaptureAutoEnrichSweepArgs](reg, autoEnrich, 0)
	addGovernedWorker[CaptureAutoEnrichWorkspaceArgs](reg, &captureAutoEnrichWorkspaceWorker{sweeper: autoEnrich}, 0)
	// The outbound send is not periodic — the api stages one job per accepted
	// message, in the same transaction as the activity; this role only needs
	// the worker registered.
	if cfg.SendRegistry != nil {
		addGovernedWorker[SendEmailArgs](reg, newSendWorker(pool, cfg.SendRegistry, cfg.SendPacing), 0)
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
	periodic = append(periodic, addGraphJobs(reg, pool, log)...)
	// The ADR-0069 §3a embed drift sweep registers itself the same way
	// (embeddriftsweep.go) — worker + tick only when an embed lane is bound.
	periodic = append(periodic, addEmbedDriftSweepJob(reg, pool, cfg.Embedder, log)...)
	// The GDPR retention pass registers itself the same way
	// (jobs_privacyretention.go).
	periodic = append(periodic, addPrivacyRetentionJobs(reg, pool, cfg, log)...)
	// The outbound-webhook retry sweep likewise (jobs_webhookretry.go).
	periodic = append(periodic, addWebhookRetryJobs(reg, pool, cfg)...)
	// The Surface-B agent scheduler likewise (jobs_agentscheduler.go).
	periodic = append(periodic, addAgentSchedulerJobs(reg, pool, cfg)...)

	if cfg.ClassifyBrain != nil {
		addGovernedWorker[CaptureClassifyArgs](reg, &captureClassifyWorker{pool: pool}, 0)
		addGovernedWorker[CaptureClassifyWorkspaceArgs](reg, &captureClassifyWorkspaceWorker{
			classifier: NewCaptureClassifier(pool, cfg.ClassifyBrain, log),
		}, 0)
		// The hourly catch-up pass (ADR-0063): the nightly suite reruns the
		// same engine; the backlog index makes an empty pass one cheap probe.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureClassifyArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	if cfg.EnrichBrain != nil {
		addGovernedWorker[CaptureEnrichArgs](reg, &captureEnrichWorker{pool: pool}, 0)
		addGovernedWorker[CaptureEnrichWorkspaceArgs](reg, &captureEnrichWorkspaceWorker{
			enricher: NewCaptureEnricher(pool, cfg.EnrichBrain, log),
		}, 0)
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
		addGovernedWorker[OrgNamePromotionArgs](reg, &orgNamePromotionWorker{pool: pool}, 0)
		addGovernedWorker[OrgNamePromotionWorkspaceArgs](reg, &orgNamePromotionWorkspaceWorker{promoter: NewOrgNamePromoter(pool, log)}, 0)
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
		addGovernedWorker[CounterpartyVerdictArgs](reg, &counterpartyVerdictWorker{pool: pool}, 0)
		addGovernedWorker[CounterpartyVerdictWorkspaceArgs](reg, &counterpartyVerdictWorkspaceWorker{engine: verdicts}, 0)
		// Hourly, like classify: the ledger's due-index makes an empty pass one
		// cheap probe, and a deferred sender should not wait a day to become a
		// record. Each workspace's job runs every stage in dependency order —
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
	periodic = registerTelegramPoll(reg, periodic, pool, cfg.ChannelVault, cfg.ChannelAPI, log)

	if cfg.GmailRegistry != nil {
		digests := &captureDigestWorker{registry: cfg.GmailRegistry, pool: pool, log: log}
		addGovernedWorker[CaptureDigestArgs](reg, digests, 0)
		addGovernedWorker[CaptureDigestWorkspaceArgs](reg, &captureDigestWorkspaceWorker{digests: digests}, 0)
		// The digest builds daily after the overnight passes; run-on-start
		// backfills a missed night so mornings are never silently empty.
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return CaptureDigestArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
		addGovernedWorker[GmailSyncArgs](reg, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log}, 0)
		addGovernedWorker[CaptureSyncArgs](reg, &captureSyncWorker{registry: cfg.GmailRegistry, log: log}, 0)
		// Backfill jobs are enqueued by the api (start op); the worker role
		// only needs the pager registered.
		addGovernedWorker[CaptureBackfillArgs](reg, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log}, 0)
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
			addGovernedWorker[GmailWatchArgs](reg, &gmailWatchWorker{
				registry: cfg.GmailRegistry, renewWithin: cfg.GmailWatch.RenewWithin, log: log,
			}, 0)
			addGovernedWorker[GmailWatchRenewArgs](reg, &gmailWatchRenewWorker{
				registry: cfg.GmailRegistry, topic: cfg.GmailWatch.Topic,
			}, 0)
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
		addGovernedWorker[OverlayReconcileArgs](reg, &overlayReconcileWorker{pool: pool, log: log}, 0)
		addGovernedWorker[OverlayReconcileWorkspaceArgs](reg, &overlayReconcileWorkspaceWorker{
			pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log,
			newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit),
		}, 0)
		// The webhook-as-signal re-fetch worker (OVA-WIRE-10): consumes the
		// coalesced OverlayRefetchArgs the receiver enqueues, refreshing one
		// record through the same store the poller uses. Registered whenever
		// the overlay vault is present (the receiver only enqueues when the api
		// role has the app secret wired).
		addGovernedWorker[OverlayRefetchArgs](reg, &overlayRefetchWorker{pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log, newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit)}, 0)
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(cfg.OverlayInterval),
			func() (river.JobArgs, *river.InsertOpts) { return OverlayReconcileArgs{}, sweepInsertOpts() },
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}

	// A kind this role would work but api/jobs.yaml does not declare has no
	// timeout, no attempt cap and no queue anyone chose — it would run at
	// River's silent one-minute default. Refusing the boot is the point: an
	// undeclared kind is indistinguishable from the default this contract
	// exists to remove, and a process that started anyway would hide it.
	if err := jobs.MustBeTotal(reg.kinds); err != nil {
		return nil, err
	}

	return jobs.New(pool, jobs.Config{
		Queues:       jobQueues(),
		Workers:      reg.workers,
		PeriodicJobs: periodic,
	}, log)
}
