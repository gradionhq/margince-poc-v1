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
	"github.com/gradionhq/margince/backend/internal/modules/ai"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
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
	cfg.freemailExtra = deployCfg.Capture.FreemailExtra

	handler, err := httpserver.LogHandler(stdout, cfg.logLevel, cfg.logFormat)
	if err != nil {
		return err
	}
	logger := slog.New(httpserver.WithCorrelation(handler))

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

	modelPath, err := selectModelPath(cfg.routingPath, cfg.fakeBrain, deployCfg.AI.CapturePayloads, pool, logger)
	if err != nil {
		return err
	}

	// Every background lane joins the WaitGroup so run() returns only
	// after in-flight handlers finish their ack — the same shape as
	// cmd/api's relay group; a bare goroutine would be killed mid-handler
	// when the relay returns.
	var background sync.WaitGroup
	if modelPath.Agent != nil {
		grounding := search.NewRetriever(search.NewStore(pool), modelPath.Embedder)
		svc := compose.NewRunnerService(pool, modelPath.Agent, modelPath.DraftReply, grounding, logger)
		_, _ = fmt.Fprintf(stdout, "worker running the Surface-B scheduler every %s\n", cfg.runnerInterval)
		background.Go(func() { runScheduler(ctx, svc, cfg.runnerInterval, logger) })
		background.Go(func() { runResumeSubscriber(ctx, rdb, svc, logger) })
	}
	if modelPath.Embedder != nil {
		gen := search.NewEmbedGen(search.NewStore(pool), modelPath.Embedder)
		_, _ = fmt.Fprintln(stdout, "worker maintaining retrieval embeddings")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:context-graph", gen.HandleEvent, logger) })
	}

	blob, blobConfigured, err := blobstore.FromEnv(ctx)
	if err != nil {
		return fmt.Errorf("worker: blobstore: %w", err)
	}
	if blobConfigured {
		_, _ = fmt.Fprintln(stdout, "worker retention erasing attachment objects (blobstore configured)")
	}
	retention := privacy.NewRetentionService(pool, blob, logger)
	_, _ = fmt.Fprintf(stdout, "worker evaluating retention every %s\n", cfg.retentionInterval)
	background.Go(func() { privacy.RunRetention(ctx, retention, cfg.retentionInterval, logger) })

	if err := backfillConnectorCredentials(ctx, pool, stdout, logger); err != nil {
		return err
	}

	stopJobs, err := startJobRunner(ctx, pool, rdb, compose.OverlayBudgetConfig(deployCfg.EffectiveOverlayBudget()), logger, cfg, modelPath, stdout)
	if err != nil {
		return err
	}
	defer stopJobs()

	workflows := compose.NewWorkflowEngineWithReplyDraft(pool, modelPath.DraftReply)
	_, _ = fmt.Fprintln(stdout, "worker dispatching workflows (cg:workflows)")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:workflows", workflows.HandleEvent, logger) })

	// Outbound-webhook delivery (E10/S-E10.6) runs only when a signing key
	// is configured: it consumes cg:webhooks to fan matching events to
	// subscribers — owner-scoped, so a webhook never delivers an event its
	// owner may not see (BYO-EVT-4) — and sweeps due retries on a ticker.
	// Without the key the delivery worker stays off entirely.
	if cfg.webhookKey != "" {
		deliverer, err := compose.NewWebhookDeliverer(pool, cfg.webhookKey, logger)
		if err != nil {
			return fmt.Errorf("worker: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "worker delivering outbound webhooks (cg:webhooks), retry sweep every %s\n", cfg.webhookRetryInterval)
		background.Go(func() { runSubscriber(ctx, rdb, "cg:webhooks", deliverer.HandleEvent, logger) })
		background.Go(func() { deliverer.RunRetrySweep(ctx, cfg.webhookRetryInterval) })
	}

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
	migrated, err := compose.NewCaptureRegistry(pool, vault).BackfillCredentials(ctx)
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

func startJobRunner(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, overlayBudget overlaybudget.Config, logger *slog.Logger, cfg workerConfig, modelPath compose.ModelPath, stdout io.Writer) (func(), error) {
	// The sweep registry is always live — the standing IMAP connector needs
	// no deployment config; gmail joins it when the OAuth app is configured.
	// The vault holds every connection's sealed credential (the standing
	// flavors resolve through it), so it initializes here regardless. The
	// SAME vault is the overlay reconcile poller's credential custodian
	// (the only one that can resolve a connected workspace's sealed HubSpot
	// token, overlay.DueOverlayConnections' CredentialRef) — resolved once,
	// shared; when it is not configured, overlayVault is nil so an
	// unconfigured deployment never fails worker boot over a poller it has
	// no connected overlay workspace to run anyway.
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
	}, cfg.freemailExtra...).WithSyncInterval(cfg.gmailSyncInterval)
	gmailWired := cfg.gmailClientID != "" && cfg.gmailClientSecret != ""
	watchCfg := gmailWatchConfig(cfg, gmailWired)
	overlayVault := vault
	if !vaultConfigured {
		overlayVault = nil
	}

	runner, err := compose.NewJobRunner(pool, logger, compose.JobRunnerConfig{
		CloseDateInterval: cfg.closeDateInterval,
		ReconcileInterval: cfg.reconcileInterval,
		TimeScanInterval:  cfg.timeScanInterval,
		GmailRegistry:     captureReg,
		GmailWatch:        watchCfg,
		// The classify + enrich passes run only where a model is
		// configured; without one both are absent by omission.
		ClassifyBrain:        modelPath.CaptureClassify,
		EnrichBrain:          modelPath.SignatureEnrich,
		OverlayVault:         overlayVault,
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
		DeepReadBrain:     modelPath.SiteExtract,
		DeepReadFactBrain: modelPath.SiteFactExtract,
		DeepReadCaps: compose.CrawlCaps{
			MaxPages: cfg.deepReadMaxPages,
			MaxBytes: cfg.deepReadMaxBytes,
			Wall:     cfg.deepReadWall,
		},
	})
	if err != nil {
		return nil, err
	}
	if err := runner.Start(ctx); err != nil {
		return nil, err
	}
	providers := "imap"
	if gmailWired {
		providers += "+gmail"
	}
	if cfg.graphClientID != "" && cfg.graphClientSecret != "" {
		providers += "+graph"
	}
	captureNote := fmt.Sprintf("capture sweep every %s: %s", cfg.gmailSyncInterval, providers)
	switch {
	case gmailWired && watchCfg.Topic != "":
		captureNote = fmt.Sprintf("capture sweep every %s: %s, watch renew every %s", cfg.gmailSyncInterval, providers, cfg.gmailWatchInterval)
	case gmailWired:
		captureNote = fmt.Sprintf("capture sweep every %s: %s (watch off: no pubsub topic)", cfg.gmailSyncInterval, providers)
	}
	overlayNote := "overlay reconcile off (no keyvault configured)"
	if overlayVault != nil {
		overlayNote = fmt.Sprintf("overlay reconcile every %s", cfg.overlayInterval)
	}
	deepReadNote := "deep read on"
	if modelPath.SiteExtract == nil {
		deepReadNote = "deep read degraded: no model path, queued reads will fail (configure --ai-routing)"
	}
	_, _ = fmt.Fprintf(stdout, "worker running River jobs (close-date every %s, reconcile every %s, time-scan every %s, %s, %s, %s)\n",
		cfg.closeDateInterval, cfg.reconcileInterval, cfg.timeScanInterval, captureNote, overlayNote, deepReadNote)
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

// selectModelPath resolves the model path: a routing config for real
// deployments, the offline fake behind an explicit dev flag, or the
// zero path — the runner and the embed lane simply don't start without
// a declared model; nothing is picked silently.
func selectModelPath(routingPath string, fake, capturePayloads bool, pool *pgxpool.Pool, log *slog.Logger) (compose.ModelPath, error) {
	switch {
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return compose.ModelPath{}, err
		}
		// A task whose whole fallback ladder has no bound tier is not a
		// boot error (a deployment may legitimately not run every
		// workload), but it must be loud: log it now, not discover it
		// from a refused call.
		for _, w := range cfg.UnboundLadderWarnings() {
			log.Warn(w)
		}
		return compose.NewModelPath(cfg, pool, capturePayloads, log)
	case fake:
		// A real ModelPath over ai.FakeRoutingConfig() rather than
		// FakeModelPath's direct client wiring: the worker always has a
		// pool, so --ai-fake safely rides the real Router (tiering, the
		// budget guardrail, metering, call tracing) with only the
		// provider swapped for the deterministic fake. capturePayloads
		// still names the deployment's own posture — cmd/api's
		// resolveModelPath honors it on this same arm, and two process
		// roles must never disagree on whether content capture is on.
		return compose.NewModelPath(ai.FakeRoutingConfig(), pool, capturePayloads, log)
	default:
		return compose.ModelPath{}, nil
	}
}

func runScheduler(ctx context.Context, svc *compose.RunnerService, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := svc.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("runner scheduler tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runResumeSubscriber consumes cg:overnight-agent: approval decisions
// wake parked runs.
func runResumeSubscriber(ctx context.Context, rdb *redis.Client, svc *compose.RunnerService, log *slog.Logger) {
	runSubscriber(ctx, rdb, "cg:overnight-agent", svc.HandleEvent, log)
}

// runSubscriber consumes one events.md consumer group, Dedupe-wrapped
// because the bus is at-least-once (events.md §3).
func runSubscriber(ctx context.Context, rdb *redis.Client, groupName string, handler events.Handler, log *slog.Logger) {
	var group kevents.Group
	for _, g := range kevents.Groups() {
		if g.Name == groupName {
			group = g
		}
	}
	sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, handler), log)
	if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("subscriber "+groupName, "err", err)
	}
}

// envOr reads an environment variable with an explicit default, keeping
// flag definitions self-documenting.
