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
	_ "time/tzdata"

	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
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
	dsn               string
	redisAddr         string
	routingPath       string
	fakeBrain         bool
	runnerInterval    time.Duration
	retentionInterval time.Duration
	closeDateInterval time.Duration
	reconcileInterval time.Duration
	logLevel          string
	logFormat         string
}

// parseWorkerFlags parses and validates the boot flags; the DSN is the
// one dependency without a sane default, so its absence fails the boot
// here.
func parseWorkerFlags(args []string) (workerConfig, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	var cfg workerConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the Surface-B runner")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "run the Surface-B runner on the offline fake model (dev/test only)")
	fs.DurationVar(&cfg.runnerInterval, "runner-interval", 30*time.Second, "Surface-B scheduler tick interval")
	fs.DurationVar(&cfg.retentionInterval, "retention-interval", 24*time.Hour, "retention evaluator pass interval")
	fs.DurationVar(&cfg.closeDateInterval, "close-date-interval", 24*time.Hour, "close-date hygiene sweep interval (INV-CLOSE-PAST)")
	fs.DurationVar(&cfg.reconcileInterval, "reconcile-interval", 24*time.Hour, "overnight follow-up reconciliation pass interval (features/07 §8a)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	if err := fs.Parse(args); err != nil {
		return workerConfig{}, err
	}
	if cfg.dsn == "" {
		return workerConfig{}, errors.New("worker: --dsn or MARGINCE_DSN required")
	}
	return cfg, nil
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := parseWorkerFlags(args)
	if err != nil {
		return err
	}

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

	modelPath, err := selectModelPath(cfg.routingPath, cfg.fakeBrain, pool)
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

	retention := privacy.NewRetentionService(pool, logger)
	_, _ = fmt.Fprintf(stdout, "worker evaluating retention every %s\n", cfg.retentionInterval)
	background.Go(func() { privacy.RunRetention(ctx, retention, cfg.retentionInterval, logger) })

	corrector := compose.NewCloseDateCorrector(pool, logger)
	_, _ = fmt.Fprintf(stdout, "worker sweeping close-date hygiene every %s\n", cfg.closeDateInterval)
	background.Go(func() { deals.RunCloseDateSweep(ctx, corrector, cfg.closeDateInterval, logger) })

	reconciler := compose.NewFollowUpReconciler(pool, logger)
	_, _ = fmt.Fprintf(stdout, "worker reconciling overnight follow-ups every %s\n", cfg.reconcileInterval)
	background.Go(func() { deals.RunFollowUpReconcile(ctx, reconciler, cfg.reconcileInterval, logger) })

	workflows := compose.NewWorkflowEngine(pool)
	_, _ = fmt.Fprintln(stdout, "worker dispatching workflows (cg:workflows)")
	background.Go(func() { runSubscriber(ctx, rdb, "cg:workflows", workflows.HandleEvent, logger) })

	_, _ = fmt.Fprintf(stdout, "worker relaying outbox events to %s\n", cfg.redisAddr)
	// Run until signalled; unshipped rows wait durably in the outbox for
	// the next boot — shutdown loses no events.
	events.NewRelay(pool, rdb, logger).Run(ctx)
	background.Wait()
	return nil
}

// selectModelPath resolves the model path: a routing config for real
// deployments, the offline fake behind an explicit dev flag, or the
// zero path — the runner and the embed lane simply don't start without
// a declared model; nothing is picked silently.
func selectModelPath(routingPath string, fake bool, pool *pgxpool.Pool) (compose.ModelPath, error) {
	switch {
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return compose.ModelPath{}, err
		}
		return compose.NewModelPath(cfg, pool)
	case fake:
		return compose.FakeModelPath(ai.NewFakeClient()), nil
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
