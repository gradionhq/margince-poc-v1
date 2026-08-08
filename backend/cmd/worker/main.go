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
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/pkg/extension"
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

	boot, err := configureWorker(args, stdout)
	if err != nil {
		return err
	}
	cfg, deployCfg, logger := boot.cfg, boot.deploy, boot.log

	pool, err := database.NewPool(ctx, cfg.dsn)
	if err != nil {
		return err
	}
	// Registered before the lanes' join below, so LIFO closes the pool after it.
	defer pool.Close()
	// Before any lane runs: a pool connecting as a role row-level security
	// does not bind serves every tenant's rows to every job, and nothing later
	// in this boot would say so.
	if err := compose.AssertRuntimeRole(ctx, pool); err != nil {
		return err
	}

	// Record the composed extension set when it changed since the last boot
	// (ADR-0069 §5); pre-bootstrap it skips — the api records the first
	// observation once it has bootstrapped the installation.
	if err := compose.ObserveExtensionInventory(ctx, pool, logger, boot.extensions); err != nil {
		return err
	}

	rdb, err := events.NewClient(ctx, cfg.redisAddr)
	if err != nil {
		return err
	}
	// Same ordering obligation as pool.Close above: the lanes read this client.
	defer closeBus(rdb, logger)

	// Before the lanes and the runner: a listener started after them reports
	// nothing during exactly the window a slow boot needs explaining. /readyz
	// answers "still starting" until started.complete below, so coming up
	// early never makes this replica look ready before it can work.
	started := &bootGate{}
	observe, err := startObserveListener(ctx, cfg, pool, rdb, started, logger)
	if err != nil {
		return err
	}
	defer observe.Stop()

	//nolint:contextcheck // boot-time wiring: the model path outlives any request context (cmd/api resolves the same path under the same waiver)
	modelPath, boundModels, err := selectModelPath(workerModelPathSpec(cfg, deployCfg), pool, logger)
	if err != nil {
		return err
	}

	// Deferred BEFORE the error is checked: a failure here still leaves earlier
	// lanes running on the bus and the pool whose closes are deferred above, and
	// LIFO is what puts this join ahead of them.
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
	// Every phase a replica needs to do work has returned; /readyz may say so.
	started.complete()
	// Deferred AFTER complete, so LIFO runs it FIRST: readiness goes false at
	// the top of the shutdown, before the runner and the lanes are put down.
	// The listener outlives both — it is stopped last — so a draining replica
	// keeps answering, and what it answers is "stop sending me work".
	defer started.draining()

	relayUntilSignal(ctx, cfg, pool, rdb, logger, stdout)
	return nil
}

// registerComposedExtensions registers the composed extension set before
// anything else runs; a failing registration aborts the boot (ADR-0069 EXT-P4).
//
// It returns the SAME snapshot it registered, because run hands that value on
// to the boot inventory: taking a second snapshot there would let the two
// observe different declarations, and the inventory's whole job is to record
// what this process is actually running.
func registerComposedExtensions() ([]extension.Extension, error) {
	extensions := composition.Extensions()
	if err := compose.RegisterExtensions(extensions); err != nil {
		return nil, err
	}
	return extensions, nil
}

// workerBoot is everything decided before this process touches a network: the
// parsed flags, the deployment file, the composed extension set, and the
// logger the phases after it report through.
type workerBoot struct {
	cfg        workerConfig
	deploy     deployconfig.Config
	extensions []extension.Extension
	log        *slog.Logger
}

// configureWorker runs the phases that can fail on configuration alone, in the
// order they depend on each other: flags, then extensions (a failing
// registration aborts the boot before anything is opened), then the deployment
// file, then the logger — and the capture posture last, because it carries the
// logger into the Sink's post-commit steps, where a fault is reported rather
// than returned.
//
// Grouped because they share one property the phases after them do not: none
// of them opens a connection, so a deployment misconfigured in any of these
// ways fails before a pool, a bus client or a listener exists to clean up.
func configureWorker(args []string, stdout io.Writer) (workerBoot, error) {
	cfg, err := parseWorkerFlags(args)
	if err != nil {
		return workerBoot{}, err
	}
	extensions, err := registerComposedExtensions()
	if err != nil {
		return workerBoot{}, err
	}
	deployCfg, err := loadDeployment(&cfg)
	if err != nil {
		return workerBoot{}, err
	}
	log, err := newWorkerLogger(cfg, stdout)
	if err != nil {
		return workerBoot{}, err
	}
	cfg.captureConfig = compose.CaptureConfigFromDeploy(deployCfg.Capture, log)
	return workerBoot{cfg: cfg, deploy: deployCfg, extensions: extensions, log: log}, nil
}

// newWorkerLogger builds this role's correlation-aware logger from the
// operator's --log-level and --log-format. Every process role shares the one
// level/format vocabulary, and a typo in either is a boot error rather than a
// silent fallback to a level nobody asked for.
func newWorkerLogger(cfg workerConfig, stdout io.Writer) (*slog.Logger, error) {
	handler, err := httpserver.LogHandler(stdout, cfg.logLevel, cfg.logFormat)
	if err != nil {
		return nil, err
	}
	return slog.New(httpserver.WithCorrelation(handler)), nil
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
