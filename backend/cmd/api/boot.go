// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The api role's boot phases: settling the installation, the compose options
// each deployment-declared surface contributes, and the listener that serves
// them. main.go keeps the sequence those phases run in — this file holds the
// phases themselves.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/buildinfo"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// recordBootLedger writes what this binary is, into the ledger no request could
// ever cause a row in: the release it was published as, then the extension set it
// composed.
//
// The api is the one role that RECORDS the release rather than checking it against
// the record; compose/releaseversion.go carries why, and is the only place that
// argument is written down.
//
// One phase because the ordering between the two is not a caller's to choose. Both
// need the installation's workspace to exist, and both must land before the server
// is assembled, which loads the transport directory from the rows the second one
// writes.
func recordBootLedger(
	ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, extensions []extension.Extension,
) error {
	if err := compose.RecordInstallationRelease(ctx, pool, logger, buildinfo.ReleaseVersion); err != nil {
		return err
	}
	return compose.RecordComposition(ctx, pool, logger, extensions)
}

// bindInstallation settles what this installation IS before anything serves:
// the boot state machine (A107/ADR-0061) — bootstrap an empty database from the
// deployment file, bind an existing singleton, refuse a multi-workspace database
// — and then what it is ENTITLED to. Both belong to this phase for the same
// reason: each must hold before the listener opens, and each refuses the boot on
// an answer this build cannot honor. The deployment precondition that must fail
// before any of it is checked here first, so a boot that would publish an
// unreachable connector never gets as far as bootstrapping an organization.
//
// Returns the loaded deployment config every later boot phase reads, and the
// license watcher whose posture baseComposeOptions reports.
func bindInstallation(ctx context.Context, cfg apiConfig, pool *pgxpool.Pool, logger *slog.Logger) (deployconfig.Config, *licensecheck.Watcher, error) {
	deployCfg, err := deployconfig.Load(cfg.configPath, cfg.posture)
	if err != nil {
		return deployconfig.Config{}, nil, err
	}
	// The connector's OAuth audience and its advertised MCP resource are
	// both derived from --public-base-url, never from the Host header an
	// attacker controls — so the gate that turns the connector on cannot
	// be satisfied without it.
	if deployCfg.MCP.ConnectorEnabled {
		if cfg.publicBaseURL == "" {
			return deployconfig.Config{}, nil, errors.New("api: mcp.connector_enabled requires --public-base-url: the OAuth " +
				"audience and the advertised MCP resource must not be derived from the Host header")
		}
		if err := validatePublicBaseURL(cfg.publicBaseURL); err != nil {
			return deployconfig.Config{}, nil, err
		}
	}
	if err := compose.EnsureInstallation(ctx, pool, logger, deployCfg); err != nil {
		return deployconfig.Config{}, nil, err
	}
	// After the installation binds, so an installation that cannot bind fails on
	// that rather than on its license.
	// The deployment posture decides which license authorities are honored: a
	// production installation accepts one, and only a non-production one also
	// runs on a license minted for a test.
	license, err := compose.EnsureLicense(ctx, logger, deployCfg, cfg.posture, config.FromOS)
	if err != nil {
		return deployconfig.Config{}, nil, err
	}
	// The re-check runs on the process context — the signal context main built —
	// so it lives exactly as long as this role serves and needs no stop of its
	// own. What cancellation actually ends is the WAIT between ticks: wazero is
	// built without WithCloseOnContextDone, so a check already inside the module
	// runs to completion whatever happens to the context. That is why the loop
	// holds nothing open and answers nobody — there is no work here for a
	// shutdown to interrupt safely, only a reading the process was about to stop
	// reporting.
	go license.RunRecheck(ctx)
	return deployCfg, license, nil
}

// declaredSurfaceOptions wires the surfaces a deployment declares rather than
// ones the code assumes: the non-production reset posture, the MCP connector's
// route group, the forgot-password flow and the mutating webhook-subscription
// surface. Each stays absent — an honest 404 or 503 — when its declaration is.
//
// The reset lane comes back with the options because the endpoint is only half
// of it: the other half is a listener this process runs for its own lifetime,
// which run() starts once the handler exists.
func declaredSurfaceOptions(cfg apiConfig, deployCfg deployconfig.Config, pool, schemaPool *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger, stdout io.Writer) ([]compose.Option, *resetLane, error) {
	// The non-production admin data-reset endpoint (POST /v1/admin/reset-data):
	// absent this deployment posture, or in production, ResetData answers its
	// closed 404 default. schemaPool may be nil (no --schema-dsn configured);
	// the reset still succeeds, only the cf_* column finalize is skipped.
	//
	// ONE read of the switch serves the endpoint, the machinery behind it and
	// the /me field that offers the action, so the three can never disagree
	// about whether the reset is live. It is stated by the deployment rather
	// than inferred from MARGINCE_ENV, which is still read here for the posture
	// itself — a different question, and no longer a destructive one.
	allowDataReset := deployCfg.Operations.AllowDataReset
	// Said out loud at boot because each role reads its own --config: an api
	// armed beside a worker that was not given the file purges the workspace and
	// leaves that worker's caches resident until it restarts. A line in each log
	// makes the disagreement visible instead of silent.
	logger.Info("data reset", "armed", allowDataReset)
	opts := []compose.Option{
		compose.WithDataReset(schemaPool, deployCfg.Seeds, allowDataReset),
		// The same seeds reach the ADR-0105 claim route, so an installation
		// provisioned by claim lays down the module defaults this file asks
		// for rather than the built-in ones. Always applied, including the
		// zero value: an unconfigured `seeds` section means built-in defaults
		// on both provisioning paths, which is the behaviour to preserve.
		compose.WithBootstrapSeeds(deployCfg.Seeds),
		// The per-route upload ceilings (OPS-CFG-12). Always applied, including
		// for a file with no `uploads` section: EffectiveUploads resolves the
		// compiled-in defaults, so this stays the one place the numbers are
		// decided either way.
		compose.WithUploadLimits(deployCfg.EffectiveUploads()),
	}
	reset, err := newResetLane(allowDataReset, pool, rdb, logger)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, reset.opts...)
	// /me carries both facts, from two options, because they are two facts: the
	// deployment posture, and whether the reset exists here at all.
	opts = append(opts, compose.WithNonProduction(cfg.posture), compose.WithDataResetAvailable(allowDataReset))
	// Gate 1: the connector's whole route group — /mcp, the authorization
	// server and both discovery documents — exists only when the deployment
	// declared it. The boot check in bindInstallation already proved the
	// canonical base URL those routes advertise.
	if deployCfg.MCP.ConnectorEnabled {
		opts = append(opts, compose.WithMCPConnector())
	}

	passwordOpts, err := passwordResetOptions(deployCfg, cfg.publicBaseURL, stdout)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, passwordOpts...)

	// The signing key enables the mutating /webhook-subscriptions surface
	// (create/rotate/replay); without it those paths answer an honest 503.
	if cfg.webhookKey != "" {
		webhookOpt, err := compose.WithWebhookKey(cfg.webhookKey)
		if err != nil {
			return nil, nil, fmt.Errorf("api: %w", err)
		}
		opts = append(opts, webhookOpt)
	}
	return opts, reset, nil
}

// sharedRedisClient opens the ONE raw-Redis handle this role holds, plus the
// close func the caller defers for the process lifetime. Two surfaces share it:
// the overlay budget meter every force-fresh read spends against, and the
// non-production data reset, which purges the streams and announces itself over
// the same connection. Sharing is the point — a second client would be a second
// connection to the same server for no gain — and it is deliberately NOT the
// inline relay's client, which a split deployment (--inline-relay=false) does
// not build at all.
//
// A LAZY client (no boot ping): a split-deployment api that cannot reach Redis
// must still boot. The meter then fails closed (force-fresh degrades to the
// mirror) and a reset reports the unreachable bus as the error it is — neither
// is a hard boot dependency. Reachability is /readyz's job, and the inline
// relay's own client is the one that must ping (a stranded outbox row is a lost
// fact, a shed force-fresh read is not).
func sharedRedisClient(cfg apiConfig, logger *slog.Logger) (*redis.Client, func()) {
	rdb := redis.NewClient(&redis.Options{Addr: cfg.redisAddr})
	return rdb, func() {
		if err := rdb.Close(); err != nil {
			logger.Warn("closing the shared redis client", "err", err)
		}
	}
}

// overlayOptions wires the overlay's two cross-role edges: the budget every
// force-fresh read spends against, and the incumbent's inbound push.
func overlayOptions(cfg apiConfig, deployCfg deployconfig.Config, rdb *redis.Client, quotaMeter *agentquota.Meter, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, error) {
	// The overlay budget meter records against Redis, the SAME server the
	// worker's poller uses, so force-fresh reads (this role) and poller
	// sweeps (cmd/worker) spend against ONE shared per-workspace-per-
	// incumbent count. cmd builds the meter (the raw-Redis dependency stays
	// here, not in compose); WithOverlayMeter Rebinds the Server's shared
	// instance to it.
	overlayMeter := overlaybudget.New(rdb, compose.OverlayBudgetConfig(deployCfg.EffectiveOverlayBudget()))
	// The MCP-SESS-* counters ride the SAME Redis. The meter is built by the
	// caller rather than here, because the model path needs the same pointer to
	// charge MCP-SESS-COST against — two meters would count one agent's spend
	// in two windows, neither of them the one the gate reads.
	opts := []compose.Option{compose.WithOverlayMeter(overlayMeter), compose.WithAgentQuota(quotaMeter)}

	// The HubSpot webhook-as-signal receiver (OVA-WIRE-10) mounts only when the
	// app client secret is configured — it verifies the inbound v3 signature
	// and enqueues coalesced re-fetches on an insert-only River client (the
	// worker runs the overlayRefetchWorker). Absent the secret, /webhooks/hubspot
	// is not mounted at all.
	if cfg.hubspotAppSecret != "" {
		webhookInserter, werr := jobs.NewInserter(pool, logger)
		if werr != nil {
			return nil, werr
		}
		opts = append(opts, compose.WithOverlayWebhook(webhookInserter, cfg.hubspotAppSecret))
		_, _ = fmt.Fprintln(stdout, "api overlay webhook receiver enabled (/webhooks/hubspot)")
	}
	return opts, nil
}

// inlineRelayLane runs the outbox relay in this process unless the deployment
// split it out to cmd/worker (--inline-relay=false). The returned stop is a
// no-op in that split case, so the caller stops one lane either way.
func inlineRelayLane(ctx context.Context, cfg apiConfig, pool *pgxpool.Pool, logger *slog.Logger, stdout io.Writer) ([]compose.Option, func(), error) {
	if !cfg.inlineRelay {
		// No inline relay to stop: cmd/worker is running it.
		return nil, func() {}, nil
	}
	busReady, stop, err := startInlineRelay(ctx, pool, cfg.redisAddr, cfg.webhookKey, logger)
	if err != nil {
		return nil, nil, err
	}
	if cfg.webhookKey != "" {
		// Say the half this role does NOT do, by name. The inline consumer
		// makes first delivery attempts and parks the ones that fail; the
		// retry sweep is a River periodic job and this role runs no runner,
		// so an api-only installation never re-attempts a parked delivery
		// and nothing else here would ever say so.
		_, _ = fmt.Fprintln(stdout, "api webhook delivery inline (cg:webhooks first attempts); re-attempting a PARKED delivery needs cmd/worker")
	}
	return []compose.Option{busReady}, stop, nil
}

// modelSurfaceOptions resolves this role's model path and wires every AI
// surface over it, returning the path so the job-handoff lanes bind to the
// same one.
func modelSurfaceOptions(ctx context.Context, cfg apiConfig, deployCfg deployconfig.Config, pool *pgxpool.Pool, logger *slog.Logger) ([]compose.Option, *compose.ModelPath, error) {
	// ONE resolution point: coldStartOptions, offerDraftOptions and the
	// /readyz AI line all consume the same *compose.ModelPath rather than
	// each running their own copy of the declared-routing/--ai-fake/
	// neither switch (and, with it, their own Router, cache and budget).
	modelPath, aiState, assistantProfile, routingVersion, err := resolveModelPath(
		ctx, modelPathSpecFrom(cfg, deployCfg), pool, logger)
	if err != nil {
		return nil, nil, err
	}
	modelPath.SetCompanyContextEnabled(deployCfg.CompanyContext.TasksEnabled())
	opts := []compose.Option{compose.WithAiPayloadCaptureFlag(deployCfg.AI.CapturePayloads)}
	opts = append(opts, coldStartOptions(modelPath, routingVersion)...)
	opts = append(opts, offerDraftOptions(pool, modelPath)...)
	opts = append(opts, compose.WithAssistantProfile(aiState, assistantProfile))
	if modelPath != nil {
		opts = append(opts, compose.WithAIMetrics(modelPath.WriteMetrics))
		// The retrieval embed lane, on the REQUEST path — the same lane the
		// reindex job and the drift sweep take. Without it the hybrid arm's
		// vector half is unreachable from a request and every caller is served a
		// lexically ranked page.
		opts = append(opts, compose.WithRetrievalEmbedder(modelPath.Embedder))
		// The backfill preview's cost pre-flight (ADR-0068) prices observed
		// history at this role's live tier bindings; self-gates to a no-op when
		// the backfill surface isn't wired. Appended after baseComposeOptions'
		// WithCaptureBackfill so the shared registry is already set.
		opts = append(opts, compose.WithBackfillEstimator(modelPath.Router()))
	}
	return opts, modelPath, nil
}

// modelAndHandoffOptions wires everything this role builds over ONE resolved model
// path: the AI surfaces themselves, and the enqueue transports that hand work
// to cmd/worker over the same path. The path comes back because no Server field
// carries it — each role resolves its own — so the reset's cache flush can only
// drop what its router cached from here.
//
// quotaMeter is the SAME per-Passport volume meter the admission gate and the
// tool registry take through WithAgentQuota (MCP-SESS-COST): binding it here
// charges a model call to the agent that caused it, where the tokens are known.
// A model path bound to a different meter would meter an agent's spend into a
// window nothing else looks at, so the path leaves this function already bound.
func modelAndHandoffOptions(ctx context.Context, cfg apiConfig, deployCfg deployconfig.Config, pool *pgxpool.Pool, logger *slog.Logger, quotaMeter *agentquota.Meter) ([]compose.Option, *compose.ModelPath, error) {
	opts, modelPath, err := modelSurfaceOptions(ctx, cfg, deployCfg, pool, logger)
	if err != nil {
		return nil, nil, err
	}
	handoffOpts, err := workerHandoffOptions(pool, logger, modelPath)
	if err != nil {
		return nil, nil, err
	}
	// Nil is a SUPPORTED deployment, not an error: an api started with neither
	// --ai-routing nor --ai-fake resolves no model path at all, and every other
	// consumer here guards it the same way. There is no model call to charge in
	// that shape, so there is nothing to bind.
	if modelPath != nil {
		*modelPath = modelPath.WithAgentTokenSpend(compose.AgentTokenSpend{Meter: quotaMeter})
	}
	return append(opts, handoffOpts...), modelPath, nil
}

// workerHandoffOptions wires the api-side half of the work this role hands to
// cmd/worker: the outbound send path plus the deep-read, voice-build,
// rate-refresh and embed-reindex enqueue transports.
func workerHandoffOptions(pool *pgxpool.Pool, logger *slog.Logger, modelPath *compose.ModelPath) ([]compose.Option, error) {
	// The outbound send path: an accepted message stages a delivery row and
	// its transmit job on ONE transaction, so the 202 the caller gets means
	// something durable will actually carry it. Insert-only here (the worker
	// role works the queue) — the same shape as every other api-enqueued job.
	sendInserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return nil, err
	}
	opts := []compose.Option{
		compose.WithDelivery(compose.NewDeliveryStager(pool, sendInserter)),
		// The alarm a deferred send is accepted against. On the same inserter
		// as the delivery: a role that can promise a send can promise a later
		// one, and one that cannot refuses both rather than accepting a moment
		// nothing will wake at.
		compose.WithScheduleTimer(compose.NewScheduleTimer(sendInserter)),
	}

	enqueueOpts, err := jobEnqueueOptions(pool, logger, modelPath)
	if err != nil {
		return nil, err
	}
	embedReindex, err := embedReindexOption(pool, modelPath, logger)
	if err != nil {
		return nil, err
	}
	opts = append(opts, embedReindex)
	opts = append(opts, enqueueOpts...)
	return opts, nil
}

// serveUntilSignal serves the composed handler with explicit operational
// limits — a server without timeouts leaks connections under slow clients —
// until the listener fails or ctx is cancelled. Shutdown drains in-flight
// requests inside a bounded window of its own: the ctx that ended the serve is
// already cancelled, and reusing it would abandon those requests rather than
// give them time to finish.
func serveUntilSignal(ctx context.Context, cfg apiConfig, handler http.Handler, stdout io.Writer) error {
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
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

	//nolint:contextcheck // the drain gets its own context on purpose: ctx is already cancelled by the time this runs, and a cancelled one would abandon in-flight requests instead of bounding them.
	stopHTTP := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return stopHTTP()
	}
}
