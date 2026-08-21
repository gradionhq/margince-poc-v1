// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The contract HTTP surface: module transport handlers, aggregated by embedding
// (the Server struct below is the inventory), together cover every operation
// crmcontracts.ServerInterface declares. The chassis (headers, correlation, panic
// recovery) is platform/httpserver; what lives here is the wiring.

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/agentactivity"
	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/commissions"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/finance"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// Option, readinessChecks, and every With* role-customization function live
// in serveroptions.go — the per-process-role wiring surface, kept separate
// from the struct/router assembly below.

// New wires the modules and returns the ready http.Handler: contract
// routes under /v1, health probe, session middleware, panic recovery.
func New(pool *pgxpool.Pool, log *slog.Logger, opts ...Option) http.Handler {
	// The fieldcatalog seam for deals (newPeopleHandlers carries the full
	// note): active cf_* deal columns ride deal payloads on both surfaces.
	dealsH := deals.NewHandlers(InstallationDB(pool), DealsInstallation()).WithFieldCatalog(customfields.NewService(pool, nil))
	// Bootstrap happens at boot from deployment configuration
	// (EnsureInstallation, A107/ADR-0061) — the HTTP surface only ever
	// serves the already-bound singleton organization.
	identitySvc := identity.NewService(pool)
	authH := identity.NewHandlers(identitySvc)

	// The transport directory, loaded on the REAL assembly path rather than in
	// newServer: route-level tests construct that one directly with a pool that
	// was never dialled, and a struct constructor is the wrong place to reach a
	// database anyway. Every role that serves /v1 comes through here.
	loadChannelProviderDirectoryOrLog(pool, log)

	srv := newServer(pool, log, authH, dealsH)
	for _, opt := range opts {
		opt(&srv, pool)
	}
	srv.applySendPath(pool)
	// The tool registry is built HERE, after the options, on the Server that is
	// actually served — so every engine an option installed is one the tools can
	// reach. The rebuild each option performs keeps a half-configured Server
	// coherent while the loop runs; this one is what the surface ends up with.
	srv.rebuildToolRegistry(pool)

	api := contractAPI(srv, pool, identitySvc)
	// ONE identity.Service for the whole process: contractAPI's admission
	// gate and the connector's authenticate closure share this instance, so
	// they share its singleton cache and its clock.
	mux := operationalMux(srv, pool, log, identitySvc, api)

	return httpserver.RecoverPanics(log,
		httpserver.LimitBodies(bodyCeilingFor(uploadCeilings(srv.uploadLimits)),
			httpserver.SecureHeaders(mux)))
}

// newServer assembles the module handler sets. Every cross-module edge is
// injected here, or in the assembly step this calls for it
// (serverassembly.go) — never as a sibling import (ADR-0054).
func newServer(pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, dealsH dealsHandlers) Server {
	// The compiled-in ceilings, taken from the deployment-config defaults rather
	// than restated here: one place resolves a default, so no composition can
	// disagree with the file about what "unconfigured" means.
	limits := deployconfig.Config{}.EffectiveUploads()
	srv := Server{
		uploadLimits:        limits,
		authHandlers:        authH,
		peopleHandlers:      newPeopleHandlers(pool).WithUploadLimit(limits.LinkedInImport),
		dealsHandlers:       dealsH,
		contractsHandlers:   contracts.NewHandlers(InstallationDB(pool)),
		commissionsHandlers: commissions.NewHandlers(InstallationDB(pool)),
		activitiesHandlers:  newActivitiesHandlers(pool).WithUploadLimit(limits.Attachment),
		searchHandlers:      search.NewHandlers(InstallationDB(pool)),
		// Constructed, not merely embedded: the handler carries no nil-pool
		// branch, so the zero value would panic on the first authenticated
		// read rather than answer anything at all.
		jobHealthHandlers: jobHealthHandlers{pool: pool},
		// DSR fulfillment executes privacy's erase path — injected here so
		// consent never imports its sibling.
		consentHandlers:     consent.NewHandlers(InstallationDB(pool)).WithEraser(privacy.NewEraser(InstallationDB(pool))),
		collectionsHandlers: newCollectionsHandlers(pool),
		// The warm room ranks its contact edges by the §4 relationship
		// strength owned by people; injected through the adapter below so
		// signals never imports its sibling.
		financeHandlers: finance.NewHandlers(InstallationDB(pool), identity.BaseCurrencyOf),
		// No adapter is registered by default, which is the supported
		// "no provider connected" configuration (PI-AC-9): every surface
		// answers honestly and nothing can reach the network. WithProvider
		// is what registers one.
		integrationsHandlers: newIntegrationsHandlers(pool, nil, nil, nil),
		signalsHandlers:      signals.NewHandlers(InstallationDB(pool), signalStrength{people: people.NewStore(InstallationDB(pool))}),
		privacyHandlers:      privacy.NewHandlers(InstallationDB(pool), NewSettingsStore(pool)),
		// The fieldcatalog seam lets renewal_reminder's preview validate a
		// draft/stored (object, date_field) pair against the workspace's own
		// live custom-field catalog before ever building SQL around it — the
		// same edge dealsH wires above.
		automationHandlers: automation.NewHandlers(InstallationDB(pool)).WithFieldCatalog(customfields.NewService(pool, nil)),
		voiceHandlers:      ai.NewHandlers(InstallationDB(pool), NewSeatBudget(pool)),
		reportHandlers:     reportHandlers{engine: newReportEngine(pool)},
		// The Morning Brief always serves on the deterministic §10.1 floor;
		// the L2 re-order is opt-in via WithBrief (the api role's model path).
		Handlers:          briefs.NewHandlers(briefs.NewBriefEngine(pool, people.NewStore(InstallationDB(pool)))),
		Reads:             network.NewReads(pool),
		orgRollupHandlers: orgRollupHandlers{pool: pool, now: time.Now},
		strengthHandlers:  strengthHandlers{people: people.NewStore(InstallationDB(pool)), now: time.Now},
		// The schema-change pool is boot-optional; nil
		// here means Create/SetOptions stay their generated 501 until the
		// api role's WithSchemaPool rebuilds this over the real pool.
		customfieldsHandlers: customfields.NewHandlers(pool, nil),
		quotasHandlers:       quotas.NewHandlers(InstallationDB(pool), identity.BaseCurrencyOf),
		// The personal agent-activity read. Plain time.Now, NOT time.Now().UTC():
		// the store bounds "today" at midnight in the clock's own location, and a
		// UTC clock would name the wrong day on a non-UTC installation for the
		// hours either side of local midnight.
		agentActivityHandlers: agentactivity.NewHandlers(agentactivity.NewStore(InstallationDB(pool), time.Now), time.Now),
		// The accept-write needs no option to wire: it resolves the reading a
		// human was already shown (RD-AC-N-5) rather than producing one, so it
		// works wherever the readings do. An attachment that has never been read
		// simply has no grounded field to accept, and the accept says so.
		attachmentExtractionHandlers: attachmentExtractionHandlers{accept: NewExtractionAccept(pool)},
		// Outbound webhooks (E10/S-E10.6): the read surface works
		// unconditionally; create/rotate/replay need a deployment signing
		// key, wired by WithWebhookSigningKey (the api role sources it from
		// the environment). Without it those paths answer an honest 503.
		webhooksHandlers: newWebhookHandlers(pool, nil, log),
		log:              log,
		dealsStore:       deals.NewStore(InstallationDB(pool), DealsInstallation()),
		// Constructed unconditionally: WithKeyvault rebuilds
		// overlayHandlers over this SAME instance rather than minting a
		// second one, and contractAPI's Dispatcher spends force-fresh
		// reads against it too (see compose/overlay.go's NewOverlayMeter
		// doc). Fail-closed until WithOverlayMeter Rebinds it with the live
		// Redis client + config.
		overlayMeter: failClosedOverlayMeter(),
		// Fail-closed until WithAgentQuota Rebinds it: a role serving the agent
		// surface with no Redis cannot tell whether an agent has passed its
		// read bound, and answers that it has.
		quotaMeter: agentquota.New(nil, agentquota.Limits{}, agentquota.DefaultWindow),
	}
	// After the literal, because the decision path takes the SAME meter pointer
	// the gate and the registry take: a step-up refused against one counter and
	// released into another would read, from the human's side, as an approval
	// that did nothing.
	srv.approvalsHandlers = approvalsHandlersWithEffects(pool, srv.quotaMeter, log)
	srv.wireCaptureSettingsSurface(pool)
	srv.wireExportSurface(pool, log)
	srv.wireOnboardingSurface(pool)
	srv.wireSystemOfRecordReads(pool)
	// toolRegistry backs ListAgentTools AND the MCP tool transport; it carries
	// the vault-backed live-incumbent resolver that lets force-fresh reads and
	// HUMAN write-back reach HubSpot (an AGENT write is refused before it gets
	// there — egressbackstop.go).
	//
	// The tool registry is NOT built here: newServer returns by value and New
	// applies the options to its own copy, so a registry built on this one
	// would hold a Server that WithScrape and WithDeepRead never reach — an
	// enrich tool answering "not configured" while its REST twin works. New
	// builds it after the option loop, where the Server is the one served.
	// /me reports the workspace's system-of-record mode so the client can
	// gate its list UI (an overlay mirror refuses sort/filter dials). The
	// dispatch owns mode resolution; identity never imports overlay.
	srv.authHandlers = srv.WithSorMode(srv.sorDispatch.isOverlay)
	return srv
}
