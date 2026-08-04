// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The worker role's boot phases: the debug subcommands that need no database,
// the deployment file this role reads, the event lanes it starts before the job
// runner, and the relay it finally blocks on. main.go keeps the sequence those
// phases run in — this file holds the phases themselves.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

// runDebugSubcommand dispatches the DB-less debug loops — `worker siteread …`
// (siteread.go) and `worker aitask …` (aitask.go) — and reports whether it
// handled the arguments. It runs before the worker flags, which would otherwise
// demand a DSN neither subcommand ever uses.
func runDebugSubcommand(ctx context.Context, args []string, stdout io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "siteread":
		return true, runSiteReadDebug(ctx, args[1:], stdout)
	case "aitask":
		return true, runAITaskProbe(ctx, args[1:], stdout)
	}
	return false, nil
}

// loadDeployment reads the same deployment file the api boots from — the
// capture pipeline tuning (capture.freemail_extra) and the operator's
// ai.capture_payloads posture the Surface-B runner honors — and folds the rate
// sources it declares into cfg. A missing file means defaults; a malformed one
// is a boot error (a typo must not silently drop the blocklist or flip the
// payload posture).
func loadDeployment(cfg *workerConfig) (deployconfig.Config, error) {
	deployCfg, err := deployconfig.Load(cfg.configPath)
	if err != nil {
		return deployconfig.Config{}, err
	}
	cfg.ratesFx = deployCfg.Rates.Fx
	cfg.ratesCurrencies = deployCfg.Rates.FxCurrencies
	cfg.ratesModelPricing = deployCfg.Rates.ModelPricing
	return deployCfg, nil
}

// workerModelPathSpec gathers the model-path knobs from where each is
// declared: the routing choice from the process flags, the capture posture from
// the deployment config — the same split cmd/api's modelPathSpecFrom reads, so
// the two roles cannot disagree on whether content capture is on.
func workerModelPathSpec(cfg workerConfig, deployCfg deployconfig.Config) modelPathSpec {
	return modelPathSpec{
		routingPath:     cfg.routingPath,
		fake:            cfg.fakeBrain,
		capturePayloads: deployCfg.AI.CapturePayloads,
	}
}

// closeBus releases the bus client at shutdown, reporting a close fault rather
// than dropping it — by then there is nobody left to return it to.
func closeBus(rdb *redis.Client, logger *slog.Logger) {
	if err := rdb.Close(); err != nil {
		logger.Warn("closing bus client", "err", err)
	}
}

// workerLanes is what the event lanes leave behind for the job runner, which
// schedules against the SAME instances those lanes consume into: one governed
// registry and one brain per role, ONE deliverer across both outbound-webhook
// lanes (E10/S-E10.6) — never two that could drift apart — plus the object
// store the deep-read logo write and the retention purge share.
type workerLanes struct {
	// background holds every lane goroutine so run() returns only after
	// in-flight handlers finish their ack — the same shape as cmd/api's relay
	// group; a bare goroutine would be killed mid-handler when the relay
	// returns.
	background *sync.WaitGroup
	// ctx is what every lane goroutine runs under and stop is what ends them,
	// so the lanes own their own lifetime: one place cancels, one place joins.
	ctx       context.Context
	stop      context.CancelFunc
	runner    *compose.RunnerService
	deliverer *webhooks.Deliverer
	blob      blobstore.Store
}

// join ends the lanes and waits for the handler each is in. It is what makes the
// bus and the pool safe to close: both are deferred before the lanes start, so
// they close after this returns, never under a live subscriber.
func (l workerLanes) join() {
	l.stop()
	l.background.Wait()
}

// startEventLanes starts the lanes this role runs before the job runner exists
// and resolves what the runner then needs from them, in the order an operator
// reads at boot.
//
// A failure returns the lanes started SO FAR alongside the error, never a zero
// value: goroutines are already running on that WaitGroup, and handing back a
// struct whose WaitGroup is nil would leave a caller no way to join them before
// it closes the bus and the pool they are still using.
func startEventLanes(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, modelPath compose.ModelPath, logger *slog.Logger, stdout io.Writer) (workerLanes, error) {
	laneCtx, stopLanes := context.WithCancel(ctx)
	lanes := workerLanes{background: &sync.WaitGroup{}, ctx: laneCtx, stop: stopLanes}

	if err := startRunnerLane(laneCtx, cfg, pool, rdb, modelPath, &lanes, logger, stdout); err != nil {
		return lanes, err
	}
	startProjectionLanes(laneCtx, pool, rdb, modelPath, lanes.background, logger, stdout)

	blob, blobConfigured, err := blobstore.FromEnv(ctx)
	if err != nil {
		return lanes, fmt.Errorf("worker: blobstore: %w", err)
	}
	if blobConfigured {
		_, _ = fmt.Fprintln(stdout, "worker storing site-read logos and erasing attachment objects (blobstore configured)")
	}
	lanes.blob = blob
	if err := backfillConnectorCredentials(ctx, pool, stdout, logger); err != nil {
		return lanes, err
	}

	if err := startWebhookLane(laneCtx, cfg, pool, rdb, &lanes, logger, stdout); err != nil {
		return lanes, err
	}
	return lanes, nil
}

// startRunnerLane builds the Surface-B runner and starts the subscriber that
// resumes its approved runs. Without a declared brain there is no runner and
// lanes.runner stays nil, which is what leaves the scheduler unregistered.
func startRunnerLane(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, modelPath compose.ModelPath, lanes *workerLanes, logger *slog.Logger, stdout io.Writer) error {
	// The Surface-B runner is this role's sending lane, and it stages through
	// the SAME delivery machinery the api does — built before the lane so it
	// cannot be composed without one. Insert-only, like the api's: the staged
	// job is worked by this role's own River runner (startJobRunner), and
	// a lane that staged onto the runner it is itself being wired into would
	// need the runner to exist before the lanes do.
	sendInserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return err
	}
	send := sendPath(cfg, compose.NewDeliveryStager(pool, sendInserter))
	if modelPath.AgentLoop == nil {
		return nil
	}
	grounding := search.NewRetriever(search.NewStore(pool), modelPath.Embedder)
	// The Surface-B runner's agent tools reach overlay write-back through
	// the workspace's own vaulted incumbent token; wire the FromEnv
	// vault-backed resolver so an autonomous run can write back (nil vault
	// → clean errNoWriteIncumbent, never a crash).
	runnerVault, _, rverr := keyvault.FromEnv(pool)
	if rverr != nil {
		return rverr
	}
	runnerSvc := compose.NewRunnerService(pool, modelPath.AgentLoop, modelPath.DraftReply, grounding, logger, compose.OverlayIncumbentResolver(pool, runnerVault), send)
	_, _ = fmt.Fprintln(stdout, "worker resuming approved Surface-B runs (cg:overnight-agent)")
	lanes.runner = runnerSvc
	lanes.background.Go(func() { runResumeSubscriber(ctx, rdb, runnerSvc, logger) })
	return nil
}

// startProjectionLanes starts the lanes that maintain derived read models: the
// retrieval embeddings a declared embed lane feeds, and the two deterministic
// projections that need no model at all.
func startProjectionLanes(ctx context.Context, pool *pgxpool.Pool, rdb *redis.Client, modelPath compose.ModelPath, background *sync.WaitGroup, logger *slog.Logger, stdout io.Writer) {
	if modelPath.Embedder != nil {
		gen := search.NewEmbedGen(search.NewStore(pool), modelPath.Embedder)
		_, _ = fmt.Fprintln(stdout, "worker maintaining retrieval embeddings")
		background.Go(func() { runSubscriber(ctx, rdb, "cg:context-graph", gen.HandleEvent, logger, 0) })
	}
	// The interaction-edge projection (ADR-0078). Unlike embeddings it needs no
	// model, so it runs on every worker rather than only where a provider is
	// configured — a deployment without AI still answers "who on our team knows
	// this contact", which is a deterministic question about our own mail.
	edges := search.NewGraphEdgeGen(search.NewStore(pool))
	_, _ = fmt.Fprintln(stdout, "worker maintaining interaction edges")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:graph-edge", edges.HandleEvent, logger, 0) })

	// The LinkedIn ghost matcher (ADR-0078 §8b): a ghost attaches the moment
	// its contact exists, whoever created them. Deterministic like the edge
	// projection above, so it runs on every worker.
	matcher := compose.NewLinkedInMatchGen(pool, people.NewStore(pool), identity.NewService(pool), logger)
	_, _ = fmt.Fprintln(stdout, "worker matching LinkedIn connections as contacts appear")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:linkedin-match", matcher.HandleEvent, logger, 0) })
}

// startWebhookLane starts the cg:webhooks delivery consumer, whose deliverer is
// also the one the River retry sweep re-attempts through — which is why it is
// built here and travels to the runner rather than being built twice. Without a
// signing key neither lane exists and lanes.deliverer stays nil.
func startWebhookLane(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, lanes *workerLanes, logger *slog.Logger, stdout io.Writer) error {
	if cfg.webhookKey == "" {
		return nil
	}
	deliverer, err := compose.NewWebhookDeliverer(pool, cfg.webhookKey, logger)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	_, _ = fmt.Fprintln(stdout, "worker delivering outbound webhooks (cg:webhooks)")
	lanes.deliverer = deliverer
	lanes.background.Go(func() { runSubscriber(ctx, rdb, "cg:webhooks", deliverer.HandleEvent, logger, 0) })
	return nil
}

// relayUntilSignal ships outbox rows until the process is signalled. Unshipped
// rows wait durably in the outbox for the next boot — shutdown loses no events.
// Joining the lanes is run()'s deferred job, so every return path joins them and
// not just this one.
func relayUntilSignal(ctx context.Context, cfg workerConfig, pool *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger, stdout io.Writer) {
	_, _ = fmt.Fprintf(stdout, "worker relaying outbox events to %s\n", cfg.redisAddr)
	events.NewRelay(pool, rdb, logger).Run(ctx)
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
