package compose

// The contract HTTP surface: module transport handlers (identity,
// people, deals, activities, approvals) shadow the generated-interface
// stubs by embedding depth, so every one of the contract's operations
// either runs real module code or answers an explicit 501 — never a
// silent 404. The chassis (headers, correlation, panic recovery) is
// platform/httpserver; what lives here is the wiring.

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/web"
)

// fallback pushes the stubs one embedding level deeper than the module
// handlers, so a module method always wins promotion and the stub only
// answers operations nothing implements.
type fallback struct{ stubs }

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers       = identity.Handlers
	peopleHandlers     = people.Handlers
	dealsHandlers      = deals.Handlers
	activitiesHandlers = activities.Handlers
	approvalsHandlers  = approvals.Handlers
)

// Server satisfies crmcontracts.ServerInterface by embedding: the module
// transport handlers at depth one shadow the depth-two stubs.
type Server struct {
	authHandlers
	peopleHandlers
	dealsHandlers
	activitiesHandlers
	approvalsHandlers
	fallback
}

var _ crmcontracts.ServerInterface = Server{}

// New wires the modules and returns the ready http.Handler: contract
// routes under /v1, health probe, session middleware, panic recovery.
func New(pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	dealsH := deals.NewHandlers(pool)
	// On workspace bootstrap, deals seeds its per-workspace defaults
	// (the default pipeline) — composed here so neither module imports
	// the other.
	auth := identity.NewHandlers(identity.NewService(pool), dealsH.SeedWorkspaceDefaultsTx)

	srv := Server{
		authHandlers:       auth,
		peopleHandlers:     people.NewHandlers(pool),
		dealsHandlers:      dealsH,
		activitiesHandlers: activities.NewHandlers(pool),
		approvalsHandlers:  approvals.NewHandlers(approvals.NewService(pool)),
	}

	api := crmcontracts.HandlerWithOptions(srv, crmcontracts.ChiServerOptions{
		BaseURL: "/v1",
	})

	// Only /v1 rides the session middleware; the embedded SPA and the
	// health probe are static and unauthenticated (the SPA's every data
	// access goes back through /v1 — it holds no privileged path).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.Handle("/v1/", httpserver.Correlate(auth.Middleware(api)))
	mux.Handle("/", web.Handler())

	return httpserver.RecoverPanics(log, httpserver.SecureHeaders(mux))
}
