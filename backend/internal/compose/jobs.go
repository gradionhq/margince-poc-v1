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
	"slices"
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

// JobRunnerConfig is NewJobRunner's boot configuration: the dials an operator
// sets a pass's cadence with, the model lanes and credential custodians the
// passes work through, and the tuning each pass takes.
//
// WHICH of these a kind needs, and what an absent one costs it, is declared
// per kind in api/jobs.yaml and resolved in jobschedule.go — not counted here.
// A field below says what it IS and what its absence means for the pass that
// reads it; the two postures that absence takes are the declaration's to
// choose, and they are not interchangeable. Registers nothing means a row
// nothing here could work is never queued at all; registers anyway keeps the
// worker so a picked-up row fails with an actionable message instead of
// rotting queued. The SAME field takes opposite postures on different kinds —
// Embedder registers nothing for the drift sweep and anyway for a reindex —
// which is why the posture is stated per kind and never per field.
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

// NewJobRunner wires every worker this process role can run, and every
// schedule that drives one, into a single River runner. Two halves, and they
// are gated separately on purpose:
//
// The WORKERS are a capability. Each block below registers the workers its
// dependency makes workable, so a role wired without a Gmail OAuth app, a
// secret vault or a model lane does not offer to work what it cannot.
//
// The SCHEDULES are the declaration's. Every periodic entry is built by
// periodicFor from api/jobs.yaml — cadence, registration posture and all —
// which is why they read as one list rather than as a schedule hidden inside
// each block. They are leader-elected, so replicas never double-dispatch.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, cfg JobRunnerConfig) (*jobs.Runner, error) {
	reg := newJobRegistry()
	// The deep read is not periodic — the api enqueues one job per started
	// dossier; the worker role only needs the worker registered. It is also the
	// one kind whose timeout the file cannot state, because the crawl wall it
	// is built from is an operator's (deepReadTimeout).
	addDeclaredWorkerWithTimeout[SiteDeepReadArgs](reg,
		newSiteDeepReadWorker(pool, cfg.DeepReadBrain, cfg.DeepReadFactBrain, cfg.DeepReadTriageBrain, log, cfg.DeepReadCaps, cfg.Blobstore),
		deepReadTimeout(cfg.DeepReadCaps))
	// The voice build is not periodic — the api enqueues one job per created
	// build; only the deferred-retry sweep ticks. Both register even with a
	// nil brain so a queued build fails actionably instead of rotting.
	addDeclaredWorker[VoiceBuildArgs](reg, newVoiceBuildWorker(pool, cfg.VoiceBrain, log))
	addDeclaredWorker[VoiceBuildRetryArgs](reg, &voiceBuildRetryWorker{store: ai.NewVoiceStore(pool), log: log})
	// Each scheduled pass is a dispatcher plus a workspace worker. Only the
	// dispatcher gets a periodic entry below; the workspace worker is enqueued,
	// never ticked.
	addDeclaredWorker[CloseDateSweepArgs](reg, &closeDateSweepWorker{pool: pool})
	addDeclaredWorker[CloseDateWorkspaceArgs](reg, &closeDateWorkspaceWorker{corrector: NewCloseDateCorrector(pool, log)})
	addDeclaredWorker[FollowUpReconcileArgs](reg, &followUpReconcileWorker{pool: pool})
	addDeclaredWorker[FollowUpWorkspaceArgs](reg, &followUpWorkspaceWorker{reconciler: NewFollowUpReconciler(pool, log)})
	addDeclaredWorker[TimeScanArgs](reg, &timeScanWorker{pool: pool})
	addDeclaredWorker[TimeScanWorkspaceArgs](reg, &timeScanWorkspaceWorker{scanner: NewTimeScanner(pool, log)})
	addDeclaredWorker[IdempotencyRetentionArgs](reg, &idempotencyRetentionWorker{pool: pool})
	addDeclaredWorker[IdempotencyRetentionWorkspaceArgs](reg, &idempotencyRetentionWorkspaceWorker{sweeper: NewIdempotencyRetentionSweeper(pool, log)})
	// The Telegram ingest job is not periodic — a poll enqueues one per accepted
	// update in the same transaction as the raw capture row; the worker role only
	// needs the worker registered,
	// same posture as the deep-read and embed-reindex workers. Registered
	// unconditionally: unlike Gmail/Graph, a channel connection carries its
	// own per-connection credential (no deployment-wide OAuth app to gate
	// on), so there is nothing to check for before wiring it up.
	addDeclaredWorker[TelegramIngestArgs](reg, newTelegramIngestWorker(pool, cfg.CaptureConfig, log))
	// The embed reindex registers itself the same way, workers only: it is not
	// periodic, because the api enqueues its dispatcher once per confirmed
	// reindex (jobs_embedreindex.go).
	addEmbedReindexJobs(reg, pool, cfg.Embedder)
	// The rate-refresh jobs are not periodic — the api enqueues one per admin
	// "Refresh from sources" click; the worker registers regardless of whether
	// a source is configured (a nil brain / empty url no-ops honestly).
	addDeclaredWorker[FxRateRefreshArgs](reg, newFxRefreshWorker(pool, cfg.FxExtractBrain, cfg.FxSourceURL, cfg.FxBootstrapCurrencies, log))
	addDeclaredWorker[AiModelRateRefreshArgs](reg, newModelCostRefreshWorker(pool, cfg.RateExtractBrain, cfg.ModelPricingSources, log))
	// The captured-organization auto-enrich sweep (ADR-0072/A118): always
	// registered, it enqueues system deep reads the worker above applies.
	autoEnrich := newCaptureAutoEnrichSweepWorker(pool, log)
	addDeclaredWorker[CaptureAutoEnrichSweepArgs](reg, autoEnrich)
	addDeclaredWorker[CaptureAutoEnrichWorkspaceArgs](reg, &captureAutoEnrichWorkspaceWorker{sweeper: autoEnrich})
	// The outbound send is not periodic — the api stages one job per accepted
	// message, in the same transaction as the activity; this role only needs
	// the worker registered.
	if cfg.SendRegistry != nil {
		addDeclaredWorker[SendEmailArgs](reg, newSendWorker(pool, cfg.SendRegistry, cfg.SendPacing))
	}

	if cfg.ClassifyBrain != nil {
		addDeclaredWorker[CaptureClassifyArgs](reg, &captureClassifyWorker{pool: pool})
		addDeclaredWorker[CaptureClassifyWorkspaceArgs](reg, &captureClassifyWorkspaceWorker{
			classifier: NewCaptureClassifier(pool, cfg.ClassifyBrain, log),
		})
	}

	if cfg.EnrichBrain != nil {
		addDeclaredWorker[CaptureEnrichArgs](reg, &captureEnrichWorker{pool: pool})
		addDeclaredWorker[CaptureEnrichWorkspaceArgs](reg, &captureEnrichWorkspaceWorker{
			enricher: NewCaptureEnricher(pool, cfg.EnrichBrain, log),
		})
	}

	// Registered unconditionally: the org-name promotion weighs evidence rows
	// the enrich pass already wrote, so it needs no model. Gating it on a brain
	// would leave an AI-less deployment unable to act on signatures it had
	// already collected.
	addDeclaredWorker[OrgNamePromotionArgs](reg, &orgNamePromotionWorker{pool: pool})
	addDeclaredWorker[OrgNamePromotionWorkspaceArgs](reg, &orgNamePromotionWorkspaceWorker{promoter: NewOrgNamePromoter(pool, log)})

	// Registered unconditionally for a different reason: only the counterparty
	// verdict's JUDGING stage needs a model, and the worker skips that stage
	// when none is configured. Gating the whole worker on a brain would mean an
	// AI-less deployment never staged a review for an existing unsure row and
	// never redacted mail it had already hidden.
	addDeclaredWorker[CounterpartyVerdictArgs](reg, &counterpartyVerdictWorker{pool: pool})
	addDeclaredWorker[CounterpartyVerdictWorkspaceArgs](reg, &counterpartyVerdictWorkspaceWorker{
		engine: NewCounterpartyVerdictEngine(pool, cfg.VerdictBrain, log),
	})

	if cfg.GmailRegistry != nil {
		digests := &captureDigestWorker{registry: cfg.GmailRegistry, pool: pool, log: log}
		addDeclaredWorker[CaptureDigestArgs](reg, digests)
		addDeclaredWorker[CaptureDigestWorkspaceArgs](reg, &captureDigestWorkspaceWorker{digests: digests})
		addDeclaredWorker[GmailSyncArgs](reg, &gmailSyncWorker{registry: cfg.GmailRegistry, log: log})
		// The sync dispatcher scans every registered connector, so a Google
		// Calendar connection (the same Google OAuth app) syncs on the identical
		// per-connection path a mailbox does — there is no gcal-specific job.
		// Per-connection pacing lives in the registry's scheduling sidecar
		// (next_sync_at = success + --gmail-sync-interval), which is why the
		// dispatcher's own cadence can be frequent without meaning frequent
		// provider calls.
		addDeclaredWorker[CaptureSyncArgs](reg, &captureSyncWorker{registry: cfg.GmailRegistry, log: log})
		// Backfill jobs are enqueued by the api (start op); the worker role
		// only needs the pager registered.
		addDeclaredWorker[CaptureBackfillArgs](reg, &captureBackfillWorker{registry: cfg.GmailRegistry, log: log})
		if cfg.GmailWatch.Topic != "" {
			addDeclaredWorker[GmailWatchArgs](reg, &gmailWatchWorker{
				registry: cfg.GmailRegistry, renewWithin: cfg.GmailWatch.RenewWithin, log: log,
			})
			addDeclaredWorker[GmailWatchRenewArgs](reg, &gmailWatchRenewWorker{
				registry: cfg.GmailRegistry, topic: cfg.GmailWatch.Topic,
			})
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
		addDeclaredWorker[OverlayReconcileArgs](reg, &overlayReconcileWorker{pool: pool, log: log})
		addDeclaredWorker[OverlayReconcileWorkspaceArgs](reg, &overlayReconcileWorkspaceWorker{
			pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log,
			newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit),
		})
		// The webhook-as-signal re-fetch worker (OVA-WIRE-10): consumes the
		// coalesced OverlayRefetchArgs the receiver enqueues, refreshing one
		// record through the same store the poller uses. Registered whenever
		// the overlay vault is present (the receiver only enqueues when the api
		// role has the app secret wired).
		addDeclaredWorker[OverlayRefetchArgs](reg, &overlayRefetchWorker{pool: pool, vault: cfg.OverlayVault, ms: ms, meter: meter, log: log, newIncumbent: overlayIncumbentFactory(cfg.OverlayBackfillLimit)})
	}

	periodic := slices.Concat(
		// The passes that register themselves: each helper wires its own
		// workers and hands back the schedules that go with them, so this
		// wiring stays one line as those surfaces grow.
		addGraphJobs(reg, pool, cfg, log),
		addEmbedDriftSweepJob(reg, pool, cfg, log),
		addPrivacyRetentionJobs(reg, pool, cfg, log),
		addWebhookRetryJobs(reg, pool, cfg),
		addAgentSchedulerJobs(reg, pool, cfg),
		registerTelegramPoll(reg, pool, cfg, log),

		// The schedules this file places. Each carries its own gate — the
		// cadence, the configuration it needs, and what an absent dependency
		// costs it — so every one of them is named here whether or not this
		// boot ends up placing it.
		periodicFor(cfg, CloseDateSweepArgs{}),
		periodicFor(cfg, FollowUpReconcileArgs{}),
		periodicFor(cfg, TimeScanArgs{}),
		periodicFor(cfg, VoiceBuildRetryArgs{}),
		periodicFor(cfg, IdempotencyRetentionArgs{}),
		periodicFor(cfg, CaptureAutoEnrichSweepArgs{}),
		periodicFor(cfg, CaptureClassifyArgs{}),
		periodicFor(cfg, CaptureEnrichArgs{}),
		periodicFor(cfg, CounterpartyVerdictArgs{}),
		periodicFor(cfg, OrgNamePromotionArgs{}),
		periodicFor(cfg, CaptureDigestArgs{}),
		periodicFor(cfg, GmailSyncArgs{}),
		periodicFor(cfg, GmailWatchArgs{}),
		periodicFor(cfg, OverlayReconcileArgs{}),
	)

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
