// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP mux assembly: contractAPI builds the generated /v1 surface with
// its admission/idempotency/overlay-guard middleware stack, and
// operationalMux mounts that surface next to the operational edges (health
// probes, metrics, the anonymous public paths, the gated remote MCP
// connector, and the provider push webhooks). server.go owns the Server
// inventory and its wiring options; this file owns how those handlers
// become one mux — including the one client-IP rule every throttled edge
// mounted here keys on.

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// contractAPI builds the generated contract router with the ADR-0055
// admission layer, idempotency, and the overlay-mode write guard wrapped
// around it (outermost last — see the wrap-order note inline).
func contractAPI(srv Server, pool *pgxpool.Pool, identitySvc *identity.Service) http.Handler {
	// The SAME meter the tool registry charges: this door refuses on the bound
	// the other door pays into, so a Passport cannot spend its window on one
	// and keep reading on the other.
	gate := auth.NewGate(identitySvc, auth.WithReadBound(srv.readMeter))
	// This registry admits REST calls; it never INVOKES a tool — a REST enrich
	// runs scrapeHandlers, not the tool — so the enricher here supplies only the
	// spec's cap and tier, and the ZERO value supplies those. Deliberately not
	// the address of this by-value parameter: that would be a second reference
	// to a pre-option copy, the exact shape the wrap-order note below warns
	// about, and it would read as if something ran through it. The MCP
	// transport invokes tools through srv.toolRegistry, which holds the live
	// server.
	registry := registryWithGate(pool, gate, srv.replyDrafter, srv.resolveOverlayIncumbent(pool), srv.send, companyEnricher{}, srv.retrievalEmbedder)
	// The ADR-0055 admission layer and the MCP tool surface share one
	// provider seam: agentGate's StageResolver dispatches per workspace
	// exactly like the MCP registry's tools do — and the overlay-mode
	// human read shadows (overlayread.go) ride this same instance.
	provider := srv.sorDispatch
	staging := approvalsAdapter{svc: approvals.NewService(pool)}
	// Wrap order: the generated router applies the slice left-to-right
	// around the handler, so the LAST entry is outermost — idempotency
	// must sit outside the agent gate so a staged-approval refusal is
	// never recorded as "the" response for a key (the approved retry is
	// the same request under the same key).
	api := crmcontracts.HandlerWithOptions(srv, crmcontracts.ChiServerOptions{
		BaseURL: "/v1",
		Middlewares: []crmcontracts.MiddlewareFunc{
			agentGate(registry, staging, provider, provider, fieldOwnership{pool: pool}, gate),
			idempotency(pool, replayProbes(staging.svc)),
			// Outermost: an overlay-mode SoR write is refused before it can
			// be recorded under an idempotency key or staged as an agent
			// approval — the honest unsupported_by_sor, for every principal.
			overlayWriteGuard(srv.sorDispatch),
		},
		// Keep query/path/header parse failures on the problem+json path:
		// the generated default writes err.Error() as text/plain, an
		// off-contract shape that also leaks the parser's internal text.
		ErrorHandlerFunc: paramParseError,
	})
	return api
}

// replayProbes wires the module-owned visibility rules the replay gate borrows
// (API-CC-8). Named rather than inline so a test can assert it covers every
// moduleProbe replayableOperations names: an unwired key fails closed, which
// retires the replay promise for that route silently instead of loudly.
func replayProbes(approvalsSvc *approvals.Service) map[string]replayProbe {
	return map[string]replayProbe{
		// A replay of an approval decision is a read of that approval, so it
		// clears the same visibility rule the inbox does, not a copy of it.
		probeApproval: func(ctx context.Context, id ids.UUID) error {
			_, err := approvalsSvc.Get(ctx, ids.From[ids.ApprovalKind](id))
			return err
		},
	}
}

// operationalMux mounts the contract surface next to the operational
// edges: health probes, metrics, the anonymous public paths, and — when the
// deployment declares it — the remote MCP connector.
func operationalMux(srv Server, pool *pgxpool.Pool, log *slog.Logger, identitySvc *identity.Service, api http.Handler) *http.ServeMux {
	// The identity handler set is read off the assembled Server, never taken
	// as a separate parameter: identity.Handlers is a value type whose With*
	// options return a copy, so a second reference to the pre-option set
	// would serve the AS and the discovery documents from stale config while
	// /v1 served the configured one — silently, since both compile.
	authH := srv.authHandlers
	// The session middleware (authH.Middleware) fronts BOTH /v1 and the
	// /oauth/ authorization server; the health probes and discovery
	// documents are unauthenticated by design, and the provider push
	// webhooks verify themselves. /metrics is different: it discloses
	// per-workspace job telemetry (which connectors and GDPR engines this
	// installation runs, queue depth, connection counts), so it is gated
	// behind requireMetricsToken rather than left open beside them.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	// What is NOT checked here, said at the line where it would go: whether the
	// composed units' MIGRATIONS were applied. A composed binary against a
	// not-yet-migrated database becomes ready and publishes routes and jobs that
	// fail with undefined-table errors — the ordinary rolling-deploy window.
	// AssertRuntimeRole beside it is the pattern such a check would follow; the
	// reason it is not here is that the runtime role holds no grant on the
	// schema_migrations_ext_* tables, so adding the check means widening what
	// margince_app may read. Tracked as issue #658.
	mux.HandleFunc("/readyz", httpserver.Readyz(srv.aiStateOrDefault(), srv.readyzEmbedState(), srv.readinessChecks(pool.Ping,
		func(ctx context.Context) error { return AssertRuntimeRole(ctx, pool) })...))
	mux.HandleFunc("/metrics", requireMetricsToken(srv.metricsToken, httpserver.Metrics(pool,
		func(ctx context.Context) (int64, error) { return events.OutboxBacklog(ctx, pool) },
		events.PublishedTotal,
		srv.writeAIMetrics,
		jobMetricsSection(func(ctx context.Context) (jobs.Snapshot, error) { return jobs.Stats(ctx, pool) }),
		overlayMetricsSection(srv, pool))))
	// The anonymous public edges sit between the session middleware (which
	// lets /v1/public/ through without session or workspace) and the
	// router: each resolves its own token/slug → tenant, throttles, and
	// binds a confined system principal. The preference edge wraps the
	// booking edge — each passes a non-matching path straight through.
	// The composed extension routes sit INSIDE this chain, wrapping the
	// generated router: an extension operation is an authenticated,
	// workspace-scoped call like any other /v1 route, and mounting it on the
	// operational mux instead would win the longest-pattern match against
	// "/v1/" and serve without a session. See extensionEdge.
	publicEdge := publicPreferences(consent.NewStore(pool), newPublicPreferenceLimiters())(
		publicBooking(activities.NewStore(pool), newPublicBookingLimiters())(
			extensionEdge(srv, log)(api),
		),
	)
	// publicPreferencesPrefix is named here twice on purpose: it is where
	// the edge reads the capability token OUT of the path, and where the
	// access log must not write it back.
	//
	// publicBookingPrefix is deliberately NOT redacted. Its slug is an
	// unguessable but PUBLIC identifier — the host hands the URL out, it
	// resolves to free/busy slots and nothing else, and 0036_booking_page
	// states the posture outright ("a public identifier, not a credential:
	// stored plaintext"). Redacting it would cost the access log the one
	// thing that identifies which booking page was hit, and buy nothing.
	mux.Handle("/v1/", httpserver.Correlate(
		httpserver.AccessLog(log, authH.Middleware(publicEdge), publicPreferencesPrefix)))
	// The remote MCP connector, mounted as ONE group behind the deployment
	// gate: the A2 transport, the A2 authorization server (ADR-0013) and
	// both discovery documents. They belong together because RFC 9728
	// discovery is a chain rooted at the resource server's 401 — a client
	// that reaches /mcp must reach the metadata and the token endpoint on
	// this same origin — and because gating only the transport would leave
	// unauthenticated client registration and a passport-minting token
	// endpoint live with no way to switch them off. A nil handler IS the
	// gate signal: with the connector off none of these routes is
	// registered, so the mux's own 404 answers all of them identically and
	// nothing tells a prober which gate is closed.
	if mcp := srv.mcpHandler(pool, identitySvc, log); mcp != nil {
		// ONE set of limiters for the whole group: the transport and the
		// authorization server are two halves of one internet-facing surface,
		// and a second construction would give each its own private ceilings.
		mcpLimits := newMCPLimiters()
		mux.Handle("/mcp", httpserver.Correlate(httpserver.AccessLog(log, mcpEdge(mcp, mcpLimits, srv.mcpAllowedOrigin))))
		// The AS endpoints live outside the generated resource surface but
		// behind the same workspace and session middleware (whose one exemption
		// is the consent GET — a human arriving without a session gets a screen
		// they can sign in on, not a JSON 401; the consent DECISION still demands
		// one); the discovery documents are static. The limits wrap that
		// middleware rather than sitting inside it, so a refused request costs no
		// session read.
		mux.Handle("/oauth/", httpserver.Correlate(httpserver.AccessLog(log, oauthEdge(authH.Middleware(authH.OAuthRouter()), mcpLimits))))
		mux.HandleFunc("/.well-known/oauth-authorization-server", authH.OAuthServerMetadata)
		mux.HandleFunc("/.well-known/oauth-protected-resource", authH.ProtectedResourceMetadata)
		// Claude probes the path-suffixed form FIRST when a 401 carries no
		// resource_metadata pointer, so serve it too rather than relying on
		// the pointer alone.
		mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", authH.ProtectedResourceMetadata)
	}
	mountProviderPushWebhooks(mux, srv, log)
	return mux
}

// mountProviderPushWebhooks mounts the provider push receivers:
// unauthenticated by nature (the provider is the caller), each verified by
// its own mechanism inside the handler; mounted only when configured — the
// route is absent otherwise.
func mountProviderPushWebhooks(mux *http.ServeMux, srv Server, log *slog.Logger) {
	if srv.gmailPush != nil {
		mux.Handle("/webhooks/gmail", httpserver.Correlate(httpserver.AccessLog(log, srv.gmailPush)))
	}
	if srv.overlayWebhook != nil {
		mux.Handle("/webhooks/hubspot", httpserver.Correlate(httpserver.AccessLog(log, srv.overlayWebhook)))
	}
}

// requireMetricsToken gates the metrics exposition behind a deployment-
// configured shared secret. An empty token means the operator never opted
// in: the route answers the mux's own 404 rather than 401, so an anonymous
// probe learns nothing (no route there) instead of learning that a
// protected one exists. A configured token is checked as a bearer
// credential in constant time, the same comparison the connector-state CSRF
// nonce uses (connectors_csrf.go), so a scrape's authorization header
// cannot be timed byte-by-byte against the configured value. The credential
// is read through httpserver.BearerToken — the one reading of an
// Authorization header this process uses everywhere else — rather than a
// second parse that could drift from it and accept or refuse a scheme
// spelling the rest of the surface disagrees on.
func requireMetricsToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.NotFound(w, r)
			return
		}
		presented := httpserver.BearerToken(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			httperr.Unauthorized(w, r, "invalid or missing metrics token")
			return
		}
		next(w, r)
	}
}

// WithMetricsToken sets the shared secret /metrics requires. Called
// unconditionally at boot (an empty string is the safe default: the
// endpoint stays off, matching the worker role's own --observe-addr
// posture of "no listener at all" until an operator asks for one).
func WithMetricsToken(token string) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.metricsToken = token
	}
}

// extensionEdge builds the composed extension router and returns it as an edge
// around the generated /v1 surface: a request matching a declared extension
// route is served by that router, and everything else falls straight through.
//
// The fall-through is a "/" pattern on the extension mux, not a lookup-then-
// dispatch: ServeMux already resolves longest-pattern-wins, so registering next
// as the catch-all makes it decide, and a method mismatch on a declared route
// still produces its own 405 instead of leaking into the core router as a 404.
//
// A boot with no declared operations (the vanilla tree) returns next
// unchanged — no mux, no allocation, and no route that could shadow a core one.
// A registry that is not wired yet returns next too, because a route that
// answered "no registry" would be worse than a 404: it would tell a client the
// operation exists and is broken, when what is true is that this ROLE does not
// serve it.
func extensionEdge(srv Server, log *slog.Logger) func(http.Handler) http.Handler {
	verbs := ComposedVerbs()
	if len(verbs) == 0 || srv.toolRegistry == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		mux := http.NewServeMux()
		mux.Handle("/", next)
		// composedServedVerbs is this boot's SERVED set, keyed by (unit, tool) —
		// the verbs a unit shipped a Handle for, attributed to the unit that
		// shipped them. A declared verb outside it is mounted and answers 501
		// rather than reaching a registry that never heard of it, or worse
		// reaching another unit's handler; see MountExtensionRoutes.
		routes, err := MountExtensionRoutes(mux, verbs, composedServedVerbs(), srv.toolRegistry.Invoke)
		if err != nil {
			// A composed set that reached here invalid means RegisterExtensions
			// accepted something this mounting cannot serve, which is a wiring
			// defect in the composition rather than a runtime condition — and
			// serving the core surface with the extension routes silently
			// missing would publish a contract nothing honours. Same posture as
			// the composition's other boot-time refusals: fail loudly.
			panic("compose: mounting the composed extension routes: " + err.Error())
		}
		implemented := 0
		for _, route := range routes {
			if route.Implemented {
				implemented++
			}
		}
		// Both counts, because their difference is the contract-only set — the
		// operations this installation publishes and answers 501 for. An
		// operator seeing a 501 in the access log should find the number here
		// rather than reading it as a fault.
		log.Info("extensions: routes mounted", "routes", len(routes), "implemented", implemented)
		return mux
	}
}
