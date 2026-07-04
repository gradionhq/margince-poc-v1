// Command worker is the background process role (ADR-0054, amended §2):
// the standalone outbox relay for split deployments — cmd/api runs the
// same relay inline by default (--inline-relay), so small installs never
// need this binary. Future async workflows land here too.
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

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
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
	_, _ = fmt.Fprintf(stdout, "worker relaying outbox events to %s\n", *redisAddr)
	// Run until signalled; unshipped rows wait durably in the outbox for
	// the next boot — shutdown loses no events.
	events.NewRelay(pool, rdb, logger).Run(ctx)
	return nil
}

// envOr reads an environment variable with an explicit default, keeping
// flag definitions self-documenting.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
