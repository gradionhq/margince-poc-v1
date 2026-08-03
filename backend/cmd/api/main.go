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
	"net/url"
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
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
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
	// The connector's OAuth audience and its advertised MCP resource are
	// both derived from --public-base-url, never from the Host header an
	// attacker controls — so the gate that turns the connector on cannot
	// be satisfied without it.
	if deployCfg.MCP.ConnectorEnabled {
		if cfg.publicBaseURL == "" {
			return errors.New("api: mcp.connector_enabled requires --public-base-url: the OAuth " +
				"audience and the advertised MCP resource must not be derived from the Host header")
		}
		if err := validatePublicBaseURL(cfg.publicBaseURL); err != nil {
			return err
		}
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

	opts, closeSchemaPool, err := baseComposeOptions(ctx, cfg, compose.CaptureConfigFromDeploy(deployCfg.Capture, logger), pool, logger, stdout)
	if err != nil {
		return err
	}
	defer closeSchemaPool()

	// Gate 1: the connector's whole route group — /mcp, the authorization
	// server and both discovery documents — exists only when the deployment
	// declared it. The boot check above already proved the canonical base URL
	// those routes advertise.
	if deployCfg.MCP.ConnectorEnabled {
		opts = append(opts, compose.WithMCPConnector())
	}

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

	// The HubSpot webhook-as-signal receiver (OVA-WIRE-10) mounts only when the
	// app client secret is configured — it verifies the inbound v3 signature
	// and enqueues coalesced re-fetches on an insert-only River client (the
	// worker runs the overlayRefetchWorker). Absent the secret, /webhooks/hubspot
	// is not mounted at all.
	if cfg.hubspotAppSecret != "" {
		webhookInserter, werr := jobs.NewInserter(pool, logger)
		if werr != nil {
			return werr
		}
		opts = append(opts, compose.WithOverlayWebhook(webhookInserter, cfg.hubspotAppSecret))
		_, _ = fmt.Fprintln(stdout, "api overlay webhook receiver enabled (/webhooks/hubspot)")
	}

	stopRelay := func() {
		// No inline relay to stop unless --inline-relay wires one below.
	}
	if cfg.inlineRelay {
		busReady, stop, err := startInlineRelay(ctx, pool, cfg.redisAddr, cfg.webhookKey, logger)
		if err != nil {
			return err
		}
		stopRelay = stop
		opts = append(opts, busReady)
		if cfg.webhookKey != "" {
			// Say the half this role does NOT do, by name. The inline consumer
			// makes first delivery attempts and parks the ones that fail; the
			// retry sweep is a River periodic job and this role runs no runner,
			// so an api-only installation never re-attempts a parked delivery
			// and nothing else here would ever say so.
			_, _ = fmt.Fprintln(stdout, "api webhook delivery inline (cg:webhooks first attempts); re-attempting a PARKED delivery needs cmd/worker")
		}
	}

	// ONE resolution point: coldStartOptions, offerDraftOptions and the
	// /readyz AI line all consume the same *compose.ModelPath rather than
	// each running their own copy of the declared-routing/--ai-fake/
	// neither switch (and, with it, their own Router, cache and budget).
	//nolint:contextcheck // boot-time wiring: the model path outlives any request context
	modelPath, aiState, assistantProfile, routingVersion, err := resolveModelPath(
		cfg.routingPath, cfg.fakeBrain, pool, deployCfg.AI.CapturePayloads, logger)
	if err != nil {
		return err
	}
	modelPath.SetCompanyContextEnabled(deployCfg.CompanyContext.TasksEnabled())
	opts = append(opts, compose.WithAiPayloadCaptureFlag(deployCfg.AI.CapturePayloads))
	opts = append(opts, coldStartOptions(modelPath, routingVersion)...)
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

	// The outbound send path: an accepted message stages a delivery row and
	// its transmit job on ONE transaction, so the 202 the caller gets means
	// something durable will actually carry it. Insert-only here (the worker
	// role works the queue) — the same shape as every other api-enqueued job.
	sendInserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return err
	}
	opts = append(opts, compose.WithDelivery(compose.NewDeliveryStager(pool, sendInserter)))

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
// validatePublicBaseURL refuses a base URL the connector cannot be reached at.
// Presence alone is not enough: every value here is copied verbatim into the
// OAuth audience, the RFC 9728 protected-resource document and the advertised
// MCP URL, and a client dereferences all three exactly as given. A malformed
// or path-bearing value therefore does not fail at boot — it boots a connector
// that advertises somewhere nobody can connect to, which looks like a client
// bug from every side.
//
// A trailing slash is accepted and trimmed downstream; anything else that
// would change what the URL MEANS — a scheme other than http(s), a missing
// host, or a path, query or fragment — is refused rather than silently
// normalized, because guessing what an operator meant is how the wrong origin
// ends up published.
func validatePublicBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("api: --public-base-url %q is not a URL: %w", raw, err)
	}
	// Userinfo is refused BEFORE any error that quotes the value, because that
	// is where a password would be: an origin carrying credentials would put
	// them in this boot error and every log line that copies it.
	if parsed.User != nil {
		return errors.New("api: --public-base-url carries userinfo; it must be a bare origin " +
			"(value withheld: it may contain a credential)")
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return fmt.Errorf("api: --public-base-url %q needs an http or https scheme, got %q", raw, parsed.Scheme)
	// Hostname(), not Host: Host keeps the port, so ":8080" is a non-empty
	// authority that names no host at all.
	case parsed.Hostname() == "":
		return fmt.Errorf("api: --public-base-url %q names no host", raw)
	// Exactly "" or "/" — NOT a trimmed comparison. url.Parse decodes as it
	// goes, so "//" and "/%2F" both arrive as a path that trims to empty while
	// the RAW value is what gets published: appending "/mcp" to either yields a
	// URL with a doubled or encoded separator that no client resolves.
	case parsed.Path != "" && parsed.Path != "/":
		return fmt.Errorf("api: --public-base-url %q must be a bare origin: the MCP resource is "+
			"derived by appending /mcp, so a path here publishes an unreachable URL", raw)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		return fmt.Errorf("api: --public-base-url %q must be a bare origin, with no query or fragment", raw)
	}
	return nil
}

func baseComposeOptions(ctx context.Context, cfg apiConfig, capCfg compose.CaptureConfig, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, func(), error) {
	var opts []compose.Option
	// Record the deployment's capture suppression-list config first, so the
	// registry rebuilds in WithKeyvault/WithGraphCapture apply it too — not just
	// the Gmail path WithGmailCapture threads it into (ADR-0072).
	opts = append(opts, compose.WithCaptureConfig(capCfg))
	if cfg.publicBaseURL != "" {
		opts = append(opts, compose.WithPublicBaseURL(cfg.publicBaseURL))
		// The canonical MCP resource (RFC 9728) is the same configured
		// base, never a request-derived origin — see the boot check in
		// run() that requires this flag once the connector gate is on.
		opts = append(opts, compose.WithMCPResource(strings.TrimSuffix(cfg.publicBaseURL, "/")+"/mcp"))
	}
	// An operator-shortened access token, applied to both mints of a
	// connection's life. Zero is "not configured", which keeps the passport
	// default — declared by omission, never a silent guess.
	if cfg.oauthAccessTokenTTL != 0 {
		opts = append(opts, compose.WithOAuthAccessTokenTTL(cfg.oauthAccessTokenTTL))
	}
	// The channel-connection surface needs no public origin of its own: Telegram
	// ingress polls, so nothing is ever told where to reach this installation.
	// It must precede kvOpts below, which hands it the vault it seals with.
	opts = append(opts, compose.WithChannelSurface())

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
	gmailOpts, err := gmailOptions(cfg, capCfg, pool, logger, stdout)
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
