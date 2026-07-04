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
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
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
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	redisAddr := fs.String("redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	routingPath := fs.String("ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the Surface-B runner")
	fakeBrain := fs.Bool("ai-fake", false, "run the Surface-B runner on the offline fake model (dev/test only)")
	runnerInterval := fs.Duration("runner-interval", 30*time.Second, "Surface-B scheduler tick interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("worker: --dsn or MARGINCE_DSN required")
	}

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := events.NewClient(ctx, *redisAddr)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	logger := slog.New(slog.NewTextHandler(stdout, nil))

	brain, err := selectBrain(*routingPath, *fakeBrain, pool)
	if err != nil {
		return err
	}
	if brain != nil {
		svc := compose.NewRunnerService(pool, brain, logger)
		_, _ = fmt.Fprintf(stdout, "worker running the Surface-B scheduler every %s\n", *runnerInterval)
		go runScheduler(ctx, svc, *runnerInterval, logger)
		go runResumeSubscriber(ctx, rdb, svc, logger)
	}

	_, _ = fmt.Fprintf(stdout, "worker relaying outbox events to %s\n", *redisAddr)
	// Run until signalled; unshipped rows wait durably in the outbox for
	// the next boot — shutdown loses no events.
	events.NewRelay(pool, rdb, logger).Run(ctx)
	return nil
}

// selectBrain resolves the runner's model path: a routing config for
// real deployments, the offline fake behind an explicit dev flag, or
// nil — the runner simply doesn't start without a declared brain; it
// never silently picks one.
func selectBrain(routingPath string, fake bool, pool *pgxpool.Pool) (runner.Brain, error) {
	switch {
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return nil, err
		}
		return compose.NewRouterBrain(cfg, pool)
	case fake:
		return ai.NewFakeClient(), nil
	default:
		return nil, nil
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
// wake parked runs. Dedupe wraps the handler because the bus is
// at-least-once (events.md §3).
func runResumeSubscriber(ctx context.Context, rdb *redis.Client, svc *compose.RunnerService, log *slog.Logger) {
	var group kevents.Group
	for _, g := range kevents.Groups() {
		if g.Name == "cg:overnight-agent" {
			group = g
		}
	}
	sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, svc.HandleEvent), log)
	if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("runner resume subscriber", "err", err)
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
