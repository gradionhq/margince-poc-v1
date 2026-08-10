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
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
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
	captureSettingsHandlers
	ownDomainHandlers
	installationSettingsHandlers
	consumerMailDomainHandlers
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
	dataResetHandlers
	jobHealthHandlers
	org360Handlers
	person360Handlers
	orgBriefHandlers
	orgDossierHandlers
	accountDraftHandlers
	financeHandlers

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
	// appViews holds the MCP App documents this api is serving. Nil for the
	// worker and for an api that composed no views — see mcpappviews.go.
	appViews *apps.Provider

	// mcpAllowedOrigin is the scheme+host the connector's Origin guard
	// admits — derived by WithMCPResource from the configured
	// --public-base-url, never from a request header a caller controls.
	mcpAllowedOrigin string

	// metricsToken gates /metrics, injected by WithMetricsToken from the
	// deployment's --metrics-token. Unlike /healthz and /readyz it discloses
	// per-workspace job-runtime telemetry (queue depth, which connectors are
	// configured), so it stays off — routes.go answers 404 rather than
	// serving it — until an operator opts in by setting one.
	metricsToken string

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
	// quotaMeter is the MCP-SESS-READS bound this role enforces on agent
	// the five MCP-SESS-* counters, shared by everything that must agree about
	// them: the admission gate that REFUSES on them, both doors' registries that
	// CHARGE them, the approvals service that WIDENS one when a lender says
	// continue, and the model path that charges the soft cost share.
	//
	// Always non-nil (newServer constructs it unconditionally, fail-closed
	// with no Redis), and WithAgentQuota Rebinds this ONE pointer to the live
	// Redis-backed meter at boot — so no option order can leave the gate
	// enforcing a different counter from the one the registry pays into.
	quotaMeter *agentquota.Meter
	// retrievalEmbedder is this role's embed lane for REQUEST-TIME ranking —
	// the same ModelPath.Embedder the background reindex and drift sweep use,
	// bound here so the hybrid arm's vector half is available to a caller and
	// not only to a job (#629). Nil in a role that resolved no model path, and
	// nil is honest rather than broken: every surface that ranks says which
	// lane ranked it.
	retrievalEmbedder search.Embedder
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
	// peopleStore is shared by the 360 and the account brief: the brief reads
	// the company's curated profile through it, under the caller's own gates.
	peopleStore *people.Store

	// orgDossierSvc and orgGrowthFitSvc are the company view's other two
	// generated surfaces. They are held for WithGrowthFit's sake: rebinding one
	// lane must not silently drop the other's handler, which is what building a
	// fresh handler set from a half-remembered pair would do.
	orgDossierSvc   *orgdossier.Service
	orgGrowthFitSvc *orgdossier.GrowthFitService

	// resetRuntime is the non-Postgres purge set POST /admin/reset-data runs —
	// the job queue, the event bus, the cache-flush announcement — injected by
	// WithResetRuntime. Zero value = a Postgres-only reset, which is the honest
	// posture for a role that wired no queue and no bus.
	//
	// dataResetHandlers holds a POINTER to this field rather than a copy:
	// options run in the order the caller passed them, so a copy taken by
	// WithDataReset would be the zero value whenever WithResetRuntime is listed
	// after it — silently reducing a full wipe to a table sweep, with nothing
	// failing to say so.
	resetRuntime ResetRuntime

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
	// The fieldcatalog seam for deals (newPeopleHandlers carries the full
	// note): active cf_* deal columns ride deal payloads on both surfaces.
	dealsH := deals.NewHandlers(pool, InstallationBaseCurrency()).WithFieldCatalog(customfields.NewService(pool, nil))
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

	return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux)))
}

// newServer assembles the module handler sets. Every cross-module edge is
// injected here, or in the assembly step this calls for it
// (serverassembly.go) — never as a sibling import (ADR-0054).
func newServer(pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, dealsH dealsHandlers) Server {
	srv := Server{
		authHandlers:       authH,
		peopleHandlers:     newPeopleHandlers(pool),
		dealsHandlers:      dealsH,
		activitiesHandlers: newActivitiesHandlers(pool),
		searchHandlers:     search.NewHandlers(pool),
		// Constructed, not merely embedded: the handler carries no nil-pool
		// branch, so the zero value would panic on the first authenticated
		// read rather than answer anything at all.
		jobHealthHandlers: jobHealthHandlers{pool: pool},
		// DSR fulfillment executes privacy's erase path — injected here so
		// consent never imports its sibling.
		consentHandlers:     consent.NewHandlers(pool).WithEraser(privacy.NewEraser(pool)),
		collectionsHandlers: collections.NewHandlers(pool),
		// The warm room ranks its contact edges by the §4 relationship
		// strength owned by people; injected through the adapter below so
		// signals never imports its sibling.
		financeHandlers:    finance.NewHandlers(pool, InstallationBaseCurrency()),
		signalsHandlers:    signals.NewHandlers(pool, signalStrength{people: people.NewStore(pool)}),
		privacyHandlers:    privacy.NewHandlers(pool),
		automationHandlers: automation.NewHandlers(pool),
		voiceHandlers:      ai.NewHandlers(pool, NewSeatBudget(pool)),
		reportHandlers:     reportHandlers{engine: newReportEngine(pool)},
		// The Morning Brief always serves on the deterministic §10.1 floor;
		// the L2 re-order is opt-in via WithBrief (the api role's model path).
		Handlers:          briefs.NewHandlers(briefs.NewBriefEngine(pool, people.NewStore(pool))),
		Reads:             network.NewReads(pool),
		orgRollupHandlers: orgRollupHandlers{pool: pool, now: time.Now},
		strengthHandlers:  strengthHandlers{people: people.NewStore(pool), now: time.Now},
		// The schema-change pool is boot-optional; nil
		// here means Create/SetOptions stay their generated 501 until the
		// api role's WithSchemaPool rebuilds this over the real pool.
		customfieldsHandlers: customfields.NewHandlers(pool, nil),
		quotasHandlers:       quotas.NewHandlers(pool, InstallationBaseCurrency()),
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
		dealsStore:       deals.NewStore(pool, InstallationBaseCurrency()),
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

// rebuildToolRegistry rebuilds the agent tool surface from the server's
// CURRENT state. Every option that changes what the registry composes over —
// the reply drafter, the send configuration — calls this rather than building
// its own registry, so applying two such options in either order lands on the
// same surface instead of the later one dropping the earlier one's wiring.
func (s *Server) rebuildToolRegistry(pool *pgxpool.Pool) {
	// The closure captures s and reads s.vault LAZILY at request time, so
	// rebuilding before WithKeyvault installs the vault is fine.
	// The gate and the registry take the SAME meter pointer: one refuses on the
	// bound, the other pays into it, and a surface where those were two
	// counters would step an agent up against a number nothing was charging.
	s.toolRegistry = registryWithGate(pool,
		auth.NewGate(identity.NewService(pool), auth.WithQuota(s.quotaMeter)),
		s.replyDrafter, s.resolveOverlayIncumbent(pool), s.send, companyEnricher{srv: s},
		s.retrievalEmbedder,
		agents.WithQuotaCharger(s.quotaMeter), agents.WithCostShare(s.quotaMeter))
}

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
