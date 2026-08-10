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
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	// The composed extension set (ADR-0069): the generated module under
	// build/composition/ in a composed build, the committed vanilla stub
	// in a bare one — same import path either way.
	"github.com/gradionhq/margince/composition"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
		os.Exit(1)
	}
}

// run boots the HTTP server (plus, by default, the inline outbox relay).
// It is the boot sequence itself — flags, extensions, logger, pool,
// installation, the options each declared surface contributes, then the
// listener — and the order it reads in is the order the process depends on;
// boot.go holds the phases.
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

	deployCfg, err := bindInstallation(ctx, cfg, pool, logger)
	if err != nil {
		return err
	}
	// Record the composed extension set when it changed since the last
	// boot — install/upgrade/removal happen in source, so this is where
	// they become observable (ADR-0069 §5).
	if err := compose.ObserveExtensionInventory(ctx, pool, logger, extensions); err != nil {
		return err
	}

	opts, schemaPool, closeSchemaPool, err := baseComposeOptions(ctx, cfg, compose.CaptureConfigFromDeploy(deployCfg.Capture, logger), pool, logger, stdout)
	if err != nil {
		return err
	}
	defer closeSchemaPool()

	rdb, closeRedis := sharedRedisClient(cfg, logger)
	defer closeRedis()

	surfaceOpts, resetLane, err := declaredSurfaceOptions(cfg, deployCfg, pool, schemaPool, rdb, logger, stdout)
	if err != nil {
		return err
	}
	opts = append(opts, surfaceOpts...)

	// The per-Passport volume meter, built once and shared: the admission gate
	// and the tool registry take it through WithAgentQuota, and the model path
	// takes it below so a model call charges the agent that caused it. This is
	// the role that serves agent principals, so leaving it fail-closed would
	// refuse every agent read an api with Redis configured could count.
	quotaMeter := agentquota.New(rdb, agentquota.Limits{}, agentquota.DefaultWindow)
	overlayOpts, err := overlayOptions(cfg, deployCfg, rdb, quotaMeter, pool, logger, stdout)
	if err != nil {
		return err
	}
	opts = append(opts, overlayOpts...)

	relayOpts, stopRelay, err := inlineRelayLane(ctx, cfg, pool, logger, stdout)
	if err != nil {
		return err
	}
	// On EVERY return, and registered last so LIFO runs it FIRST — before the pool
	// it ships from closes. relay.go carries why only this stop ends the lane.
	defer stopRelay()
	opts = append(opts, relayOpts...)

	//nolint:contextcheck // boot-time wiring: the model path outlives any request context
	modelOpts, modelPath, err := modelAndHandoffOptions(cfg, deployCfg, pool, logger)
	if err != nil {
		return err
	}
	opts = append(opts, modelOpts...)
	// MCP-SESS-COST: the same meter the gate reads, charged where the tokens
	// are known. A model path bound to a different meter would meter an agent's
	// spend into a window nothing else looks at.
	//
	// Nil is a SUPPORTED deployment, not an error: an api started with neither
	// --ai-routing nor --ai-fake resolves no model path at all, and every other
	// consumer here guards it the same way. There is no model call to charge in
	// that shape, so there is nothing to bind.
	if modelPath != nil {
		*modelPath = modelPath.WithAgentTokenSpend(compose.AgentTokenSpend{Meter: quotaMeter})
	}
	opts = append(opts, compose.WithCompanyContextRollout(string(deployCfg.CompanyContext.EffectiveRollout())))

	apiHandler := compose.New(pool, logger, opts...)
	// Only now: the flush it listens with is captured while the options run.
	resetLane.listen(ctx, modelPath)
	return serveUntilSignal(ctx, cfg, apiHandler, stdout)
}

// validatePublicBaseURL refuses a base URL the connector cannot be reached at.
//
// It is validateBareOrigin under the one flag name that has always used it; the
// rule is shared with --mcp-apps-base-url, which needs exactly the same shape for
// a different reason (a path there would make the derived document URL
// unreachable rather than the advertised resource).
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
	return validateBareOrigin("--public-base-url", raw)
}

// validateBareOrigin holds one operator-supplied origin to scheme+host and
// nothing else. The flag name is a parameter so the refusal names the setting
// the operator actually typed.
func validateBareOrigin(flagName, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("api: %s %q is not a URL: %w", flagName, raw, err)
	}
	// Userinfo is refused BEFORE any error that quotes the value, because that
	// is where a password would be: an origin carrying credentials would put
	// them in this boot error and every log line that copies it.
	if parsed.User != nil {
		return fmt.Errorf("api: %s carries userinfo; it must be a bare origin "+
			"(value withheld: it may contain a credential)", flagName)
	}
	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return fmt.Errorf("api: %s %q needs an http or https scheme, got %q", flagName, raw, parsed.Scheme)
	// Hostname(), not Host: Host keeps the port, so ":8080" is a non-empty
	// authority that names no host at all.
	case parsed.Hostname() == "":
		return fmt.Errorf("api: %s %q names no host", flagName, raw)
	// Exactly "" or "/" — NOT a trimmed comparison. url.Parse decodes as it
	// goes, so "//" and "/%2F" both arrive as a path that trims to empty while
	// the RAW value is what gets published: appending "/mcp" to either yields a
	// URL with a doubled or encoded separator that no client resolves.
	case parsed.Path != "" && parsed.Path != "/":
		return fmt.Errorf("api: %s %q must be a bare origin: a URL is derived by appending "+
			"a path to it, so a path here produces one nothing resolves", flagName, raw)
	case parsed.RawQuery != "" || parsed.Fragment != "":
		return fmt.Errorf("api: %s %q must be a bare origin, with no query or fragment", flagName, raw)
	}
	return nil
}

// baseComposeOptions assembles the boot-optional compose.Options that
// don't depend on the inline relay's lifecycle (public base URL,
// blobstore, keyvault, the customfields schema pool). The
// returned schema pool (nil when --schema-dsn is unset) is also handed to
// WithDataReset by declaredSurfaceOptions so the reset endpoint's cf_*
// finalize runs on the same owner connection the customfields engine
// uses. The returned close func releases whatever this stage opened
// (currently only the schema pool) and is always safe to call, even when
// nothing was opened.
func baseComposeOptions(ctx context.Context, cfg apiConfig, capCfg compose.CaptureConfig, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, *pgxpool.Pool, func(), error) {
	var opts []compose.Option
	// Record the deployment's capture suppression-list config first, so the
	// registry rebuilds in WithKeyvault/WithGraphCapture apply it too — not just
	// the Gmail path WithGmailCapture threads it into (ADR-0072).
	opts = append(opts, compose.WithCaptureConfig(capCfg))
	// Always applied, including the empty default: /metrics stays off until
	// an operator sets --metrics-token, never silently open.
	opts = append(opts, compose.WithMetricsToken(cfg.metricsToken))
	if cfg.publicBaseURL != "" {
		opts = append(opts, compose.WithPublicBaseURL(cfg.publicBaseURL))
		// The canonical MCP resource (RFC 9728) is the same configured
		// base, never a request-derived origin — see the boot check in
		// bindInstallation that requires this flag once the connector
		// gate is on.
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
		return nil, nil, nil, err
	}
	opts = append(opts, blobOpts...)

	// Validate the overlay backfill cap unconditionally: an invalid
	// MARGINCE_OVERLAY_BACKFILL_LIMIT is a boot error whether or not a vault
	// is configured (the value is only USED when a vault wires the overlay
	// surface, but "invalid → boot error, never a silent default" must not
	// hinge on that).
	overlayBackfillLimit, err := overlayBackfillLimitFromEnv()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("api: %w", err)
	}
	kvOpts, err := keyvaultOptions(pool, stdout, overlayBackfillLimit)
	if err != nil {
		return nil, nil, nil, err
	}
	opts = append(opts, kvOpts...)

	// The Gmail and Graph transports ride the vault WithKeyvault wired, so
	// they must follow kvOpts (and graph follows gmail: WithGraphCapture
	// joins the connect registry WithGmailCapture builds when both are
	// configured).
	gmailOpts, err := gmailOptions(cfg, capCfg, pool, logger, stdout)
	if err != nil {
		return nil, nil, nil, err
	}
	opts = append(opts, gmailOpts...)
	graphOpts, err := graphOptions(cfg, pool, logger, stdout)
	if err != nil {
		return nil, nil, nil, err
	}
	opts = append(opts, graphOpts...)

	schemaOpts, schemaPool, closeSchemaPool, err := schemaPoolOptions(ctx, cfg.schemaDSN, stdout)
	if err != nil {
		return nil, nil, nil, err
	}
	opts = append(opts, schemaOpts...)

	return opts, schemaPool, closeSchemaPool, nil
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
	// The link base rides compose.WithPublicBaseURL, assembled with the base
	// options — this option carries the transport alone.
	return []compose.Option{compose.WithPasswordReset(m)}, nil
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
// need, and the reset-data endpoint's cf_* column finalize rides the
// same pool — only when --schema-dsn/MARGINCE_SCHEMA_DSN is set. Without
// one those two operations stay their generated 501
// (ErrSchemaChangesUnavailable) and reset skips the finalize step; the
// returned pool is nil in that case, and the close func is a no-op, so
// run() can always defer it unconditionally.
func schemaPoolOptions(ctx context.Context, schemaDSN string, stdout io.Writer) ([]compose.Option, *pgxpool.Pool, func(), error) {
	if schemaDSN == "" {
		return nil, nil, func() {}, nil
	}
	// The engine serializes every ALTER on a table behind a transaction-scoped
	// advisory lock (customfields.beginSchemaChange), so this pool never runs
	// more than one DDL statement per table at a time; a handful of
	// connections is a deliberately small footprint for a rare admin path,
	// next to the app pool's MaxConns=16 default (database.NewPool).
	pool, err := database.NewPool(ctx, withPoolMaxConns(schemaDSN, 3))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("api: schema pool: %w", err)
	}
	_, _ = fmt.Fprintln(stdout, "api custom-field schema changes enabled (schema pool configured)")
	return []compose.Option{compose.WithSchemaPool(pool)}, pool, pool.Close, nil
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
