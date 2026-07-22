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
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"

	// The DE jurisdiction pack compiles into every edge binary of this
	// DE-first deployment (ADR-0042: composition by require-set).
	_ "github.com/gradionhq/margince/backend/internal/modules/de"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
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
	dsn                  string
	configPath           string
	schemaDSN            string
	addr                 string
	redisAddr            string
	inlineRelay          bool
	routingPath          string
	fakeBrain            bool
	logLevel             string
	logFormat            string
	publicBaseURL        string
	apiBaseURL           string
	gmailClientID        string
	gmailClientSecret    string
	gmailPushToken       string
	gmailPushAudience    string
	gmailPushSA          string
	gmailJWKSURL         string
	graphClientID        string
	graphClientSecret    string
	graphTenant          string
	connectorStateKey    string
	webhookKey           string
	webhookRetryInterval time.Duration
}

// parseAPIFlags parses and validates the boot flags; the DSN is the one
// dependency without a sane default, so its absence fails the boot here.
func parseAPIFlags(args []string) (apiConfig, error) {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	var cfg apiConfig
	fs.StringVar(&cfg.dsn, "dsn", os.Getenv("MARGINCE_DSN"), "Postgres DSN (runtime app role)")
	fs.StringVar(&cfg.configPath, "config", envOr("MARGINCE_CONFIG", "margince.yaml"),
		"path to the deployment configuration file (A107/ADR-0061: bootstrap + auth); a missing file boots an existing installation but cannot bootstrap an empty database")
	fs.StringVar(&cfg.schemaDSN, "schema-dsn", os.Getenv("MARGINCE_SCHEMA_DSN"),
		"Postgres DSN (owner role) for the customfields runtime-DDL pool; unset = the two schema-change operations answer 501")
	fs.StringVar(&cfg.addr, "addr", ":8080", "listen address")
	fs.StringVar(&cfg.redisAddr, "redis", envOr("MARGINCE_REDIS", "localhost:56379"), "Redis address (event bus)")
	fs.BoolVar(&cfg.inlineRelay, "inline-relay", true, "run the outbox relay in this process (false when cmd/worker runs it)")
	fs.StringVar(&cfg.routingPath, "ai-routing", os.Getenv("MARGINCE_AI_ROUTING"), "path to ai-routing.yaml; enables the cold-start read-back")
	fs.BoolVar(&cfg.fakeBrain, "ai-fake", false, "drive the AI surfaces with the offline fake model (dev/test only)")
	fs.StringVar(&cfg.logLevel, "log-level", envOr("MARGINCE_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	fs.StringVar(&cfg.logFormat, "log-format", envOr("MARGINCE_LOG_FORMAT", "text"), "log format: text|json")
	fs.StringVar(&cfg.publicBaseURL, "public-base-url", os.Getenv("MARGINCE_PUBLIC_BASE_URL"), "canonical external scheme+host for buyer-facing links (RFC 8058 unsubscribe); required to send marketing mail and for the Gmail/Graph OAuth callback")
	fs.StringVar(&cfg.gmailClientID, "gmail-client-id", os.Getenv("MARGINCE_GMAIL_CLIENT_ID"), "Google OAuth client id for the Gmail capture connector; with the secret, state key and public-base-url, enables /connectors/gmail/*")
	fs.StringVar(&cfg.gmailClientSecret, "gmail-client-secret", os.Getenv("MARGINCE_GMAIL_CLIENT_SECRET"), "Google OAuth client secret for the Gmail capture connector")
	fs.StringVar(&cfg.gmailPushToken, "gmail-push-token", os.Getenv("MARGINCE_GMAIL_PUSH_TOKEN"), "shared secret on the Pub/Sub push subscription URL; enables POST /webhooks/gmail-push (empty = route absent)")
	fs.StringVar(&cfg.gmailPushAudience, "gmail-push-audience", os.Getenv("MARGINCE_GMAIL_PUSH_AUDIENCE"), "OIDC audience the Pub/Sub push subscription mints tokens for (this endpoint's public URL); with --gmail-push-service-account, the push webhook also verifies Google's OIDC token")
	fs.StringVar(&cfg.gmailPushSA, "gmail-push-service-account", os.Getenv("MARGINCE_GMAIL_PUSH_SERVICE_ACCOUNT"), "the Google service account email that signs Pub/Sub push OIDC tokens; verified as the token's email claim")
	fs.StringVar(&cfg.gmailJWKSURL, "gmail-jwks-url", os.Getenv("MARGINCE_GMAIL_JWKS_URL"), "override Google's OIDC JWKS URL; test/dev only")
	fs.StringVar(&cfg.graphClientID, "graph-client-id", os.Getenv("MARGINCE_GRAPH_CLIENT_ID"), "Microsoft (Entra) application id for the Outlook/M365 capture connector; with the secret, state key and public-base-url, enables /connectors/graph/*")
	fs.StringVar(&cfg.graphClientSecret, "graph-client-secret", os.Getenv("MARGINCE_GRAPH_CLIENT_SECRET"), "Microsoft client secret for the Outlook/M365 capture connector")
	fs.StringVar(&cfg.graphTenant, "graph-tenant", os.Getenv("MARGINCE_GRAPH_TENANT"), "Microsoft identity tenant for the consent endpoint (default: common — any organization)")
	fs.StringVar(&cfg.apiBaseURL, "api-base-url", os.Getenv("MARGINCE_API_BASE_URL"), "the api's externally-reachable base for the OAuth callback redirect_uri; defaults to --public-base-url (same-origin deployments), set only when the api is on a different origin than the SPA (e.g. dev)")
	fs.StringVar(&cfg.connectorStateKey, "connector-state-key", os.Getenv("MARGINCE_CONNECTOR_STATE_KEY"), "HMAC key (>=32 bytes) signing the OAuth connect `state`; required for the Gmail and Graph connect flows")
	fs.StringVar(&cfg.webhookKey, "webhook-key", os.Getenv("MARGINCE_WEBHOOK_KEY"), "base64 32-byte key sealing outbound-webhook signing secrets; enables the mutating /webhook-subscriptions surface, and (with --inline-relay) the cg:webhooks delivery consumer + retry sweep. Empty = those paths answer 503 and no inline delivery runs.")
	fs.DurationVar(&cfg.webhookRetryInterval, "webhook-retry-interval", 5*time.Second, "outbound-webhook retry-sweep tick interval (inline-relay only)")
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

	deepRead, err := deepReadOption(pool, logger)
	if err != nil {
		return err
	}
	opts = append(opts, deepRead)
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

// startInlineRelay boots the in-process outbox relay. The bus is not
// optional plumbing: without a relay every committed write strands its
// outbox row, so an unreachable Redis fails the boot the same way an
// unreachable Postgres does (B-EP04.1). The returned compose option makes
// the bus a readiness dependency of THIS process (a split deployment's
// api is ready on Postgres alone); the stop function runs after the HTTP
// server shuts down, so late-committing requests usually ship before
// exit — anything still unshipped waits durably in the outbox for the
// next boot, and shutdown loses no events.
//
//nolint:contextcheck // the relay + webhook consumer are process-lifetime lanes, deliberately rooted at context.Background() and stopped by the returned stop(), never by the request ctx.
func startInlineRelay(ctx context.Context, pool *pgxpool.Pool, redisAddr, webhookKey string, webhookRetryInterval time.Duration, logger *slog.Logger) (compose.Option, func(), error) {
	rdb, err := events.NewClient(ctx, redisAddr)
	if err != nil {
		return nil, nil, err
	}
	// The relay/consumer lanes outlive any single request by design — a bus
	// lane must drain on shutdown, not cancel with an inbound request — so
	// they run on a fresh cancelable context, not the request ctx.
	relayCtx, cancel := context.WithCancel(context.Background())
	var relay sync.WaitGroup
	relay.Go(func() {
		events.NewRelay(pool, rdb, logger).Run(relayCtx)
	})
	// When a webhook signing key is configured, this single-process role
	// also runs the cg:webhooks delivery consumer + retry sweep (in a split
	// deployment cmd/worker owns them). Owner-scoped fan-out (BYO-EVT-4)
	// rides the same deliverer.
	if webhookKey != "" {
		if derr := startInlineWebhookDelivery(relayCtx, &relay, rdb, pool, webhookKey, webhookRetryInterval, logger); derr != nil {
			cancel()
			if cerr := rdb.Close(); cerr != nil {
				logger.Warn("closing bus client", "err", cerr)
			}
			return nil, nil, fmt.Errorf("api: %w", derr)
		}
	}
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

// startInlineWebhookDelivery builds the owner-scoped delivery deliverer and
// registers its cg:webhooks consumer + retry sweep on the relay group. Kept
// out of startInlineRelay so that function stays flat; both goroutines share
// the relay's lifecycle context and WaitGroup.
func startInlineWebhookDelivery(ctx context.Context, relay *sync.WaitGroup, rdb *redis.Client, pool *pgxpool.Pool, webhookKey string, retryInterval time.Duration, logger *slog.Logger) error {
	deliverer, err := compose.NewWebhookDeliverer(pool, webhookKey, logger)
	if err != nil {
		return err
	}
	var group kevents.Group
	for _, g := range kevents.Groups() {
		if g.Name == "cg:webhooks" {
			group = g
		}
	}
	relay.Go(func() {
		sub := events.NewSubscriber(rdb, group, events.Dedupe(rdb, group.Name, deliverer.HandleEvent), logger)
		if err := sub.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("subscriber cg:webhooks", "err", err)
		}
	})
	relay.Go(func() { deliverer.RunRetrySweep(ctx, retryInterval) })
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
