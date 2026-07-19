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
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

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

// workerConfig is the parsed boot configuration of the worker process.
type workerConfig struct {
	dsn                string
	configPath         string
	freemailExtra      []string
	redisAddr          string
	routingPath        string
	fakeBrain          bool
	runnerInterval     time.Duration
	retentionInterval  time.Duration
	closeDateInterval  time.Duration
	reconcileInterval  time.Duration
	timeScanInterval   time.Duration
	gmailClientID      string
	gmailClientSecret  string
	graphClientID      string
	graphClientSecret  string
	graphTenant        string
	gmailSyncInterval  time.Duration
	gmailPubsubTopic   string
	gmailWatchInterval time.Duration
	gmailWatchRenew    time.Duration
	deepReadMaxPages   int
	deepReadMaxBytes   int
	deepReadWall       time.Duration
	logLevel           string
	logFormat          string
}

// parseWorkerFlags parses and validates the boot flags; the DSN is the
// one dependency without a sane default, so its absence fails the boot
// here.
func parseWorkerFlags(args []string) (workerConfig, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	var cfg workerConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.configPath, "config", envOr("MARGINCE_CONFIG", "margince.yaml"),
		"path to the deployment configuration file (A107/ADR-0061); read for the ai.capture_payloads posture the Surface-B runner honors and the capture pipeline tuning (capture.freemail_extra). A missing file boots with defaults")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the Surface-B runner")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "run the Surface-B runner on the offline fake model (dev/test only)")
	fs.DurationVar(&cfg.runnerInterval, "runner-interval", 30*time.Second, "Surface-B scheduler tick interval")
	fs.DurationVar(&cfg.retentionInterval, "retention-interval", 24*time.Hour, "retention evaluator pass interval")
	fs.DurationVar(&cfg.closeDateInterval, "close-date-interval", 24*time.Hour, "close-date hygiene sweep interval (INV-CLOSE-PAST)")
	fs.DurationVar(&cfg.reconcileInterval, "reconcile-interval", 24*time.Hour, "overnight follow-up reconciliation pass interval (features/07 §8a)")
	fs.DurationVar(&cfg.timeScanInterval, "time-scan-interval", time.Hour, "clock-trigger scan interval (no_activity_reminder et al., Task 14)")
	fs.StringVar(&cfg.gmailClientID, "gmail-client-id", os.Getenv("MARGINCE_GMAIL_CLIENT_ID"), "Google OAuth client id for the Gmail capture connector; enables the background Gmail sync poll")
	fs.StringVar(&cfg.gmailClientSecret, "gmail-client-secret", os.Getenv("MARGINCE_GMAIL_CLIENT_SECRET"), "Google OAuth client secret for the Gmail capture connector")
	fs.StringVar(&cfg.graphClientID, "graph-client-id", os.Getenv("MARGINCE_GRAPH_CLIENT_ID"), "Microsoft (Entra) application id for the Outlook/M365 capture connector; enables its background sync poll")
	fs.StringVar(&cfg.graphClientSecret, "graph-client-secret", os.Getenv("MARGINCE_GRAPH_CLIENT_SECRET"), "Microsoft client secret for the Outlook/M365 capture connector")
	fs.StringVar(&cfg.graphTenant, "graph-tenant", os.Getenv("MARGINCE_GRAPH_TENANT"), "Microsoft identity tenant for token refresh (default: common — any organization)")
	fs.DurationVar(&cfg.gmailSyncInterval, "gmail-sync-interval", 2*time.Minute, "Gmail incremental-sync poll interval")
	fs.StringVar(&cfg.gmailPubsubTopic, "gmail-pubsub-topic", os.Getenv("MARGINCE_GMAIL_PUBSUB_TOPIC"), "Gmail Pub/Sub topic (projects/<p>/topics/<t>); enables the push-watch register+renew job. Empty leaves capture on the poll.")
	fs.DurationVar(&cfg.gmailWatchInterval, "gmail-watch-interval", 6*time.Hour, "Gmail push-watch maintenance scan interval")
	fs.DurationVar(&cfg.gmailWatchRenew, "gmail-watch-renew-within", 48*time.Hour, "renew a Gmail watch this far ahead of its 7-day expiry")
	maxPagesDefault, err := envIntOr("MARGINCE_DEEPREAD_MAX_PAGES", 0)
	if err != nil {
		return workerConfig{}, err
	}
	maxBytesDefault, err := envIntOr("MARGINCE_DEEPREAD_MAX_BYTES", 0)
	if err != nil {
		return workerConfig{}, err
	}
	wallDefault, err := envDurationOr("MARGINCE_DEEPREAD_WALL", 0)
	if err != nil {
		return workerConfig{}, err
	}
	fs.IntVar(&cfg.deepReadMaxPages, "deepread-max-pages", maxPagesDefault, "deep-read crawl page cap; 0 takes the built-in default")
	fs.IntVar(&cfg.deepReadMaxBytes, "deepread-max-bytes", maxBytesDefault, "deep-read crawl aggregate byte cap; 0 takes the built-in default")
	fs.DurationVar(&cfg.deepReadWall, "deepread-wall", wallDefault, "deep-read crawl wall clock; 0 takes the built-in default")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	if cfg.dsn == "" {
		return workerConfig{}, errors.New("worker: --dsn or MARGINCE_DSN required")
	}
	if cfg.deepReadMaxPages < 0 || cfg.deepReadMaxBytes < 0 || cfg.deepReadWall < 0 {
		return workerConfig{}, errors.New("worker: the deep-read caps must be zero (default) or positive")
	}
	return cfg, nil
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
		svc := compose.NewRunnerService(pool, modelPath.Agent, grounding, logger)
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

	stopJobs, err := startJobRunner(ctx, pool, logger, cfg, modelPath, stdout)
	if err != nil {
		return err
	}
	defer stopJobs()

	workflows := compose.NewWorkflowEngine(pool, compose.NewVoiceDrafter(pool, modelPath.DraftReply))
	_, _ = fmt.Fprintln(stdout, "worker dispatching workflows (cg:workflows)")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:workflows", workflows.HandleEvent, logger) })

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
func startJobRunner(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, cfg workerConfig, modelPath compose.ModelPath, stdout io.Writer) (func(), error) {
	// The sweep registry is always live — the standing IMAP connector needs
	// no deployment config; gmail joins it when the OAuth app is configured.
	// The vault holds every connection's sealed credential (the standing
	// flavors resolve through it), so it initializes here regardless.
	vault, _, verr := keyvault.FromEnv(pool)
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
	watchCfg := compose.GmailWatchConfig{
		Interval:    cfg.gmailWatchInterval,
		RenewWithin: cfg.gmailWatchRenew,
	}
	// The watch job only runs where a Pub/Sub topic is configured AND the Gmail
	// app is wired; otherwise capture stays on the poll.
	if gmailWired {
		watchCfg.Topic = cfg.gmailPubsubTopic
	}
	runner, err := compose.NewJobRunner(pool, logger, compose.JobRunnerConfig{
		CloseDateInterval: cfg.closeDateInterval,
		ReconcileInterval: cfg.reconcileInterval,
		TimeScanInterval:  cfg.timeScanInterval,
		GmailRegistry:     captureReg,
		// The classify + enrich passes run only where a model is
		// configured; without one both are absent by omission.
		ClassifyBrain: modelPath.CaptureClassify,
		EnrichBrain:   modelPath.SignatureEnrich,

		GmailWatch: watchCfg,
		// The deep-read worker registers regardless: without a model path
		// (nil SiteExtract) it fails a picked-up read honestly rather than
		// leaving it queued behind a job no one can work.
		DeepReadBrain:     modelPath.SiteExtract,
		DeepReadFactBrain: modelPath.SiteFactExtract,
		VoiceBuildBrain:   modelPath.VoiceBuild,
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
	deepReadNote := "deep read on"
	if modelPath.SiteExtract == nil {
		deepReadNote = "deep read degraded: no model path, queued reads will fail (configure --ai-routing)"
	}
	_, _ = fmt.Fprintf(stdout, "worker running River jobs (close-date every %s, reconcile every %s, time-scan every %s, %s, %s)\n",
		cfg.closeDateInterval, cfg.reconcileInterval, cfg.timeScanInterval, captureNote, deepReadNote)
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
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envIntOr / envDurationOr back a numeric flag's default with an
// environment variable; a set-but-unparseable value is a boot error,
// never a silent fallback.
func envIntOr(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("worker: %s=%q is not an integer: %w", key, v, err)
	}
	return parsed, nil
}

func envDurationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("worker: %s=%q is not a duration: %w", key, v, err)
	}
	return parsed, nil
}
