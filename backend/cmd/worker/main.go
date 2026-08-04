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
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	// Embedded tzdata: workspace timezones must resolve on scratch
	// containers that ship no zoneinfo.
	_ "time/tzdata"

	// The composed extension set (ADR-0069): the generated module under
	// build/composition/ in a composed build, the committed vanilla stub
	// in a bare one — same import path either way.
	"github.com/gradionhq/margince/composition"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
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

// run is the worker's boot sequence — the debug subcommands, flags,
// extensions, deployment file, logger, pool, bus, the event lanes and the job
// runner, then the relay it blocks on — in the order the process depends on;
// boot.go and jobrunner.go hold the phases.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	if handled, err := runDebugSubcommand(ctx, args, stdout); handled {
		return err
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

	deployCfg, err := loadDeployment(&cfg)
	if err != nil {
		return err
	}

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
	defer closeBus(rdb, logger)

	//nolint:contextcheck // boot-time wiring: the model path outlives any request context (cmd/api resolves the same path under the same waiver)
	modelPath, boundModels, err := selectModelPath(workerModelPathSpec(cfg, deployCfg), pool, logger)
	if err != nil {
		return err
	}

	// Deferred BEFORE the error is checked: a failure here still leaves earlier
	// lanes running on the bus and the pool this function closes above.
	lanes, err := startEventLanes(ctx, cfg, pool, rdb, modelPath, logger, stdout)
	defer lanes.join()
	if err != nil {
		return err
	}

	stopJobs, err := startJobRunner(ctx, pool, rdb, compose.OverlayBudgetConfig(deployCfg.EffectiveOverlayBudget()),
		logger, cfg, modelPath, boundModels, lanes, stdout)
	if err != nil {
		return err
	}
	defer stopJobs()

	workflows := compose.NewWorkflowEngineWithReplyDraft(pool, modelPath.DraftReply)
	_, _ = fmt.Fprintln(stdout, "worker dispatching workflows (cg:workflows)")
	lanes.background.Go(func() { runSubscriber(lanes.ctx, rdb, "cg:workflows", workflows.HandleEvent, logger, 0) })

	relayUntilSignal(ctx, cfg, pool, rdb, logger, stdout)
	return nil
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
