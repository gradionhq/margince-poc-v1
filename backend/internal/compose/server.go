// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The contract HTTP surface: module transport handlers, aggregated by
// embedding (the Server struct below is the inventory), together cover
// every operation crmcontracts.ServerInterface declares. The chassis
// (headers, correlation, panic recovery) is platform/httpserver; what
// lives here is the wiring.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Server satisfies crmcontracts.ServerInterface by embedding: every
// module transport handler set together covers the full contract
// surface.
type Server struct {
	authHandlers
	peopleHandlers
	dealsHandlers
	activitiesHandlers
	approvalsHandlers
	searchHandlers
	consentHandlers
	collectionsHandlers
	signalsHandlers
	privacyHandlers
	automationHandlers
	voiceHandlers
	reportHandlers
	briefs.Handlers
	// The relationship-graph reads (ADR-0078): who knows this contact, and
	// how a deal is covered.
	network.Reads
	coldstartHandlers
	companyHandlers
	onboardingStateHandlers
	siteReadHandlers
	scrapeHandlers
	connectorHandlers
	backfillHandlers
	captureExclusionHandlers
	captureSettingsHandlers
	channelHandlers
	filteredExportHandlers
	overlayExportHandlers
	orgRollupHandlers
	strengthHandlers
	customfieldsHandlers
	quotasHandlers
	attachmentExtractionHandlers
	overlayHandlers
	embedReindexHandlers
	rateRefreshHandlers
	webhooksHandlers
	org360Handlers
	orgBriefHandlers

	// gmailPush is the Pub/Sub push webhook (built on the shared chassis,
	// webhook.go), injected by WithGmailPush only when a subscription token
	// is configured — the route is absent otherwise, never open.
	gmailPush http.Handler

	// overlayWebhook is the HubSpot webhook-as-signal receiver (OVA-WIRE-10),
	// injected by WithOverlayWebhook only when the overlay app secret is
	// configured — the route is absent otherwise, never an open unverified
	// endpoint.
	overlayWebhook http.Handler

	// mcpConnectorEnabled is the remote-connector deployment gate, set by
	// WithMCPConnector from the deployment file. It governs the connector as
	// ONE group — transport, authorization server, both discovery documents —
	// and routes.go, where the group is mounted, carries why.
	mcpConnectorEnabled bool

	// mcpAllowedOrigin is the scheme+host the connector's Origin guard
	// admits — derived by WithMCPResource from the configured
	// --public-base-url, never from a request header a caller controls.
	mcpAllowedOrigin string

	// busReady is the /readyz bus probe, injected only by the process
	// role that runs the inline relay — a split deployment's api answers
	// ready on Postgres alone.
	busReady func(context.Context) error

	// blob is the object store, injected by WithBlobstore. When configured
	// it feeds a /readyz probe and backs the attachment handlers; nil means
	// a role that stores no objects.
	blob blobstore.Store

	// vault is the secret store, injected by WithKeyvault. When configured
	// it feeds a /readyz probe and backs the capture connector-credential
	// path; nil means a role that resolves no stored connector credentials.
	vault keyvault.Vault

	// captureConfig is the deployment's capture suppression-list config
	// (CAP-PARAM-5/6, ADR-0072), injected by WithCaptureConfig. The options
	// that rebuild the capture registry (WithKeyvault, WithGraphCapture) read
	// it so the transactional/free-mail additions apply on EVERY registry, not
	// only the Gmail one WithGmailCapture threads it into. Zero value = the
	// pinned baselines.
	captureConfig CaptureConfig

	// gmailAppConfigured records whether this DEPLOYMENT configured a Google app
	// that could transmit under a user's mailbox grant — the one fact the send
	// pre-flight cannot read off a capture_connection row, since the grant
	// survives the app being removed and a mailbox connected on one deployment
	// reads the same on another.
	//
	// It is a deployment fact, not a role fact: WithGmailCapture records it
	// before its own transport gate and off canSync, so an installation holding
	// client credentials but no state key — which mounts no api-side connect
	// transport yet sends perfectly well from the worker — still counts as
	// configured. False is the honest default for a composition never told about
	// a Google app at all. Gmail is the only provider with a field here because
	// it is the only one comms.SendScopeFor gives a send scope.
	gmailAppConfigured bool

	// schemaPoolReady is the /readyz schema-pool probe, injected only by
	// WithSchemaPool — a role that never mounted --schema-dsn declares
	// that by omission (customfields.Create/SetOptions stay their
	// generated 501) rather than probing a pool it
	// doesn't have.
	schemaPoolReady func(context.Context) error

	// log is the process logger, shared with the optional engines an
	// option wires (e.g. the brief L2 ranker's degradation warnings).
	log *slog.Logger

	// offerDrafter is the AI-drafted offer regeneration orchestrator (arc
	// 4b), injected by WithOfferDraft. Without it, offerregenerate.go's
	// RegenerateOffer shadow stays mechanical-only — the same "declared
	// or absent, never a silent default" posture as
	// coldstartHandlers/scrapeHandlers.
	offerDrafter *offerDrafter

	// dealsStore backs that same shadow: a direct Store.RegenerateOffer
	// call, so the mechanical mint's Offer can reach offerDrafter before
	// the response is written — a separate instance from dealsHandlers'
	// own store, the same split offerDrafter itself already uses.
	dealsStore *deals.Store
	// send is the outbound-send deployment configuration (public base URL,
	// delivery machinery, mailbox pre-flight) every send transport shares.
	// The options that set it rebuild BOTH the activities handlers and the
	// tool registry, so the HTTP surface and the MCP surface can never be
	// configured differently.
	send SendPath

	// replyDrafter is the shared HTTP/REST-agent reply path. Nil preserves
	// the activities module's deterministic floor.
	replyDrafter activities.EmailDrafter
	// toolRegistry backs ListAgentTools — the same *agents.Registry the MCP transport uses.
	toolRegistry *agents.Registry

	// aiMetrics is the /metrics renderer for this role's AI surfaces, set
	// by WithAIMetrics. coldStartOptions and offerDraftOptions each
	// resolve the declared routing file into their own ModelPath — their
	// own in-process *ai.Router — but every Router increments the SAME
	// process-wide callMetrics collector (ai/metrics.go), so both
	// registrations point at one shared renderer: last-wins is correct
	// and /metrics still reports the single honest total exactly once.
	// nil means an AI-less role reports no AI counters at all.
	aiMetrics func(io.Writer)
	aiState   string // the /readyz AI line (aistate.go); never a readiness gate

	// overlayMeter is this Server's REST-surface OVB meter — what
	// contractAPI's Dispatcher force-fresh reads spend against and what
	// GetOverlayBudget reports (once WithKeyvault rebuilds overlayHandlers
	// over it). Its windows live in Redis (see compose/overlay.go's
	// NewOverlayMeter doc), so it shares a per-workspace-per-incumbent count
	// with cmd/worker's poller meter over the same Redis; threading this one
	// instance through both wiring points is convention, no longer a
	// correctness requirement.
	// Always non-nil (newServer constructs it unconditionally, fail-closed
	// with no Redis): a role that never calls WithOverlayMeter answers shed
	// for every force-fresh read (never spends live quota it cannot
	// account for), and a role with no vault never reaches GetOverlayBudget
	// at all. WithOverlayMeter Rebinds this shared pointer to the live
	// Redis-backed meter at boot.
	overlayMeter *overlaybudget.Meter
	// overlayBackfillLimit bounds the overlay initial mirror backfill per
	// object class (dev/demo — WithOverlayBackfillLimit); 0 is uncapped.
	overlayBackfillLimit int

	// orgBriefSvc writes both of the company view's grounded-prose surfaces:
	// the standing account brief and the prepared "Ask Margince" questions.
	// WithAccountBrief rebinds its model lane at boot, so the api role writes
	// with a model and every other role serves the same deterministic floor.
	// (WithBrief is a different option — the Morning Brief's L2 ranker.)
	orgBriefSvc *orgbrief.Service
	// org360Svc is the composite read the brief is assembled from, held so
	// WithAccountBrief can rebuild the brief service over the SAME gated
	// read rather than a second one that might drift from it.
	org360Svc *org360.Service

	// sorDispatch is the per-workspace native/overlay provider dispatch:
	// the ONE instance both the ADR-0055 admission layer (contractAPI's
	// agentGate) and the overlay-mode human read shadows (overlayread.go)
	// ride, so a workspace's resolved x_sor_mode is cached once, not per
	// consumer. Assembled in newServer, before the options run, so
	// WithKeyvault can hand its Invalidate to overlay.Service as the
	// mode-flip observer (a connect/disconnect drops the cached mode
	// immediately in this process).
	sorDispatch *Dispatcher
}

var _ crmcontracts.ServerInterface = Server{}

// Option, readinessChecks, and every With* role-customization function live
// in serveroptions.go — the per-process-role wiring surface, kept separate
// from the struct/router assembly below.

// New wires the modules and returns the ready http.Handler: contract
// routes under /v1, health probe, session middleware, panic recovery.
func New(pool *pgxpool.Pool, log *slog.Logger, opts ...Option) http.Handler {
	// The fieldcatalog seam for deals (the peopleHandlers wiring in
	// newServer carries the full note): active cf_* deal columns ride
	// deal payloads on both surfaces.
	dealsH := deals.NewHandlers(pool).WithFieldCatalog(customfields.NewService(pool, nil))
	// Bootstrap happens at boot from deployment configuration
	// (EnsureInstallation, A107/ADR-0061) — the HTTP surface only ever
	// serves the already-bound singleton organization.
	identitySvc := identity.NewService(pool)
	authH := identity.NewHandlers(identitySvc)

	srv := newServer(pool, log, authH, dealsH)
	for _, opt := range opts {
		opt(&srv, pool)
	}
	srv.applySendPath(pool)

	api := contractAPI(srv, pool, identitySvc)
	// ONE identity.Service for the whole process: contractAPI's admission
	// gate and the connector's authenticate closure share this instance, so
	// they share its singleton cache and its clock.
	mux := operationalMux(srv, pool, log, identitySvc, api)

	return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux)))
}

// newServer assembles the module handler sets. Every cross-module edge
// is injected HERE, never as a sibling import (ADR-0054).
func newServer(pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, dealsH dealsHandlers) Server {
	srv := Server{
		authHandlers: authH,
		// The fieldcatalog seam: customfields' catalog read makes the
		// workspace's active cf_* columns ride person/organization
		// payloads (values only — the schema-change engine stays behind
		// WithSchemaPool; ActiveColumns needs none of it).
		// The match stager is injected here because approvals is a sibling of
		// people and a module never imports one: compose is where that edge is
		// made, as it is for every other cross-module dependency.
		peopleHandlers: people.NewHandlers(pool).
			WithFieldCatalog(customfields.NewService(pool, nil)).
			WithMatchStager(linkedInMatchStager(pool)),
		dealsHandlers: dealsH,
		activitiesHandlers: activities.NewHandlers(pool).
			WithConsent(consent.NewGate(consent.NewStore(pool))).
			// The public booking capture seams (feedback/14): people is the
			// idempotent-on-email person path, consent records the
			// passthrough — both injected here, never sibling imports.
			WithPublicBooking(people.NewStore(pool), bookingConsentAdapter{store: consent.NewStore(pool)}).
			// The RFC 8058 unsubscribe linker (B-E11.32): consent mints the
			// preference token behind the List-Unsubscribe URL.
			WithUnsubscribe(preferenceLinkAdapter{store: consent.NewStore(pool)}),
		approvalsHandlers: approvalsHandlersWithEffects(pool),
		searchHandlers:    search.NewHandlers(pool),
		// DSR fulfillment executes privacy's erase path — injected here so
		// consent never imports its sibling.
		consentHandlers:     consent.NewHandlers(pool).WithEraser(privacy.NewEraser(pool)),
		collectionsHandlers: collections.NewHandlers(pool),
		// The warm room ranks its contact edges by the §4 relationship
		// strength owned by people; injected through the adapter below so
		// signals never imports its sibling.
		signalsHandlers:    signals.NewHandlers(pool, signalStrength{people: people.NewStore(pool)}),
		privacyHandlers:    privacy.NewHandlers(pool),
		automationHandlers: automation.NewHandlers(pool),
		voiceHandlers:      ai.NewHandlers(pool, NewSeatBudget(pool)),
		reportHandlers:     reportHandlers{engine: newReportEngine(pool)},
		// The Morning Brief always serves on the deterministic §10.1 floor;
		// the L2 re-order is opt-in via WithBrief (the api role's model path).
		Handlers: briefs.NewHandlers(briefs.NewBriefEngine(pool, people.NewStore(pool))),
		Reads:    network.NewReads(pool),
		// The RC-2 personal-mail exclusion CRUD over the caller's own rules
		// (capture.md CAP-WIRE-2); the same store backs the ONE Sink's
		// pre-ingestion gate (wired in NewCaptureRegistry).
		captureExclusionHandlers: captureExclusionHandlers{store: capture.NewExclusions(pool)},
		// The workspace capture-settings surface (CAP-WIRE-7, ADR-0072):
		// read the auto-enrich posture (all roles), toggle it (admin/ops).
		captureSettingsHandlers: captureSettingsHandlers{store: capture.NewSettings(pool)},
		// First-class filtered export (B-E15.13): the writer reuses the ONE
		// predicate engine + the bundle writer's open-format rendering; the
		// collections store resolves a saved view / dynamic list source
		// behind its own visibility gate.
		filteredExportHandlers: filteredExportHandlers{writer: NewFilteredExportWriter(pool), collections: collections.NewStore(pool)},
		overlayExportHandlers:  newOverlayExportHandlers(pool, log),
		orgRollupHandlers:      orgRollupHandlers{pool: pool, now: time.Now},
		strengthHandlers:       strengthHandlers{people: people.NewStore(pool), now: time.Now},
		// The installation's own company (the 0083 anchor). Its own store
		// instance, like every other people-backed shadow here: the company
		// form's write shape is people's, the transport is compose's.
		companyHandlers:  companyHandlers{store: people.NewStore(pool), rollout: companyContextRolloutOnboarding},
		siteReadHandlers: siteReadHandlers{companyContextRollout: companyContextRolloutOnboarding},
		onboardingStateHandlers: onboardingStateHandlers{
			state: identity.NewOnboardingStore(pool), company: people.NewStore(pool),
			proposal: &onboardingProposalEngine{
				state: identity.NewOnboardingStore(pool), people: people.NewStore(pool),
				rollout: companyContextRolloutOnboarding,
			},
		},
		// The schema-change pool is boot-optional; nil
		// here means Create/SetOptions stay their generated 501 until the
		// api role's WithSchemaPool rebuilds this over the real pool.
		customfieldsHandlers: customfields.NewHandlers(pool, nil),
		quotasHandlers:       quotas.NewHandlers(pool),
		// The accept-write's default engine rides the honest-empty NoOp
		// extractor (nothing is ever grounded, so nothing is acceptable);
		// WithExtractor rebuilds it together with the activities read so
		// both surfaces answer from the SAME seam.
		attachmentExtractionHandlers: attachmentExtractionHandlers{accept: NewExtractionAccept(pool, nil)},
		// Outbound webhooks (E10/S-E10.6): the read surface works
		// unconditionally; create/rotate/replay need a deployment signing
		// key, wired by WithWebhookSigningKey (the api role sources it from
		// the environment). Without it those paths answer an honest 503.
		webhooksHandlers: newWebhookHandlers(pool, nil, log),
		log:              log,
		dealsStore:       deals.NewStore(pool),
		// Constructed unconditionally: WithKeyvault rebuilds
		// overlayHandlers over this SAME instance rather than minting a
		// second one, and contractAPI's Dispatcher spends force-fresh
		// reads against it too (see compose/overlay.go's NewOverlayMeter
		// doc). Fail-closed until WithOverlayMeter Rebinds it with the live
		// Redis client + config.
		overlayMeter: failClosedOverlayMeter(),
	}
	// The overlay read dispatch is built with a nil live-incumbent resolver
	// here (force-fresh degrades to the mirror). WithKeyvault injects the
	// vault-backed resolver once the vault is known — the vault arrives via
	// an option applied AFTER newServer returns, and the dispatch/provider/
	// freshness reader are pointers shared across that return, so a
	// boot-time SetOverlayIncumbentResolver reaches the same instance this
	// field serves reads through.
	srv.sorDispatch = NewDispatcher(NewProvider(pool), NewOverlayProvider(pool, srv.overlayMeter, nil), pool)
	// The company view (org360) is assembled from THIS system of record;
	// it asks the same dispatch every other overlay-aware read asks, so a
	// workspace running on the incumbent mirror gets one honest refusal
	// instead of a page that quietly omits most of itself. Wired after the
	// literal because it needs srv.sorDispatch, which is built above.
	// The people store carries the SAME fieldcatalog seam peopleHandlers
	// gets: the 360 serves the organization object, and without it the
	// company view would silently omit the cf_* columns GET
	// /organizations/{id} returns for the same record.
	// The brief reads THROUGH the 360 service, so it inherits every gate the
	// page itself applies and can only describe what this caller may see.
	// The model lane is nil here: WithAccountBrief binds the api role's
	// summarize lane, and without it the brief serves its deterministic
	// floor.
	srv.org360Svc = org360.NewService(pool,
		people.NewStore(pool).WithFieldCatalog(customfields.NewService(pool, nil)),
		approvals.NewService(pool), time.Now)
	srv.orgBriefSvc = orgbrief.NewService(pool, srv.org360Svc, nil, "", time.Now)
	srv.orgBriefHandlers = orgbrief.NewHandlers(srv.orgBriefSvc, srv.sorDispatch.isOverlay)
	srv.org360Handlers = org360.NewHandlers(
		srv.org360Svc,
		srv.sorDispatch.isOverlay,
	)
	// toolRegistry backs ListAgentTools AND the MCP tool transport; it carries
	// the vault-backed live-incumbent resolver that lets force-fresh reads and
	// HUMAN write-back reach HubSpot (an AGENT write is refused before it gets
	// there — egressbackstop.go).
	// The closure captures srv and reads srv.vault LAZILY at request time, so
	// building it here (before WithKeyvault installs the vault) is fine.
	srv.rebuildToolRegistry(pool)
	// /me reports the workspace's system-of-record mode so the client can
	// gate its list UI (an overlay mirror refuses sort/filter dials). The
	// dispatch owns mode resolution; identity never imports overlay.
	srv.authHandlers = srv.WithSorMode(srv.sorDispatch.isOverlay)
	return srv
}

// rebuildToolRegistry rebuilds the agent tool surface from the server's
// CURRENT state. Every option that changes what the registry composes over —
// the reply drafter, the send configuration — calls this rather than building
// its own registry, so applying two such options in either order lands on the
// same surface instead of the later one dropping the earlier one's wiring.
func (s *Server) rebuildToolRegistry(pool *pgxpool.Pool) {
	// The closure captures s and reads s.vault LAZILY at request time, so
	// rebuilding before WithKeyvault installs the vault is fine.
	s.toolRegistry = registryWithGate(pool, auth.NewGate(identity.NewService(pool)),
		s.replyDrafter, s.resolveOverlayIncumbent(pool), s.send)
}

// contractAPI mounts the generated contract router with the ADR-0055
// admission layer, which rides INSIDE the router (it needs the matched
// route pattern) and shares the MCP surface's tier table, approvals
// staging, and live-authority gate — one gate, two transports.
// readyzEmbedState builds /readyz's embed-status closure (Task 17) over
// whatever embed lane this process role already wired via
// WithEmbedReindex — the SAME store and embedder embedReindexHandlers'
// status/preview/confirm read, so this reports through the one seam
// rather than opening a second router/store pair. A role that never
// wires an embed lane (no declared routing config, --ai-fake, or the
// two self-gating nils WithEmbedReindex checks) leaves engine nil; that
// is a legitimate "no embed lane to report on" shape, not a fault, so it
// renders "unknown" exactly like a marker-read failure does — Readyz's
// body never distinguishes the two, only ever "was this readable right
// now or not."
// signalStrength bridges people's §4 relationship-strength computation to
// the slice the warm room consumes (signals.StrengthSource). It carries
// only the score and its bucket across the seam — the full explainable
// decomposition stays with its owner. This is the arch-legal edge: signals
// declares its own seam type, and the cross-module dependency lives here in
// compose, never as a signals→people import.
type signalStrength struct{ people *people.Store }

func (s signalStrength) PersonStrength(ctx context.Context, personID ids.PersonID, now time.Time) (signals.RelationshipStrength, error) {
	rs, err := s.people.PersonStrength(ctx, personID, now)
	if err != nil {
		return signals.RelationshipStrength{}, err
	}
	return signals.RelationshipStrength{Strength: rs.Strength, Bucket: rs.Bucket}, nil
}

// paramParseError maps a generated request-parameter parse failure onto
// the same 422 validation_error shape every other bad query input uses
// (mirrors httperr's malformed-cursor path). It names only the offending
// parameter — never the wrapped parser text, which can carry internal
// detail — so a bad cursor/limit/sort/UUID answers problem+json, not a
// text/plain leak.
func paramParseError(w http.ResponseWriter, r *http.Request, err error) {
	param := "request"
	switch e := err.(type) {
	case *crmcontracts.RequiredParamError:
		param = e.ParamName
	case *crmcontracts.InvalidParamFormatError:
		param = e.ParamName
	case *crmcontracts.TooManyValuesForParamError:
		param = e.ParamName
	case *crmcontracts.UnmarshalingParamError:
		param = e.ParamName
	case *crmcontracts.UnescapedCookieParamError:
		param = e.ParamName
	}
	httperr.Write(w, r, httperr.Validation(param, "invalid_parameter",
		"parameter is missing or malformed"))
}
