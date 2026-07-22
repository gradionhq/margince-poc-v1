// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command api is the HTTP process role (ADR-0054, amended §2): thin
// main, a testable run(), wiring through internal/compose. By default it
// also runs the outbox relay inline (one process for
// dev and small self-hosted installs); a split deployment passes
// --inline-relay=false and runs cmd/worker.
package main

import (
	// Embedded tzdata: workspace timezones must resolve on scratch
	// containers that ship no zoneinfo.
	_ "time/tzdata"

	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// The composed extension set (ADR-0069): the generated module under
	// build/composition/ in a composed build, the committed vanilla stub
	// in a bare one — same import path either way.
	"github.com/gradionhq/margince/composition"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
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
	cfg, err := parseAPIFlags(args)
	if err != nil {
		return err
	}

	// Register the composed extension set before anything serves; a
	// failing registration aborts the boot (ADR-0069 EXT-P4). ONE
	// snapshot serves registration and the boot inventory below, so both
	// observe the same declarations.
	extensions := composition.Extensions()
	if err := compose.RegisterExtensions(extensions); err != nil {
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

	// The boot state machine (A107/ADR-0061): bootstrap an empty database
	// from the deployment file, bind an existing singleton, refuse a
	// multi-workspace database. Runs before the listener opens — the API
	// never serves an unbound installation.
	deployCfg, err := deployconfig.Load(cfg.configPath)
	if err != nil {
		return err
	}
	if err := compose.EnsureInstallation(ctx, pool, logger, deployCfg); err != nil {
		return err
	}
	// Record the composed extension set when it changed since the last
	// boot — install/upgrade/removal happen in source, so this is where
	// they become observable (ADR-0069 §5).
	if err := compose.ObserveExtensionInventory(ctx, pool, logger, extensions); err != nil {
		return err
	}

	opts, closeSchemaPool, err := baseComposeOptions(ctx, cfg, deployCfg.Capture.FreemailExtra, pool, logger, stdout)
	if err != nil {
		return err
	}
	defer closeSchemaPool()

	resetOpts, err := passwordResetOptions(deployCfg, cfg.publicBaseURL, stdout)
	if err != nil {
		return err
	}
	opts = append(opts, resetOpts...)

	// The signing key enables the mutating /webhook-subscriptions surface
	// (create/rotate/replay); without it those paths answer an honest 503.
	if cfg.webhookKey != "" {
		webhookOpt, err := compose.WithWebhookKey(cfg.webhookKey)
		if err != nil {
			return fmt.Errorf("api: %w", err)
		}
		opts = append(opts, webhookOpt)
	}

	// The overlay budget meter records against Redis, the SAME server the
	// worker's poller uses, so force-fresh reads (this role) and poller
	// sweeps (cmd/worker) spend against ONE shared per-workspace-per-
	// incumbent count. A LAZY client (no boot ping): a split-deployment api
	// that cannot reach Redis must still boot — the meter then fails closed
	// (force-fresh degrades to the mirror), never a hard boot dependency.
	// cmd builds the meter (the raw-Redis dependency stays here, not in
	// compose); WithOverlayMeter Rebinds the Server's shared instance to it.
	overlayRDB := redis.NewClient(&redis.Options{Addr: cfg.redisAddr})
	defer func() {
		if err := overlayRDB.Close(); err != nil {
			logger.Warn("overlay budget: closing the redis client", "err", err)
		}
	}()
	overlayMeter := overlaybudget.New(overlayRDB, compose.OverlayBudgetConfig(deployCfg.EffectiveOverlayBudget()))
	opts = append(opts, compose.WithOverlayMeter(overlayMeter))

	stopRelay := func() {
		// No inline relay to stop unless --inline-relay wires one below.
	}
	if cfg.inlineRelay {
		busReady, stop, err := startInlineRelay(ctx, pool, cfg.redisAddr, cfg.webhookKey, cfg.webhookRetryInterval, logger)
		if err != nil {
			return err
		}
		stopRelay = stop
		opts = append(opts, busReady)
	}

	// ONE resolution point: coldStartOptions, offerDraftOptions and the
	// /readyz AI line all consume the same *compose.ModelPath rather than
	// each running their own copy of the declared-routing/--ai-fake/
	// neither switch (and, with it, their own Router, cache and budget).
	modelPath, aiState, assistantProfile, err := resolveModelPath(cfg.routingPath, cfg.fakeBrain, pool, deployCfg.AI.CapturePayloads, logger)
	if err != nil {
		return err
	}
	modelPath.SetCompanyContextEnabled(deployCfg.CompanyContext.TasksEnabled())
	opts = append(opts, compose.WithAiPayloadCaptureFlag(deployCfg.AI.CapturePayloads))
	opts = append(opts, coldStartOptions(modelPath)...)
	opts = append(opts, offerDraftOptions(pool, modelPath)...)
	opts = append(opts, compose.WithAssistantProfile(aiState, assistantProfile))
	if modelPath != nil {
		opts = append(opts, compose.WithAIMetrics(modelPath.WriteMetrics))
		// The backfill preview's cost pre-flight (ADR-0068) prices observed
		// history at this role's live tier bindings; self-gates to a no-op when
		// the backfill surface isn't wired. Appended after baseComposeOptions'
		// WithCaptureBackfill so the shared registry is already set.
		opts = append(opts, compose.WithBackfillEstimator(modelPath.Router()))
	}

	enqueueOpts, err := jobEnqueueOptions(pool, logger, modelPath)
	if err != nil {
		return err
	}
	embedReindex, err := embedReindexOption(pool, modelPath, logger)
	if err != nil {
		return err
	}
	opts = append(opts, embedReindex)
	opts = append(opts, enqueueOpts...)
	opts = append(opts, compose.WithCompanyContextRollout(string(deployCfg.CompanyContext.EffectiveRollout())))

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

// baseComposeOptions assembles the boot-optional compose.Options that
// don't depend on the inline relay's lifecycle (public base URL,
// blobstore, keyvault, the customfields schema pool) — split out of run()
// so that function stays inside the file's long-func budget. The
// returned close func releases whatever this stage opened (currently
// only the schema pool) and is always safe to call, even when nothing
// was opened.
func baseComposeOptions(ctx context.Context, cfg apiConfig, freemailExtra []string, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, func(), error) {
	var opts []compose.Option
	if cfg.publicBaseURL != "" {
		opts = append(opts, compose.WithPublicBaseURL(cfg.publicBaseURL))
	}

	blobOpts, err := blobstoreOptions(ctx, stdout)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, blobOpts...)

	// Validate the overlay backfill cap unconditionally: an invalid
	// MARGINCE_OVERLAY_BACKFILL_LIMIT is a boot error whether or not a vault
	// is configured (the value is only USED when a vault wires the overlay
	// surface, but "invalid → boot error, never a silent default" must not
	// hinge on that).
	overlayBackfillLimit, err := overlayBackfillLimitFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("api: %w", err)
	}
	kvOpts, err := keyvaultOptions(pool, stdout, overlayBackfillLimit)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, kvOpts...)

	// The Gmail and Graph transports ride the vault WithKeyvault wired, so
	// they must follow kvOpts (and graph follows gmail: WithGraphCapture
	// joins the connect registry WithGmailCapture builds when both are
	// configured).
	gmailOpts, err := gmailOptions(cfg, freemailExtra, pool, logger, stdout)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, gmailOpts...)
	graphOpts, err := graphOptions(cfg, pool, logger, stdout)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, graphOpts...)

	schemaOpts, closeSchemaPool, err := schemaPoolOptions(ctx, cfg.schemaDSN, stdout)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, schemaOpts...)

	return opts, closeSchemaPool, nil
}

// passwordResetOptions wires the A74 forgot-password flow when the
// deployment file configures outbound email. The emailed link needs a
// canonical external base — with email enabled, a missing
// --public-base-url is a boot error, never a link derived from a
// request Host.
func passwordResetOptions(deployCfg deployconfig.Config, publicBaseURL string, stdout io.Writer) ([]compose.Option, error) {
	if !deployCfg.Email.Enabled {
		return nil, nil
	}
	if publicBaseURL == "" {
		return nil, errors.New("api: email.enabled requires --public-base-url/MARGINCE_PUBLIC_BASE_URL (the reset link's canonical base)")
	}
	smtpPassword, err := deployCfg.Email.SMTPPassword()
	if err != nil {
		return nil, err
	}
	m := mailer.SMTP{
		Host:        deployCfg.Email.SMTP.Host,
		Port:        deployCfg.Email.SMTP.Port,
		Username:    deployCfg.Email.SMTP.Username,
		Password:    smtpPassword,
		FromAddress: deployCfg.Email.FromAddress,
	}
	_, _ = fmt.Fprintln(stdout, "api password reset enabled (outbound email configured)")
	return []compose.Option{compose.WithPasswordReset(m, publicBaseURL)}, nil
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

// schemaPoolOptions wires the customfields engine's owner-privileged
// schema-change pool — the second pgxpool the two
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
