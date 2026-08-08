// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The worker role's River wiring: the credential custodian and sweep registry
// the scheduled jobs share, the job configuration itself, and the bounded drain
// that stops the runner.
package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

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

// startJobRunner boots the River periodic jobs: River
// gives leader election (one run cluster-wide, so worker replicas never
// double-sweep the close-date and reconcile passes), retries, and graceful
// drain — what the bare tickers lacked. The domain logic (Sweep/Reconcile)
// is unchanged; only the scheduler is River now. The returned stop function
// drains in-flight jobs on shutdown.
func startJobRunner(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, overlayBudget overlaybudget.Config, logger *slog.Logger, cfg workerConfig, modelPath compose.ModelPath, boundModels map[string]map[string]bool, lanes workerLanes, stdout io.Writer) (func(), error) {
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

	// The extension tier's per-call Runtime, bound before the runner exists
	// rather than beside the Surface-B lane that also binds it. This is the
	// binding the job lane depends on: a composed extension job is a River kind
	// worked HERE, on every worker, including one with no model configured —
	// where startRunnerLane returns at its AgentLoop guard without ever
	// binding. An unbound process is not a crash (every capability answers
	// errExtensionRuntimeUnwired, a clean refusal) but a tick that can only
	// ever refuse is not a working job.
	//
	// Unconditional, and that is the point: a guard here would reintroduce
	// exactly the shape that left the job lane unbound. `vault` is
	// keyvault.FromEnv's, already nil where none is configured, and it is the
	// same value startRunnerLane passes — so the two bindings are idempotent.
	compose.BindExtensionRuntime(pool, vault)

	runner, err := newJobRunner(pool, logger, cfg, captureReg, watchCfg, configuredVault, lanes, rdb, overlayBudget, modelPath, boundModels)
	if err != nil {
		return nil, err
	}
	if err := runner.Start(ctx); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintln(stdout, jobRunnerBanner(cfg, watchCfg, modelPath, configuredVault, lanes.runner))
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

// newJobRunner declares every scheduled lane this role runs, and for each the
// deployment condition that turns it on — or the omission that honestly leaves
// it off. One declaration, so no lane can be enabled by one boot phase and
// starved by another.
func newJobRunner(pool *pgxpool.Pool, logger *slog.Logger, cfg workerConfig, captureReg *capture.Registry, watchCfg compose.GmailWatchConfig, configuredVault keyvault.Vault, lanes workerLanes, rdb *redis.Client, overlayBudget overlaybudget.Config, modelPath compose.ModelPath, boundModels map[string]map[string]bool) (*jobs.Runner, error) {
	return compose.NewJobRunner(pool, logger, compose.JobRunnerConfig{
		// The registry that resolves a staged delivery's mailbox: the SAME
		// sweep registry the capture polls use, so the connector set that
		// syncs a mailbox is the one that transmits from it.
		SendRegistry:      captureReg,
		SendPacing:        compose.SendPacing{Limit: cfg.sendRateLimit, Window: cfg.sendRateWindow, MaxAge: cfg.sendMaxAge},
		CloseDateInterval: cfg.closeDateInterval,
		ReconcileInterval: cfg.reconcileInterval,
		TimeScanInterval:  cfg.timeScanInterval,
		// The GDPR retention fan-out's cadence: --retention-interval is the
		// schedule source, now read by River rather than by a ticker.
		PrivacyRetention: compose.PrivacyRetentionConfig{Interval: cfg.retentionInterval},
		// A nil deliverer (no --webhook-key) registers neither half.
		WebhookRetry: compose.WebhookRetryConfig{Interval: cfg.webhookRetryInterval, Deliverer: lanes.deliverer},
		// A nil service (no declared model) registers neither half.
		AgentScheduler: compose.AgentSchedulerConfig{Interval: cfg.runnerInterval, Service: lanes.runner},
		GmailRegistry:  captureReg,
		GmailWatch:     watchCfg,
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
		DeepReadCaps:          compose.CrawlCaps{MaxPages: cfg.deepReadMaxPages, MaxBytes: cfg.deepReadMaxBytes, Wall: cfg.deepReadWall},
		// The same object store retention purges from: a deep read resolves
		// the company's logo out of the site it just crawled and stores the
		// normalized bytes here. Nil (no blobstore configured) leaves every
		// company on its monogram — the read itself is unaffected.
		Blobstore: lanes.blob,
		// The embed-reindex worker registers regardless: without an embed
		// lane (nil Embedder) a picked-up job fails clearly rather than
		// sitting queued forever behind a job no one can work — the same
		// posture as DeepReadBrain above.
		Embedder: modelPath.Embedder,
	})
}
