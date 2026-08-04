// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command worker is the background process role (ADR-0054, amended §2):
// the standalone outbox relay for split deployments — cmd/api runs the
// same relay inline by default (--inline-relay), so small installs never
// need this binary — plus the Surface-B runner scheduler when a brain is
// declared: catalog seeding, due-job execution, and the
// approval-decided resume subscriber.
package main

import (
	"cmp"
	// Embedded tzdata: workspace timezones must resolve on scratch
	// containers that ship no zoneinfo.
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	// The composed extension set (ADR-0069): the generated module under
	// build/composition/ in a composed build, the committed vanilla stub
	// in a bare one — same import path either way.
	"github.com/gradionhq/margince/composition"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	// `worker siteread …` is the DB-less deep-read debug loop
	// (siteread.go) — dispatched before the worker flags, which would
	// otherwise demand a DSN the subcommand never uses.
	if len(args) > 0 && args[0] == "siteread" {
		return runSiteReadDebug(ctx, args[1:], stdout)
	}
	// `worker aitask …` is the DB-less AI-task probe (aitask.go), dispatched
	// for the same reason as siteread above.
	if len(args) > 0 && args[0] == "aitask" {
		return runAITaskProbe(ctx, args[1:], stdout)
	}

	cfg, err := parseWorkerFlags(args)
	if err != nil {
		return err
	}

	// Register the composed extension set before anything runs; a
	// failing registration aborts the boot (ADR-0069 EXT-P4). ONE
	// snapshot serves registration and the boot inventory below, so both
	// observe the same declarations.
	extensions := composition.Extensions()
	if err := compose.RegisterExtensions(extensions); err != nil {
		return err
	}
	// The worker reads the same deployment file the api boots from: the
	// capture pipeline tuning (capture.freemail_extra) and the operator's
	// ai.capture_payloads posture the Surface-B runner honors. A missing
	// file means defaults; a malformed one is a boot error (a typo must
	// not silently drop the blocklist or flip the payload posture).
	deployCfg, err := deployconfig.Load(cfg.configPath)
	if err != nil {
		return err
	}
	cfg.ratesFx = deployCfg.Rates.Fx
	cfg.ratesCurrencies = deployCfg.Rates.FxCurrencies
	cfg.ratesModelPricing = deployCfg.Rates.ModelPricing

	handler, err := httpserver.LogHandler(stdout, cfg.logLevel, cfg.logFormat)
	if err != nil {
		return err
	}
	logger := slog.New(httpserver.WithCorrelation(handler))
	// Set after the logger exists: the capture config carries it to the Sink's
	// post-commit steps, where a fault is reported rather than returned.
	cfg.captureConfig = compose.CaptureConfigFromDeploy(deployCfg.Capture, logger)

	pool, err := database.NewPool(ctx, cfg.dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Record the composed extension set when it changed since the last
	// boot (ADR-0069 §5); pre-bootstrap it skips — the api records the
	// first observation once it has bootstrapped the installation.
	if err := compose.ObserveExtensionInventory(ctx, pool, logger, extensions); err != nil {
		return err
	}

	rdb, err := events.NewClient(ctx, cfg.redisAddr)
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			logger.Warn("closing bus client", "err", err)
		}
	}()

	//nolint:contextcheck // boot-time wiring: the model path outlives any request context (cmd/api resolves the same path under the same waiver)
	modelPath, boundModels, err := selectModelPath(modelPathSpec{
		routingPath:     cfg.routingPath,
		fake:            cfg.fakeBrain,
		capturePayloads: deployCfg.AI.CapturePayloads,
	}, pool, logger)
	if err != nil {
		return err
	}

	// The Surface-B runner is this role's sending lane, and it stages through
	// the SAME delivery machinery the api does — built before the lane so it
	// cannot be composed without one. Insert-only, like the api's: the staged
	// job is worked by this role's own River runner (startJobRunner below), and
	// a lane that staged onto the runner it is itself being wired into would
	// need the runner to exist before the lanes do.
	sendInserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return err
	}
	send := sendPath(cfg, compose.NewDeliveryStager(pool, sendInserter))

	// Every background lane joins the WaitGroup so run() returns only
	// after in-flight handlers finish their ack — the same shape as
	// cmd/api's relay group; a bare goroutine would be killed mid-handler
	// when the relay returns.
	var background sync.WaitGroup
	// Declared out here because the job runner below schedules the SAME
	// service the resume subscriber consumes into: one governed registry and
	// one brain per role, never two that could drift apart.
	var runnerSvc *compose.RunnerService
	if modelPath.AgentLoop != nil {
		grounding := search.NewRetriever(search.NewStore(pool), modelPath.Embedder)
		// The Surface-B runner's agent tools reach overlay write-back through
		// the workspace's own vaulted incumbent token; wire the FromEnv
		// vault-backed resolver so an autonomous run can write back (nil vault
		// → clean errNoWriteIncumbent, never a crash).
		runnerVault, _, rverr := keyvault.FromEnv(pool)
		if rverr != nil {
			return rverr
		}
		runnerSvc = compose.NewRunnerService(pool, modelPath.AgentLoop, modelPath.DraftReply, grounding, logger, compose.OverlayIncumbentResolver(pool, runnerVault), send)
		_, _ = fmt.Fprintln(stdout, "worker resuming approved Surface-B runs (cg:overnight-agent)")
		background.Go(func() { runResumeSubscriber(ctx, rdb, runnerSvc, logger) })
	}
	if modelPath.Embedder != nil {
		gen := search.NewEmbedGen(search.NewStore(pool), modelPath.Embedder)
		_, _ = fmt.Fprintln(stdout, "worker maintaining retrieval embeddings")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:context-graph", gen.HandleEvent, logger, 0) })
	}
	// The interaction-edge projection (ADR-0078). Unlike embeddings it needs no
	// model, so it runs on every worker rather than only where a provider is
	// configured — a deployment without AI still answers "who on our team knows
	// this contact", which is a deterministic question about our own mail.
	{
		edges := search.NewGraphEdgeGen(search.NewStore(pool))
		_, _ = fmt.Fprintln(stdout, "worker maintaining interaction edges")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:graph-edge", edges.HandleEvent, logger, 0) })
	}

	// The LinkedIn ghost matcher (ADR-0078 §8b): a ghost attaches the moment
	// its contact exists, whoever created them. Deterministic like the edge
	// projection above, so it runs on every worker.
	{
		matcher := compose.NewLinkedInMatchGen(pool, people.NewStore(pool), identity.NewService(pool), logger)
		_, _ = fmt.Fprintln(stdout, "worker matching LinkedIn connections as contacts appear")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:linkedin-match", matcher.HandleEvent, logger, 0) })
	}

	blob, blobConfigured, err := blobstore.FromEnv(ctx)
	if err != nil {
		return fmt.Errorf("worker: blobstore: %w", err)
	}
	if blobConfigured {
		_, _ = fmt.Fprintln(stdout, "worker storing site-read logos and erasing attachment objects (blobstore configured)")
	}
	if err := backfillConnectorCredentials(ctx, pool, stdout, logger); err != nil {
		return err
	}

	// ONE deliverer serves both outbound-webhook lanes (E10/S-E10.6): the
	// cg:webhooks consumer started here and the River retry sweep the runner
	// below schedules. That shared instance is why it is built before the
	// runner rather than after it.
	var webhookDeliverer *webhooks.Deliverer
	if cfg.webhookKey != "" {
		webhookDeliverer, err = compose.NewWebhookDeliverer(pool, cfg.webhookKey, logger)
		if err != nil {
			return fmt.Errorf("worker: %w", err)
		}
		_, _ = fmt.Fprintln(stdout, "worker delivering outbound webhooks (cg:webhooks)")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:webhooks", webhookDeliverer.HandleEvent, logger, 0) })
	}

	stopJobs, err := startJobRunner(ctx, pool, rdb, compose.OverlayBudgetConfig(deployCfg.EffectiveOverlayBudget()), logger, cfg, modelPath, boundModels, blob, webhookDeliverer, runnerSvc, stdout)
	if err != nil {
		return err
	}
	defer stopJobs()

	workflows := compose.NewWorkflowEngineWithReplyDraft(pool, modelPath.DraftReply)
	_, _ = fmt.Fprintln(stdout, "worker dispatching workflows (cg:workflows)")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:workflows", workflows.HandleEvent, logger, 0) })

	_, _ = fmt.Fprintf(stdout, "worker relaying outbox events to %s\n", cfg.redisAddr)
	// Run until signalled; unshipped rows wait durably in the outbox for
	// the next boot — shutdown loses no events.
	events.NewRelay(pool, rdb, logger).Run(ctx)
	background.Wait()
	return nil
}

// backfillConnectorCredentials migrates any legacy capture_connection rows
// whose credential still lives in the auth bytea column onto the keyvault.
// It runs once at boot when a vault is configured and is
// idempotent — a row already carrying a credential_ref is skipped — so
// re-running every boot is safe and a no-op once every row is migrated.
// Without a vault it is skipped: the legacy auth column still resolves
// credentials until one is provisioned. A malformed root key fails the boot
// (keyvault.FromEnv); a mid-backfill failure is logged and non-fatal — capture
// keeps resolving from the auth column and the next boot retries.
func backfillConnectorCredentials(ctx context.Context, pool *pgxpool.Pool, stdout io.Writer, logger *slog.Logger) error {
	vault, configured, err := keyvault.FromEnv(pool)
	if err != nil {
		return fmt.Errorf("worker: keyvault: %w", err)
	}
	if !configured {
		return nil
	}
	migrated, err := compose.NewCaptureRegistry(pool, vault, compose.CaptureConfig{}).BackfillCredentials(ctx)
	if err != nil {
		logger.Error("connector-credential backfill did not complete; capture continues from the legacy column and the next boot retries", "err", err)
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "worker keyvault configured; migrated %d legacy connector credential(s) onto the vault\n", migrated)
	return nil
}

// startJobRunner boots the River periodic jobs: River
// gives leader election (one run cluster-wide, so worker replicas never
// double-sweep the close-date and reconcile passes), retries, and graceful
// drain — what the bare tickers lacked. The domain logic (Sweep/Reconcile)
// is unchanged; only the scheduler is River now. The returned stop function
// drains in-flight jobs on shutdown.
// gmailWatchConfig builds the Gmail push-watch maintenance config: the
// watch job runs only where a Pub/Sub topic is configured AND the Gmail
// app is wired (gmailWired); otherwise capture stays on the poll and the
// topic is left empty.
func gmailWatchConfig(cfg workerConfig, gmailWired bool) compose.GmailWatchConfig {
	w := compose.GmailWatchConfig{
		Interval:    cfg.gmailWatchInterval,
		RenewWithin: cfg.gmailWatchRenew,
	}
	if gmailWired {
		w.Topic = cfg.gmailPubsubTopic
	}
	return w
}

func startJobRunner(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, overlayBudget overlaybudget.Config, logger *slog.Logger, cfg workerConfig, modelPath compose.ModelPath, boundModels map[string]map[string]bool, blob blobstore.Store, webhookDeliverer *webhooks.Deliverer, runnerSvc *compose.RunnerService, stdout io.Writer) (func(), error) {
	// The sweep registry is always live — the standing IMAP connector needs
	// no deployment config; gmail joins it when the OAuth app is configured.
	// The vault holds every connection's sealed credential (the standing
	// flavors resolve through it), so it initializes here regardless. The
	// SAME vault is the credential custodian of the two pollers this role
	// runs — the overlay reconcile (the only one that can resolve a connected
	// workspace's sealed HubSpot token, overlay.DueOverlayConnections'
	// CredentialRef) and the Telegram getUpdates poll (a bot's sealed token) —
	// resolved once, shared; when it is not configured, configuredVault is nil
	// so an unconfigured deployment never fails worker boot over pollers it has
	// no connected workspace to run anyway.
	vault, vaultConfigured, verr := keyvault.FromEnv(pool)
	if verr != nil {
		return nil, fmt.Errorf("worker: keyvault: %w", verr)
	}
	captureReg := compose.CaptureSyncRegistry(pool, vault, compose.GmailConfig{
		ClientID:     cfg.gmailClientID,
		ClientSecret: cfg.gmailClientSecret,
	}, compose.GraphConfig{
		ClientID:     cfg.graphClientID,
		ClientSecret: cfg.graphClientSecret,
		Tenant:       cfg.graphTenant,
	}, cfg.captureConfig).WithSyncInterval(cfg.gmailSyncInterval)
	watchCfg := gmailWatchConfig(cfg, cfg.gmailAppWired())
	configuredVault := vault
	if !vaultConfigured {
		configuredVault = nil
	}

	runner, err := compose.NewJobRunner(pool, logger, compose.JobRunnerConfig{
		// The registry that resolves a staged delivery's mailbox: the SAME
		// sweep registry the capture polls use, so the connector set that
		// syncs a mailbox is the one that transmits from it.
		SendRegistry: captureReg,
		SendPacing: compose.SendPacing{
			Limit: cfg.sendRateLimit, Window: cfg.sendRateWindow, MaxAge: cfg.sendMaxAge,
		},
		CloseDateInterval: cfg.closeDateInterval,
		ReconcileInterval: cfg.reconcileInterval,
		TimeScanInterval:  cfg.timeScanInterval,
		// The GDPR retention fan-out's cadence: --retention-interval is the
		// schedule source, now read by River rather than by a ticker.
		PrivacyRetention: compose.PrivacyRetentionConfig{Interval: cfg.retentionInterval},
		// A nil deliverer (no --webhook-key) registers neither half.
		WebhookRetry: compose.WebhookRetryConfig{
			Interval: cfg.webhookRetryInterval, Deliverer: webhookDeliverer,
		},
		// A nil service (no declared model) registers neither half.
		AgentScheduler: compose.AgentSchedulerConfig{
			Interval: cfg.runnerInterval, Service: runnerSvc,
		},
		GmailRegistry: captureReg,
		GmailWatch:    watchCfg,
		// The Telegram ingest worker builds its Sink from this — the same
		// suppression-list config every other capture path shares.
		CaptureConfig: cfg.captureConfig,
		// Telegram ingress pulls, so the WORKER role owns it end to end: the
		// dispatcher and the poll jobs both need the vault that holds each bot's
		// sealed token. Without a configured vault there is no token to unseal
		// and the poller stays off by omission, the same posture the overlay
		// poller takes one field below.
		ChannelVault: configuredVault,
		// The classify + enrich passes run only where a model is
		// configured; without one both are absent by omission.
		ClassifyBrain:        modelPath.CaptureClassify,
		VerdictBrain:         modelPath.CaptureCounterpartyVerdict,
		EnrichBrain:          modelPath.Enrich,
		SignalExtractBrain:   modelPath.SignalExtract,
		OverlayVault:         configuredVault,
		OverlayInterval:      cfg.overlayInterval,
		OverlayBackfillLimit: cfg.overlayBackfillLimit,
		// The poller's OVB meter records against the SAME Redis the relay
		// uses (rdb) so the worker's poller spend and the api's force-fresh
		// spend land on one shared per-workspace-per-incumbent count. Built
		// here in cmd (the raw-Redis dependency stays out of compose).
		OverlayMeter: overlaybudget.New(rdb, overlayBudget),
		// The deep-read worker registers regardless: without a model path
		// (nil SiteExtract) it fails a picked-up read honestly rather than
		// leaving it queued behind a job no one can work.
		DeepReadBrain:       modelPath.SiteExtract,
		DeepReadFactBrain:   modelPath.SiteFactExtract,
		DeepReadTriageBrain: modelPath.SiteTriage,
		// Same posture for the voice build: the worker registers with or
		// without a model, failing picked-up builds actionably when brainless.
		VoiceBrain: modelPath.VoiceBuild,
		// The rate-refresh producers register regardless; without a source
		// (empty FX url / no pricing sources) or a model (nil RateExtract)
		// they no-op honestly. FX and model-cost both extract from a fetched
		// page via the shared RateExtract lane.
		RateExtractBrain:      modelPath.RateExtract,
		FxSourceURL:           cmp.Or(cfg.ratesFx, "https://api.frankfurter.dev/v1/latest"),
		FxBootstrapCurrencies: fxBootstrapCurrencies(cfg.ratesCurrencies),
		FxExtractBrain:        modelPath.RateExtract,
		ModelPricingSources:   compose.PricingSourcesFromMap(cfg.ratesModelPricing),
		BoundModelIDs:         boundModels,
		DeepReadCaps: compose.CrawlCaps{
			MaxPages: cfg.deepReadMaxPages,
			MaxBytes: cfg.deepReadMaxBytes,
			Wall:     cfg.deepReadWall,
		},
		// The same object store retention purges from: a deep read resolves
		// the company's logo out of the site it just crawled and stores the
		// normalized bytes here. Nil (no blobstore configured) leaves every
		// company on its monogram — the read itself is unaffected.
		Blobstore: blob,
		// The embed-reindex worker registers regardless: without an embed
		// lane (nil Embedder) a picked-up job fails clearly rather than
		// sitting queued forever behind a job no one can work — the same
		// posture as DeepReadBrain above.
		Embedder: modelPath.Embedder,
	})
	if err != nil {
		return nil, err
	}
	if err := runner.Start(ctx); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintln(stdout, jobRunnerBanner(cfg, watchCfg, modelPath, configuredVault, runnerSvc))
	return func() {
		// The run context is already cancelled at shutdown, so give the
		// drain its own bounded window.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			logger.Warn("stopping job runner", "err", err)
		}
	}, nil
}

// runResumeSubscriber consumes cg:overnight-agent: approval decisions
// wake parked runs.
//
// Its reclaim window has to clear the whole run, not the default: this
// handler resumes a multi-step agent loop that may take the full
// RunWallClock, and reclaiming a merely-slow consumer would hand the same
// decision to a peer replica while the first is still running it. The run
// row's own claim already refuses the second resume; keeping the window
// above the handler's honest runtime means the bus stops producing the
// duplicate in the first place.
func runResumeSubscriber(ctx context.Context, rdb *redis.Client, svc *compose.RunnerService, log *slog.Logger) {
	runSubscriber(ctx, rdb, "cg:overnight-agent", svc.HandleEvent, log, compose.RunWallClock+time.Minute)
}

// runSubscriber consumes one events.md consumer group, Dedupe-wrapped
// because the bus is at-least-once (events.md §3). minIdle overrides the
// reclaim window for a group whose handler runs longer than the default;
// zero keeps it.
func runSubscriber(ctx context.Context, rdb *redis.Client, groupName string, handler events.Handler, log *slog.Logger, minIdle time.Duration) {
	var group kevents.Group
	for _, g := range kevents.Groups() {
		if g.Name == groupName {
			group = g
		}
	}
	sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, handler), log).WithMinIdle(minIdle)
	if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("subscriber "+groupName, "err", err)
	}
}

// envOr reads an environment variable with an explicit default, keeping
// flag definitions self-documenting.
