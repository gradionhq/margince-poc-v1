// Command api is the HTTP process role (ADR-0054, amended §2): thin
// main, a testable run(), wiring through internal/compose. By default it
// also runs the outbox relay inline (decisions/0005 — one process for
// dev and small self-hosted installs); a split deployment passes
// --inline-relay=false and runs cmd/worker.
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
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// run boots the HTTP server (plus, by default, the inline outbox relay)
// with explicit operational limits and graceful shutdown — a server
// without timeouts leaks connections under slow clients.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	addr := fs.String("addr", ":8080", "listen address")
	redisAddr := fs.String("redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	inlineRelay := fs.Bool("inline-relay", true, "run the outbox relay in this process (false when cmd/worker runs it)")
	routingPath := fs.String("ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the cold-start read-back")
	fakeBrain := fs.Bool("ai-fake", false, "drive the AI surfaces with the offline fake model (dev/test only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("api: --dsn or MARGINCE_DSN required")
	}

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(stdout, nil))

	// The bus is not optional plumbing: without a relay every committed
	// write strands its outbox row, so an unreachable Redis fails the
	// boot the same way an unreachable Postgres does (B-EP04.1).
	var relay sync.WaitGroup
	stopRelay := func() {}
	if *inlineRelay {
		rdb, err := events.NewClient(ctx, *redisAddr)
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()
		relayCtx, cancel := context.WithCancel(context.Background())
		stopRelay = cancel
		relay.Go(func() {
			events.NewRelay(pool, rdb, logger).Run(relayCtx)
		})
	}

	// The cold-start read-back needs a declared model path; without one
	// the operation stays an explicit 501 (same posture as the worker's
	// runner lane).
	var opts []compose.Option
	switch {
	case *routingPath != "":
		cfg, err := ai.LoadRoutingFile(*routingPath)
		if err != nil {
			return err
		}
		modelPath, err := compose.NewModelPath(cfg, pool)
		if err != nil {
			return err
		}
		opts = append(opts, compose.WithColdStart(compose.NewWebFetcher(), modelPath.ColdStart))
	case *fakeBrain:
		opts = append(opts, compose.WithColdStart(compose.NewWebFetcher(), ai.NewFakeClient()))
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           compose.New(pool, logger, opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	if *inlineRelay {
		_, _ = fmt.Fprintf(stdout, "api listening on %s (base path /v1), relaying events to %s\n", *addr, *redisAddr)
	} else {
		_, _ = fmt.Fprintf(stdout, "api listening on %s (base path /v1); the outbox relay runs in cmd/worker\n", *addr)
	}

	// The relay stops after the HTTP server so late-committing requests
	// usually ship before exit; anything still unshipped waits durably in
	// the outbox for the next boot — shutdown loses no events.
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
