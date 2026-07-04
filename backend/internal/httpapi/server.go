// Package httpapi assembles the contract surface: module transport
// handlers (identity, people, deals, activities, approvals) shadow the
// generated-interface stubs by embedding depth, so every one of the
// contract's operations either runs real module code or answers an
// explicit 501 — never a silent 404.
package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/v1/", correlate(auth.Middleware(api)))
	mux.Handle("/", web.Handler())

	return recoverPanics(log, secureHeaders(mux))
}

// secureHeaders sets the browser-facing response headers on everything —
// UI and API alike. SameSite=Strict on the session cookie covers CSRF;
// these close what it does not: framing (clickjacking), MIME sniffing,
// and referrer leakage. The CSP pins scripts to the embedded SPA; the
// fonts.g* entries exist only because index.html loads the design
// language's typefaces from Google Fonts.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// correlate opens the per-request trace scope: one freshly minted
// correlation_id groups every event the request's writes emit (events.md
// §2). Minted server-side, never taken from a request header — a client
// that could set it could stitch itself into another tenant's story.
func correlate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := principal.WithCorrelationID(r.Context(), ids.NewV7())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverPanics is the outermost guard: a panicking handler answers an
// opaque 500 instead of killing the connection (and taking pre-Go-1.21
// servers down with it). The panic value and stack are logged — the one
// place observability matters most must never be a silent 500.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(r.Context(), "handler panic",
					"panic", rec, "method", r.Method, "path", r.URL.Path,
					"stack", string(debug.Stack()))
				httperr.Write(w, r, &httperr.DetailedError{
					Status: http.StatusInternalServerError, Code: "internal",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
