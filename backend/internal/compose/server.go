package compose

// The contract HTTP surface: module transport handlers (identity,
// people, deals, activities, approvals) shadow the generated-interface
// stubs by embedding depth, so every one of the contract's operations
// either runs real module code or answers an explicit 501 — never a
// silent 404. The chassis (headers, correlation, panic recovery) is
// platform/httpserver; what lives here is the wiring.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
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
	reportHandlers
	coldstartHandlers
}

var _ crmcontracts.ServerInterface = Server{}

// Option customizes the wiring for one process role; everything not
// optioned keeps its safe default.
type Option func(*Server, *pgxpool.Pool)

// WithColdStart enables the cold-start read-back over the given fetch
// and model seams. Without it the operation stays an explicit 501 —
// the api role must DECLARE its model path, never pick one silently.
func WithColdStart(fetch PageFetcher, brain runner.Brain) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.coldstartHandlers = coldstartHandlers{engine: &coldStartEngine{
			fetch:     fetch,
			brain:     brain,
			approvals: approvals.NewService(pool),
		}}
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
		return consent.SeedDefaultPurposesTx(ctx, tx)
	}
	authH := identity.NewHandlers(identitySvc, seedDefaults)

	srv := Server{
		authHandlers:        authH,
		peopleHandlers:      people.NewHandlers(pool),
		dealsHandlers:       dealsH,
		activitiesHandlers:  activities.NewHandlers(pool).WithConsent(consent.NewGate(consent.NewStore(pool))),
		approvalsHandlers:   approvals.NewHandlers(approvals.NewService(pool)),
		searchHandlers:      search.NewHandlers(pool),
		consentHandlers:     consent.NewHandlers(pool),
		collectionsHandlers: collections.NewHandlers(pool),
		reportHandlers:      reportHandlers{engine: newReportEngine(pool)},
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
	api := crmcontracts.HandlerWithOptions(srv, crmcontracts.ChiServerOptions{
		BaseURL:     "/v1",
		Middlewares: []crmcontracts.MiddlewareFunc{agentGate(registry, staging, provider, gate)},
	})

	// Only /v1 rides the session middleware; the embedded SPA and the
	// health probe are static and unauthenticated (the SPA's every data
	// access goes back through /v1 — it holds no privileged path).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.Handle("/v1/", httpserver.Correlate(authH.Middleware(api)))
	// The A2 authorization server (ADR-0013): AS endpoints live outside
	// the generated resource surface but behind the same workspace and
	// session middleware; the discovery documents are static.
	mux.Handle("/oauth/", httpserver.Correlate(authH.Middleware(authH.OAuthRouter())))
	mux.HandleFunc("/.well-known/oauth-authorization-server", identity.OAuthServerMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", identity.ProtectedResourceMetadata)
	mux.Handle("/", web.Handler())

	return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux)))
}
