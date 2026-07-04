package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/httpapi"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
)

// runServe boots the HTTP server plus the outbox relay (decisions/0005)
// with explicit operational limits and graceful shutdown — a server
// without timeouts leaks connections under slow clients.
func runServe(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	addr := fs.String("addr", ":8080", "listen address")
	redisAddr := fs.String("redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("serve: --dsn or MARGINCE_DSN required")
	}

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	// The bus is not optional plumbing: without a relay every committed
	// write strands its outbox row, so an unreachable Redis fails the
	// boot the same way an unreachable Postgres does (B-EP04.1).
	rdb, err := events.NewClient(ctx, *redisAddr)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	logger := slog.New(slog.NewTextHandler(stdout, nil))
	relayCtx, stopRelay := context.WithCancel(context.Background())
	var relay sync.WaitGroup
	relay.Go(func() {
		events.NewRelay(pool, rdb, logger).Run(relayCtx)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.New(pool, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	_, _ = fmt.Fprintf(stdout, "crm listening on %s (base path /v1), relaying events to %s\n", *addr, *redisAddr)

	// The relay stops after the HTTP server so late-committing requests
	// usually ship before exit; anything still unshipped waits durably in
	// the outbox for the next boot — shutdown loses no kevents.
	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	stopAll := func(httpErr error) error {
		stopRelay()
		relay.Wait()
		return httpErr
	}

	select {
	case err := <-errCh:
		return stopAll(err)
	case <-ctx.Done():
		return stopAll(stopHTTP())
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
