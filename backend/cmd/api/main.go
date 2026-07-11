// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command api is the HTTP process role (ADR-0054, amended §2): thin
// main, a testable run(), wiring through internal/compose. By default it
// also runs the outbox relay inline (decisions/0005 — one process for
// dev and small self-hosted installs); a split deployment passes
// --inline-relay=false and runs cmd/worker.
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
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// apiConfig is the parsed boot configuration of the api process.
type apiConfig struct {
	dsn           string
	schemaDSN     string
	addr          string
	redisAddr     string
	inlineRelay   bool
	routingPath   string
	fakeBrain     bool
	logLevel      string
	logFormat     string
	publicBaseURL string
}

// parseAPIFlags parses and validates the boot flags; the DSN is the one
// dependency without a sane default, so its absence fails the boot here.
func parseAPIFlags(args []string) (apiConfig, error) {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	var cfg apiConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.schemaDSN, "schema-dsn", os.Getenv("MARGINCE_SCHEMA_DSN"),
		"Postgres DSN (owner role) for the customfields runtime-DDL pool; unset = the two schema-change operations answer 501 (decisions/0024)")
	fs.StringVar(&cfg.addr, "addr", ":8080", "listen address")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.BoolVar(&cfg.inlineRelay, "inline-relay", true, "run the outbox relay in this process (false when cmd/worker runs it)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the cold-start read-back")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "drive the AI surfaces with the offline fake model (dev/test only)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	fs.StringVar(&cfg.publicBaseURL, "public-base-url", os.Getenv("MARGINCE_PUBLIC_BASE_URL"), "canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe); required to send marketing mail")
	if err := fs.Parse(args); err != nil {
		return apiConfig{}, err
	}
	if cfg.dsn == "" {
		return apiConfig{}, errors.New("api: --dsn or MARGINCE_DSN required")
	}
	return cfg, nil
}

// run boots the HTTP server (plus, by default, the inline outbox relay)
// with explicit operational limits and graceful shutdown — a server
// without timeouts leaks connections under slow clients.
func run(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := parseAPIFlags(args)
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

	var opts []compose.Option
	if cfg.publicBaseURL != "" {
		opts = append(opts, compose.WithPublicBaseURL(cfg.publicBaseURL))
	}

	blobOpts, err := blobstoreOptions(ctx, stdout)
	if err != nil {
		return err
	}
	opts = append(opts, blobOpts...)

	kvOpts, err := keyvaultOptions(pool, stdout)
	if err != nil {
		return err
	}
	opts = append(opts, kvOpts...)

	schemaOpts, closeSchemaPool, err := schemaPoolOptions(ctx, cfg.schemaDSN, stdout)
	if err != nil {
		return err
	}
	defer closeSchemaPool()
	opts = append(opts, schemaOpts...)

	stopRelay := func() {
		// No inline relay to stop unless --inline-relay wires one below.
	}
	if cfg.inlineRelay {
		busReady, stop, err := startInlineRelay(ctx, pool, cfg.redisAddr, logger)
		if err != nil {
			return err
		}
		stopRelay = stop
		opts = append(opts, busReady)
	}

	coldStart, err := coldStartOptions(cfg.routingPath, cfg.fakeBrain, pool)
	if err != nil {
		return err
	}
	opts = append(opts, coldStart...)

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           compose.New(pool, logger, opts...),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	if cfg.inlineRelay {
		_, _ = fmt.Fprintf(stdout, "api listening on %s (base path /v1), relaying events to %s\n", cfg.addr, cfg.redisAddr)
	} else {
		_, _ = fmt.Fprintf(stdout, "api listening on %s (base path /v1); the outbox relay runs in cmd/worker\n", cfg.addr)
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

// blobstoreOptions wires the attachment endpoints (and their /readyz probe +
// erase-path object purge) only when an object store is configured; without
// one the endpoints answer 501 rather than nil-deref at request time.
func blobstoreOptions(ctx context.Context, stdout io.Writer) ([]compose.Option, error) {
	blob, configured, err := blobstore.FromEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("api: blobstore: %w", err)
	}
	if !configured {
		return nil, nil
	}
	_, _ = fmt.Fprintln(stdout, "api attachments enabled (blobstore configured)")
	return []compose.Option{compose.WithBlobstore(blob)}, nil
}

// keyvaultOptions wires the secret vault — its /readyz probe and the
// vault-backed connector-credential path — only when a root key is
// configured. Without one the vault stays absent: the transient one-shot IMAP
// pull (which persists no credential) still works, and the persisting paths
// (Connect/Sync) refuse loudly rather than nil-deref if ever invoked. A key
// that is set but malformed is a boot error (keyvault.FromEnv), never a silent
// fallback to something weaker.
func keyvaultOptions(pool *pgxpool.Pool, stdout io.Writer) ([]compose.Option, error) {
	vault, configured, err := keyvault.FromEnv(pool)
	if err != nil {
		return nil, fmt.Errorf("api: keyvault: %w", err)
	}
	if !configured {
		return nil, nil
	}
	_, _ = fmt.Fprintln(stdout, "api connector-credential vault enabled (keyvault configured)")
	return []compose.Option{compose.WithKeyvault(vault)}, nil
}

// schemaPoolOptions wires the customfields engine's owner-privileged
// schema-change pool (decisions/0024) — the second pgxpool the two
// runtime-DDL operations (createCustomField, updateCustomFieldOptions)
// need — only when --schema-dsn/MARGINCE_SCHEMA_DSN is set. Without one
// those two operations stay their generated 501 (ErrSchemaChangesUnavailable);
// the close func is a no-op in that case, so run() can always defer it
// unconditionally.
func schemaPoolOptions(ctx context.Context, schemaDSN string, stdout io.Writer) ([]compose.Option, func(), error) {
	if schemaDSN == "" {
		return nil, func() {}, nil
	}
	// The engine serializes every ALTER on a table behind a transaction-scoped
	// advisory lock (customfields.beginSchemaChange), so this pool never runs
	// more than one DDL statement per table at a time; a handful of
	// connections is a deliberately small footprint for a rare admin path,
	// next to the app pool's MaxConns=16 default (database.NewPool).
	pool, err := database.NewPool(ctx, withPoolMaxConns(schemaDSN, 3))
	if err != nil {
		return nil, nil, fmt.Errorf("api: schema pool: %w", err)
	}
	_, _ = fmt.Fprintln(stdout, "api custom-field schema changes enabled (schema pool configured)")
	return []compose.Option{compose.WithSchemaPool(pool)}, pool.Close, nil
}

// withPoolMaxConns appends a pool_max_conns limit to dsn unless the
// operator already sized the pool themselves (database.NewPool's own
// DSN-wins-over-default rule) — the URL and keyword/value DSN forms take
// the query-parameter and space-separated keyword spellings respectively.
func withPoolMaxConns(dsn string, n int) string {
	if strings.Contains(dsn, "pool_max_conns") {
		return dsn
	}
	param := fmt.Sprintf("pool_max_conns=%d", n)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + param
	}
	return dsn + " " + param
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
		// The read-back and per-org enrichment share the fetch + extraction
		// seam, so both light up together on the one declared model path;
		// the Morning-Brief L2 re-order rides its own routed lane.
		fetch := compose.NewWebFetcher()
		return []compose.Option{
			compose.WithColdStart(fetch, modelPath.ColdStart),
			compose.WithScrape(fetch, modelPath.ColdStart),
			compose.WithBrief(modelPath.BriefRank),
		}, nil
	case fakeBrain:
		fetch := compose.NewWebFetcher()
		fake := ai.NewFakeClient()
		return []compose.Option{
			compose.WithColdStart(fetch, fake),
			compose.WithScrape(fetch, fake),
			compose.WithBrief(fake),
		}, nil
	default:
		return nil, nil
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
