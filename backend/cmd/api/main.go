// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
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
	logLevel := fs.String("log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	logFormat := fs.String("log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("api: --dsn or MARGINCE_DSN required")
	}

	handler, err := newLogHandler(stdout, *logLevel, *logFormat)
	if err != nil {
		return err
	}
	logger := slog.New(httpserver.WithCorrelation(handler))

	pool, err := database.NewPool(ctx, *dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	var opts []compose.Option

	stopRelay := func() {}
	if *inlineRelay {
		busReady, stop, err := startInlineRelay(ctx, pool, *redisAddr, logger)
		if err != nil {
			return err
		}
		stopRelay = stop
		opts = append(opts, busReady)
	}

	coldStart, err := coldStartOptions(*routingPath, *fakeBrain, pool)
	if err != nil {
		return err
	}
	opts = append(opts, coldStart...)

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

	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	stopAll := func(httpErr error) error {
		stopRelay()
		return httpErr
	}

	select {
	case err := <-errCh:
		return stopAll(err)
	case <-ctx.Done():
		return stopAll(stopHTTP())
	}
}

// startInlineRelay boots the in-process outbox relay. The bus is not
// optional plumbing: without a relay every committed write strands its
// outbox row, so an unreachable Redis fails the boot the same way an
// unreachable Postgres does (B-EP04.1). The returned compose option makes
// the bus a readiness dependency of THIS process (a split deployment's
// api is ready on Postgres alone); the stop function runs after the HTTP
// server shuts down, so late-committing requests usually ship before
// exit — anything still unshipped waits durably in the outbox for the
// next boot, and shutdown loses no events.
func startInlineRelay(ctx context.Context, pool *pgxpool.Pool, redisAddr string, logger *slog.Logger) (compose.Option, func(), error) {
	rdb, err := events.NewClient(ctx, redisAddr)
	if err != nil {
		return nil, nil, err
	}
	relayCtx, cancel := context.WithCancel(context.Background())
	var relay sync.WaitGroup
	relay.Go(func() {
		events.NewRelay(pool, rdb, logger).Run(relayCtx)
	})
	stop := func() {
		cancel()
		relay.Wait()
		if err := rdb.Close(); err != nil {
			logger.Warn("closing bus client", "err", err)
		}
	}
	busReady := compose.WithBusReady(func(ctx context.Context) error {
		return rdb.Ping(ctx).Err()
	})
	return busReady, stop, nil
}

// coldStartOptions resolves the cold-start read-back's model wiring: a
// declared routing file for real deployments, the offline fake behind an
// explicit dev flag, or nothing — the operation then stays an explicit
// 501 (same posture as the worker's runner lane).
func coldStartOptions(routingPath string, fakeBrain bool, pool *pgxpool.Pool) ([]compose.Option, error) {
	switch {
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return nil, err
		}
		modelPath, err := compose.NewModelPath(cfg, pool)
		if err != nil {
			return nil, err
		}
		return []compose.Option{compose.WithColdStart(compose.NewWebFetcher(), modelPath.ColdStart)}, nil
	case fakeBrain:
		return []compose.Option{compose.WithColdStart(compose.NewWebFetcher(), ai.NewFakeClient())}, nil
	default:
		return nil, nil
	}
}

// newLogHandler builds the slog backend from the operator's level and
// format choices; a typo in either is a boot error, never a silent
// fallback to defaults.
func newLogHandler(w io.Writer, level, format string) (slog.Handler, error) {
	var lv slog.LevelVar
	switch level {
	case "debug":
		lv.Set(slog.LevelDebug)
	case "info":
		lv.Set(slog.LevelInfo)
	case "warn":
		lv.Set(slog.LevelWarn)
	case "error":
		lv.Set(slog.LevelError)
	default:
		return nil, fmt.Errorf("--log-level %q: want debug, info, warn, or error", level)
	}
	opts := &slog.HandlerOptions{Level: &lv}
	switch format {
	case "text":
		return slog.NewTextHandler(w, opts), nil
	case "json":
		return slog.NewJSONHandler(w, opts), nil
	default:
		return nil, fmt.Errorf("--log-format %q: want text or json", format)
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
