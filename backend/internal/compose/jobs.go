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

	// CloseDateInterval is the deals close-date hygiene sweep's cadence — the
	// operator-facing --close-date-interval. No worker is gated on it: the
	// pass is database-only, so it registers on every role that runs jobs.
	CloseDateInterval time.Duration
	// ReconcileInterval is the overnight follow-up reconciliation's cadence
	// (--reconcile-interval), on the same footing.
	ReconcileInterval time.Duration
	// TimeScanInterval is the automation clock-trigger scan's cadence
	// (--time-scan-interval), on the same footing.
	TimeScanInterval time.Duration
	// PrivacyRetention carries the GDPR retention dispatcher's cadence
	// (jobs_privacyretention.go).
	PrivacyRetention PrivacyRetentionConfig
	// WebhookRetry carries the retry dispatcher's cadence and the delivery
	// engine one workspace's pass re-attempts through (jobs_webhookretry.go).
	WebhookRetry WebhookRetryConfig
	// AgentScheduler carries the Surface-B dispatcher's cadence and the runner
	// one workspace's pass ticks (jobs_agentscheduler.go).
	AgentScheduler AgentSchedulerConfig
	// GmailRegistry is the connector registry every capture pass resolves a
	// connection and its credentials through. Nil is a deployment with no
	// Google OAuth app configured: the sync dispatcher, the per-connection
	// sync, the backfill pager and the morning digest all register nothing,
	// because not one of them can reach a mailbox without it.
	GmailRegistry *capture.Registry
	// GmailWatch carries the push-watch maintenance pass's cadence and the
	// Pub/Sub topic a watch is registered against. An empty Topic is a
	// deployment that never opted into push: the watch pass registers nothing
	// and capture stays on the poll. It is the SECOND field of that pass's
	// conjunction — a registry alone does not wire it.
	GmailWatch GmailWatchConfig
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
	VerdictBrain completer
	// SignalExtractBrain is the signal-extract lane the hourly signal pass
	// reads settled conversations with. Nil = no AI configured, and the
	// consequence is stated rather than hidden: the deterministic
	// ghosted-thread rule still runs, and what only a reader can get out of
	// the prose simply is not extracted. The kind registers either way, which
	// is why nothing in api/jobs.yaml gates on this field.
	SignalExtractBrain completer
	// OverlayVault is the custodian of an incumbent connection's sealed token.
	// Nil is a role with no way to unseal one, so the reconcile poller and the
	// webhook-as-signal re-fetch worker register nothing rather than queue
	// sweeps that could only fail at their first credential read.
	OverlayVault keyvault.Vault
	// OverlayInterval is the reconcile poller's cadence — the operator-facing
	// --overlay-reconcile-interval. It paces the due-SCAN and not one tenant's
	// sweep: the per-workspace pacing lives in overlay_sync_state.next_sweep_at,
	// which the scan is gated on, so a frequent poll does not mean frequent
	// incumbent calls.
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
	// Embedder is the retrieval embed lane (ModelPath.Embedder) both embed
	// passes re-embed under, and it is the one field whose two kinds take
	// OPPOSITE postures — for a reason that is about who enqueues them.
	//
	// The REINDEX workers register even with a nil embedder, DeepReadBrain's
	// posture: a human's confirm at the transport is what enqueues a reindex,
	// so a row can already be waiting, and it should fail clearly
	// (jobs_embedreindex.go) rather than sit queued behind a job no one works.
	// The DRIFT sweep registers nothing, because nothing but its own tick ever
	// enqueues it: there is no queued row to fail loudly on, and a tick that
	// could only ever enqueue failures is noise rather than a signal.
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
	// BoundModelIDs maps a provider to the model ids this deployment's routing
	// binds on it, so each pricing source is narrowed to its OWN provider's
	// bindings. Nil (nothing wired) keeps every model.
	BoundModelIDs map[string]map[string]bool
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
	reg, periodic := wireJobs(pool, log, cfg)

	// A kind this role would work but api/jobs.yaml does not declare has no
	// timeout, no attempt cap and no queue anyone chose — it would run at
	// River's silent one-minute default. Refusing the boot is the point: an
	// undeclared kind is indistinguishable from the default this contract
	// exists to remove, and a process that started anyway would hide it.
	if err := jobs.MustBeTotal(reg.kinds); err != nil {
		return nil, err
	}
	// Totality says every kind this role works is declared; it does not say
	// each is worked by the args type its declaration names. A Kind() that
	// returns another declared kind's string passes the check above and runs
	// under that kind's timeout, queue and attempt cap.
	if err := reg.everyKindIsRegisteredWithItsDeclaredType(); err != nil {
		return nil, err
	}

	return jobs.New(pool, jobs.Config{
		Queues:       jobQueues(),
		Workers:      reg.workers,
		PeriodicJobs: periodic,
	}, log)
}

// wireJobs is NewJobRunner's assembly with the River client left off the end.
// It is separate because the census builds the same wiring in order to READ it
// — which kinds this configuration registers, under which args types, behind
// which workers — and a client would need a live pool for a question that has
// nothing to do with a database.
func wireJobs(pool *pgxpool.Pool, log *slog.Logger, cfg JobRunnerConfig) (*jobRegistry, []*river.PeriodicJob) {
	reg := newJobRegistry()
	// The workers, in the groups their gating and their concern make: each
	// helper registers its own and states what an absent dependency costs
	// them. The schedules that drive them are the list below, because a
	// cadence is the declaration's and not the group's.
	addModelLaneJobs(reg, pool, cfg, log)
	addDatabaseOnlySweepJobs(reg, pool, log)
	addCapturePipelineJobs(reg, pool, cfg, log)
	addGmailCaptureJobs(reg, pool, cfg, log)
	addOverlayJobs(reg, pool, cfg, log)

	periodic := slices.Concat(
		// The passes that register themselves: each helper wires its own
		// workers and hands back the schedules that go with them, so this
		// wiring stays one line as those surfaces grow.
		addGraphJobs(reg, pool, cfg, log),
		addEmbedDriftSweepJob(reg, pool, cfg, log),
		addPrivacyRetentionJobs(reg, pool, cfg, log),
		addWebhookRetryJobs(reg, pool, cfg),
		addAgentSchedulerJobs(reg, pool, cfg),
		addSignalJobs(reg, pool, cfg, log),
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
		periodicFor(cfg, AgentTaskRetentionArgs{}),
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

	return reg, periodic
}

// addModelLaneJobs registers the kinds whose work is a model call: the site
// deep read, the voice build, the embed reindex, and the two refreshes that
// extract rates from a page. The deferred-build retry sweep rides with them
// because it fans out to the voice build and to nothing else.
//
// Not one of them is gated on the lane it reads, and that shared posture is
// what makes them a group. Something outside the runner enqueues every model
// call here — an api call, a human's confirm, an admin's refresh click, the
// retry sweep's own fan-out — so a row can already be waiting when a role with
// no lane configured comes up, and registering anyway is what makes that row
// fail with an actionable message instead of sitting queued behind a job
// nothing works. The embed DRIFT sweep takes the opposite posture for the
// opposite reason (embeddriftsweep.go): nothing but its own tick enqueues it,
// so there is no waiting row for a worker to answer.
func addModelLaneJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) {
	// The deep read is the one kind whose timeout the file cannot state,
	// because the crawl wall it is built from is an operator's (deepReadTimeout).
	addDeclaredWorkerWithTimeout[SiteDeepReadArgs](reg,
		newSiteDeepReadWorker(pool, cfg.DeepReadBrain, cfg.DeepReadFactBrain, cfg.DeepReadTriageBrain, log, cfg.DeepReadCaps, cfg.Blobstore),
		deepReadTimeout(cfg.DeepReadCaps))
	addDeclaredWorker[VoiceBuildArgs](reg, newVoiceBuildWorker(pool, cfg.VoiceBrain, log))
	addDeclaredWorker[VoiceBuildRetryArgs](reg, &voiceBuildRetryWorker{store: ai.NewVoiceStore(pool), log: log})
	// The reindex is a dispatcher plus a workspace worker, and neither is
	// ticked: the api enqueues the dispatcher once per confirmed reindex
	// (jobs_embedreindex.go).
	addEmbedReindexJobs(reg, pool, cfg.Embedder)
	// Both refreshes read a source the deployment configures. An unconfigured
	// one — a nil brain, an empty url, no pricing sources — leaves the worker
	// registered and its producer proposing nothing, which is the honest
	// answer to "refresh from sources" when there are none.
	addDeclaredWorker[FxRateRefreshArgs](reg, newFxRefreshWorker(pool, cfg.FxExtractBrain, cfg.FxSourceURL, cfg.FxBootstrapCurrencies, log))
	addDeclaredWorker[AiModelRateRefreshArgs](reg, newModelCostRefreshWorker(pool, cfg.RateExtractBrain, cfg.ModelPricingSources, cfg.BoundModelIDs, log))
}

// addDatabaseOnlySweepJobs registers the periodic passes that need nothing but
// the database: the deals close-date hygiene, the follow-up reconcile, the
// automation clock scan, and the idempotency-key retention sweep.
//
// It takes no JobRunnerConfig, and that is the group rather than an economy of
// arguments — there is no lane, credential or registry any of these could be
// gated on, so every role that runs jobs runs all four. Each is a dispatcher
// plus a workspace worker: only the dispatcher is ticked (the schedules in
// wireJobs), and the workspace worker is enqueued by it, never scheduled.
func addDatabaseOnlySweepJobs(reg *jobRegistry, pool *pgxpool.Pool, log *slog.Logger) {
	addDeclaredWorker[CloseDateSweepArgs](reg, &closeDateSweepWorker{pool: pool})
	addDeclaredWorker[CloseDateWorkspaceArgs](reg, &closeDateWorkspaceWorker{corrector: NewCloseDateCorrector(pool, log)})
	addDeclaredWorker[FollowUpReconcileArgs](reg, &followUpReconcileWorker{pool: pool})
	addDeclaredWorker[FollowUpWorkspaceArgs](reg, &followUpWorkspaceWorker{reconciler: NewFollowUpReconciler(pool, log)})
	addDeclaredWorker[TimeScanArgs](reg, &timeScanWorker{pool: pool})
	addDeclaredWorker[TimeScanWorkspaceArgs](reg, &timeScanWorkspaceWorker{scanner: NewTimeScanner(pool, log)})
	addDeclaredWorker[IdempotencyRetentionArgs](reg, &idempotencyRetentionWorker{pool: pool})
	addDeclaredWorker[IdempotencyRetentionWorkspaceArgs](reg, &idempotencyRetentionWorkspaceWorker{sweeper: NewIdempotencyRetentionSweeper(pool, log)})
	addDeclaredWorker[AgentTaskRetentionArgs](reg, &agentTaskRetentionWorker{pool: pool})
	addDeclaredWorker[AgentTaskRetentionWorkspaceArgs](reg, &agentTaskRetentionWorkspaceWorker{sweeper: NewAgentTaskRetentionSweeper(pool, log)})
}
