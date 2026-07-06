// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The contract HTTP surface: module transport handlers shadow the
// generated-interface stubs by embedding depth (the Server struct below
// is the inventory), so every one of the contract's operations
// either runs real module code or answers an explicit 501 — never a
// silent 404. The chassis (headers, correlation, panic recovery) is
// platform/httpserver; what lives here is the wiring.

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/web"
)

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers        = identity.Handlers
	peopleHandlers      = people.Handlers
	dealsHandlers       = deals.Handlers
	activitiesHandlers  = activities.Handlers
	approvalsHandlers   = approvals.Handlers
	searchHandlers      = search.Handlers
	consentHandlers     = consent.Handlers
	collectionsHandlers = collections.Handlers
	signalsHandlers     = signals.Handlers
	privacyHandlers     = privacy.Handlers
	agentsHandlers      = agents.Handlers
	voiceHandlers       = ai.Handlers
)

// Server satisfies crmcontracts.ServerInterface by embedding the module
// transport handler sets. Every contract operation is implemented; the
// generated stubs (stubs_gen.go) stay as the drift gate's inventory and
// would resurface as a compile error here if a regenerated contract
// added an operation nothing implements.
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
	agentsHandlers
	voiceHandlers
	reportHandlers
	briefHandlers
	coldstartHandlers
	scrapeHandlers
	imapConnectHandlers
	filteredExportHandlers

	// busReady is the /readyz bus probe, injected only by the process
	// role that runs the inline relay — a split deployment's api answers
	// ready on Postgres alone.
	busReady func(context.Context) error

	// log is the process logger, shared with the optional engines an
	// option wires (e.g. the brief L2 ranker's degradation warnings).
	log *slog.Logger
}

var _ crmcontracts.ServerInterface = Server{}

// Option customizes the wiring for one process role; everything not
// optioned keeps its safe default.
type Option func(*Server, *pgxpool.Pool)

// WithBusReady adds the event-bus probe to /readyz. The api role passes
// it when it runs the inline relay: a process that must ship events is
// not ready while the bus is unreachable.
func WithBusReady(check func(context.Context) error) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.busReady = check }
}

// WithPublicBaseURL sets the canonical scheme+host the buyer-facing
// unsubscribe/preference links resolve to (B-E11.32). It is configured at
// boot, never derived from a request: the link carries the recipient's
// unsubscribe token. Without it a marketing send refuses rather than emit
// a forgeable link.
func WithPublicBaseURL(base string) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.activitiesHandlers = s.WithPublicBaseURL(base)
	}
}

// WithColdStart enables the cold-start read-back over the given fetch
// and model seams. Without it the operation stays an explicit 501 —
// the api role must DECLARE its model path, never pick one silently.
func WithColdStart(fetch PageFetcher, brain runner.Brain) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.coldstartHandlers = coldstartHandlers{engine: &coldStartEngine{
			extract:   evidenceExtractor{fetch: fetch, brain: brain},
			approvals: approvals.NewService(pool),
		}}
	}
}

// WithScrape enables per-organization enrichment (scrapeCompany) over the same
// fetch and model seams as the read-back. Without it the operation stays an
// explicit 501 — the api role must DECLARE its model path, never pick one
// silently.
func WithScrape(fetch PageFetcher, brain runner.Brain) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.scrapeHandlers = scrapeHandlers{engine: &scrapeEngine{
			extract:   evidenceExtractor{fetch: fetch, brain: brain},
			people:    people.NewStore(pool),
			approvals: approvals.NewService(pool),
		}}
	}
}

// WithBrief enables the Morning-Brief L2 ranker (B-E05.2) over the given
// model lane. Without it the brief still serves fully on the deterministic
// §10.1 composite — the L2 layer is advisory over that floor, never a
// prerequisite for the home surface.
func WithBrief(brain runner.Brain) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.briefHandlers.engine.WithL2Ranker(brain, s.log)
	}
}

// New wires the modules and returns the ready http.Handler: contract
// routes under /v1, health probe, session middleware, panic recovery.
func New(pool *pgxpool.Pool, log *slog.Logger, opts ...Option) http.Handler {
	dealsH := deals.NewHandlers(pool)
	// On workspace bootstrap, deals seeds its per-workspace defaults
	// (the default pipeline) — composed here so neither module imports
	// the other.
	identitySvc := identity.NewService(pool)
	// Workspace bootstrap seeds every module's per-workspace defaults in
	// ONE transaction (C5): the default pipeline and the consent purpose
	// catalog stand or fall together.
	seedDefaults := func(ctx context.Context, tx pgx.Tx) error {
		if err := dealsH.SeedWorkspaceDefaultsTx(ctx, tx); err != nil {
			return err
		}
		if err := consent.SeedDefaultPurposesTx(ctx, tx); err != nil {
			return err
		}
		if err := consent.SeedDefaultRetentionTx(ctx, tx); err != nil {
			return err
		}
		if err := agents.SeedStarterAutomationsTx(ctx, tx); err != nil {
			return err
		}
		// The admin's public booking page: the workspace's only user at
		// seed time IS the bootstrap admin (RLS scopes the read).
		var adminID ids.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM app_user ORDER BY created_at LIMIT 1`).Scan(&adminID); err != nil {
			return err
		}
		_, err := activities.SeedBookingPageTx(ctx, tx, adminID)
		return err
	}
	authH := identity.NewHandlers(identitySvc, seedDefaults)

	srv := Server{
		authHandlers:   authH,
		peopleHandlers: people.NewHandlers(pool),
		dealsHandlers:  dealsH,
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
		signalsHandlers: signals.NewHandlers(pool, signalStrength{people: people.NewStore(pool)}),
		privacyHandlers: privacy.NewHandlers(pool),
		agentsHandlers:  agents.NewHandlers(pool),
		voiceHandlers:   ai.NewHandlers(pool),
		reportHandlers:  reportHandlers{engine: newReportEngine(pool)},
		// The Morning Brief always serves on the deterministic §10.1 floor;
		// the L2 re-order is opt-in via WithBrief (the api role's model path).
		briefHandlers: briefHandlers{engine: NewBriefEngine(pool, people.NewStore(pool))},
		// The one-shot IMAP pull shares the capture registry (Sink + the
		// live-authority principal swap); credentials arrive per request and
		// are never persisted, so no standing connection is registered.
		imapConnectHandlers: imapConnectHandlers{registry: NewCaptureRegistry(pool)},
		// First-class filtered export (B-E15.13): the writer reuses the ONE
		// predicate engine + the bundle writer's open-format rendering; the
		// collections store resolves a saved view / dynamic list source
		// behind its own visibility gate.
		filteredExportHandlers: filteredExportHandlers{
			writer:      NewFilteredExportWriter(pool),
			collections: collections.NewStore(pool),
		},
		log: log,
	}
	for _, opt := range opts {
		opt(&srv, pool)
	}

	// The ADR-0055 admission layer rides INSIDE the router (it needs the
	// matched route pattern) and shares the MCP surface's tier table,
	// approvals staging, and live-authority gate — one gate, two
	// transports.
	gate := auth.NewGate(identitySvc)
	registry := newRegistry(pool, gate)
	provider := NewProvider(pool)
	staging := approvalsAdapter{svc: approvals.NewService(pool)}
	// Wrap order: the generated router applies the slice left-to-right
	// around the handler, so the LAST entry is outermost — idempotency
	// must sit outside the agent gate so a staged-approval refusal is
	// never recorded as "the" response for a key (the approved retry is
	// the same request under the same key).
	api := crmcontracts.HandlerWithOptions(srv, crmcontracts.ChiServerOptions{
		BaseURL: "/v1",
		Middlewares: []crmcontracts.MiddlewareFunc{
			agentGate(registry, staging, provider, fieldOwnership{pool: pool}, gate),
			idempotency(pool),
		},
		// Keep query/path/header parse failures on the problem+json path:
		// the generated default writes err.Error() as text/plain, an
		// off-contract shape that also leaks the parser's internal text.
		ErrorHandlerFunc: paramParseError,
	})

	// Only /v1 rides the session middleware; the embedded SPA and the
	// health probe are static and unauthenticated (the SPA's every data
	// access goes back through /v1 — it holds no privileged path).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	readiness := []httpserver.ReadyCheck{{Name: "postgres", Check: pool.Ping}}
	if srv.busReady != nil {
		readiness = append(readiness, httpserver.ReadyCheck{Name: "redis", Check: srv.busReady})
	}
	mux.HandleFunc("/readyz", httpserver.Readyz(readiness...))
	mux.HandleFunc("/metrics", httpserver.Metrics(pool,
		func(ctx context.Context) (int64, error) { return events.OutboxBacklog(ctx, pool) },
		events.PublishedTotal))
	// The anonymous public edges sit between the session middleware (which
	// lets /v1/public/ through without session or workspace) and the
	// router: each resolves its own token/slug → tenant, throttles, and
	// binds a confined system principal. The preference edge wraps the
	// booking edge — each passes a non-matching path straight through.
	publicEdge := publicPreferences(consent.NewStore(pool), newPublicPreferenceLimiters())(
		publicBooking(activities.NewStore(pool), newPublicBookingLimiters())(api))
	mux.Handle("/v1/", httpserver.Correlate(httpserver.AccessLog(log, authH.Middleware(publicEdge))))
	// The A2 authorization server (ADR-0013): AS endpoints live outside
	// the generated resource surface but behind the same workspace and
	// session middleware; the discovery documents are static.
	mux.Handle("/oauth/", httpserver.Correlate(httpserver.AccessLog(log, authH.Middleware(authH.OAuthRouter()))))
	mux.HandleFunc("/.well-known/oauth-authorization-server", identity.OAuthServerMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", identity.ProtectedResourceMetadata)
	mux.Handle("/", web.Handler())

	return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux)))
}

// signalStrength bridges people's §4 relationship-strength computation to
// the slice the warm room consumes (signals.StrengthSource). It carries
// only the score and its bucket across the seam — the full explainable
// decomposition stays with its owner. This is the arch-legal edge: signals
// declares its own seam type, and the cross-module dependency lives here in
// compose, never as a signals→people import.
type signalStrength struct{ people *people.Store }

func (s signalStrength) PersonStrength(ctx context.Context, personID ids.UUID, now time.Time) (signals.RelationshipStrength, error) {
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
