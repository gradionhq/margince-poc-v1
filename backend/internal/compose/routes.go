// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP mux assembly: contractAPI builds the generated /v1 surface with
// its admission/idempotency/overlay-guard middleware stack, and
// operationalMux mounts that surface next to the operational edges (health
// probes, metrics, the anonymous public paths, the A2 authorization server,
// and the provider push webhooks). server.go owns the Server inventory and
// its wiring options; this file owns how those handlers become one mux.

import (
	"context"
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
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// contractAPI builds the generated contract router with the ADR-0055
// admission layer, idempotency, and the overlay-mode write guard wrapped
// around it (outermost last — see the wrap-order note inline).
func contractAPI(srv Server, pool *pgxpool.Pool, identitySvc *identity.Service) http.Handler {
	gate := auth.NewGate(identitySvc)
	registry := registryWithGate(pool, gate, srv.replyDrafter, srv.resolveOverlayIncumbent(pool))
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
			agentGate(registry, staging, provider, fieldOwnership{pool: pool}, gate),
			idempotency(pool),
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

// operationalMux mounts the contract surface next to the operational
// edges: health probes, metrics, the anonymous public paths, and the A2
// authorization server.
func operationalMux(srv Server, pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, api http.Handler) *http.ServeMux {
	// The session middleware (authH.Middleware) fronts BOTH /v1 and the /oauth/
	// authorization server (/oauth/authorize requires a live session); the
	// health probes, metrics, discovery documents, and the provider push
	// webhooks are unauthenticated by design (each webhook verifies itself).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.HandleFunc("/readyz", httpserver.Readyz(srv.aiStateOrDefault(), srv.readyzEmbedState(), srv.readinessChecks(pool.Ping)...))
	mux.HandleFunc("/metrics", httpserver.Metrics(pool,
		func(ctx context.Context) (int64, error) { return events.OutboxBacklog(ctx, pool) },
		events.PublishedTotal,
		srv.writeAIMetrics,
		overlayMetricsSection(srv, pool)))
	// The anonymous public edges sit between the session middleware (which
	// lets /v1/public/ through without session or workspace) and the
	// router: each resolves its own token/slug → tenant, throttles, and
	// binds a confined system principal. The preference edge wraps the
	// booking edge — each passes a non-matching path straight through.
	publicEdge := publicPreferences(consent.NewStore(pool), newPublicPreferenceLimiters())(
		publicBooking(activities.NewStore(pool), newPublicBookingLimiters())(api),
	)
	mux.Handle("/v1/", httpserver.Correlate(httpserver.AccessLog(log, authH.Middleware(publicEdge))))
	// The A2 authorization server (ADR-0013): AS endpoints live outside
	// the generated resource surface but behind the same workspace and
	// session middleware; the discovery documents are static.
	mux.Handle("/oauth/", httpserver.Correlate(httpserver.AccessLog(log, authH.Middleware(authH.OAuthRouter()))))
	mux.HandleFunc("/.well-known/oauth-authorization-server", identity.OAuthServerMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource", identity.ProtectedResourceMetadata)
	// Provider push webhooks: unauthenticated by nature (the provider is the
	// caller), each verified by its own mechanism inside the handler; mounted
	// only when configured — the route is absent otherwise.
	if srv.gmailPush != nil {
		mux.Handle("/webhooks/gmail-push", httpserver.Correlate(httpserver.AccessLog(log, srv.gmailPush)))
	}
	if srv.overlayWebhook != nil {
		mux.Handle("/webhooks/hubspot", httpserver.Correlate(httpserver.AccessLog(log, srv.overlayWebhook)))
	}
	return mux
}
