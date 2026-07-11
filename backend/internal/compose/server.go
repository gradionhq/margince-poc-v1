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
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/web"
)

// Aliases give the embedded handler sets distinct field names; each
// alias carries its module's full method set.
type (
	authHandlers         = identity.Handlers
	peopleHandlers       = people.Handlers
	dealsHandlers        = deals.Handlers
	activitiesHandlers   = activities.Handlers
	approvalsHandlers    = approvals.Handlers
	searchHandlers       = search.Handlers
	consentHandlers      = consent.Handlers
	collectionsHandlers  = collections.Handlers
	signalsHandlers      = signals.Handlers
	privacyHandlers      = privacy.Handlers
	agentsHandlers       = agents.Handlers
	voiceHandlers        = ai.Handlers
	customfieldsHandlers = customfields.Handlers
)

// Server satisfies crmcontracts.ServerInterface by embedding: every
// module transport handler set together covers the full contract
// surface, so there is no residual stub embed left to shadow.
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
	briefs.Handlers
	coldstartHandlers
	scrapeHandlers
	imapConnectHandlers
	filteredExportHandlers
	orgRollupHandlers
	customfieldsHandlers

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

	// schemaPoolReady is the /readyz schema-pool probe, injected only by
	// WithSchemaPool — a role that never mounted --schema-dsn declares
	// that by omission (customfields.Create/SetOptions stay their
	// generated 501, decisions/0024) rather than probing a pool it
	// doesn't have.
	schemaPoolReady func(context.Context) error

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

// WithBlobstore wires the object store: it feeds the /readyz probe and
// backs the attachment handlers. Without it the attachment endpoints stay
// their generated 501 stubs, so a role that stores no objects declares
// that by omission rather than nil-derefing at request time.
func WithBlobstore(store blobstore.Store) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.blob = store
		s.activitiesHandlers = s.WithBlobstore(store)
		// Erasure must reach the attachment bytes, not only the rows, so the
		// DSR erase path gets a blob-aware eraser (decisions/0022, Art. 17).
		s.consentHandlers = s.WithEraser(privacy.NewEraser(pool).WithBlobstore(store))
	}
}

// WithKeyvault wires the secret store: it feeds the /readyz probe and backs
// the capture connector-credential path (Authenticate seals the credential
// bundle, Sync resolves it). Without it a role that persists or resolves
// connector credentials declares that gap at wiring time rather than
// nil-derefing at Authenticate — a capture-capable role must pass this or
// fail to boot (enforced in cmd, decisions/0023).
func WithKeyvault(vault keyvault.Vault) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.vault = vault
		// Rebuild the capture registry with the vault so the connector-
		// credential paths (Connect seals, Sync resolves) have their custodian.
		s.imapConnectHandlers = imapConnectHandlers{registry: NewCaptureRegistry(pool, vault)}
	}
}

// readinessChecks assembles the /readyz dependency probes for this role.
// Postgres is always probed; the bus, the object store, the secret vault,
// and the schema pool are probed only when this role wired them, so a
// split deployment answers ready on exactly what it depends on. A wedged
// dependency must fail readiness — a probe is never dropped to keep the
// pod in rotation.
func (s *Server) readinessChecks(pgPing func(context.Context) error) []httpserver.ReadyCheck {
	checks := []httpserver.ReadyCheck{{Name: "postgres", Check: pgPing}}
	if s.busReady != nil {
		checks = append(checks, httpserver.ReadyCheck{Name: "redis", Check: s.busReady})
	}
	if s.blob != nil {
		checks = append(checks, httpserver.ReadyCheck{Name: "blobstore", Check: s.blob.Health})
	}
	if s.vault != nil {
		checks = append(checks, httpserver.ReadyCheck{Name: "keyvault", Check: s.vault.Health})
	}
	if s.schemaPoolReady != nil {
		checks = append(checks, httpserver.ReadyCheck{Name: "customfields-schema-pool", Check: s.schemaPoolReady})
	}
	return checks
}

// WithSchemaPool wires the owner-privileged schema-change pool the
// customfields engine's two runtime-DDL paths (Create, SetOptions) need
// (decisions/0024: --schema-dsn / MARGINCE_SCHEMA_DSN). It feeds the
// /readyz probe and rebuilds the customfields handlers over the real
// pool; without it those two operations stay their generated 501
// (ErrSchemaChangesUnavailable) rather than nil-derefing a pool that was
// never mounted — a role that runs no runtime DDL declares that by
// omission, the same posture as WithBlobstore/WithKeyvault.
func WithSchemaPool(schemaPool *pgxpool.Pool) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.customfieldsHandlers = customfields.NewHandlers(pool, schemaPool)
		s.schemaPoolReady = schemaPool.Ping
	}
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
		s.WithL2Ranker(brain, s.log)
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
	authH := identity.NewHandlers(identitySvc, workspaceSeed(dealsH))

	srv := newServer(pool, log, authH, dealsH)
	for _, opt := range opts {
		opt(&srv, pool)
	}

	api := contractAPI(srv, pool, identitySvc)
	mux := operationalMux(srv, pool, log, authH, api)

	return httpserver.RecoverPanics(log, httpserver.LimitBodies(httpserver.SecureHeaders(mux)))
}

// workspaceSeed is the workspace-bootstrap hook identity runs: it seeds
// every module's per-workspace defaults in ONE transaction (C5) — the
// default pipeline and the consent purpose catalog stand or fall
// together.
func workspaceSeed(dealsH dealsHandlers) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
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
		var adminID ids.UserID
		if err := tx.QueryRow(ctx, `SELECT id FROM app_user ORDER BY created_at LIMIT 1`).Scan(&adminID); err != nil {
			return err
		}
		_, err := activities.SeedBookingPageTx(ctx, tx, adminID)
		return err
	}
}

// newServer assembles the module handler sets. Every cross-module edge
// is injected HERE, never as a sibling import (ADR-0054).
func newServer(pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, dealsH dealsHandlers) Server {
	return Server{
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
		Handlers: briefs.NewHandlers(briefs.NewBriefEngine(pool, people.NewStore(pool))),
		// The one-shot IMAP pull shares the capture registry (Sink + the
		// live-authority principal swap); credentials arrive per request and
		// are never persisted (RunTransient), so the default registry needs no
		// vault — WithKeyvault rebuilds it with one for the persisting paths.
		imapConnectHandlers: imapConnectHandlers{registry: NewCaptureRegistry(pool, nil)},
		// First-class filtered export (B-E15.13): the writer reuses the ONE
		// predicate engine + the bundle writer's open-format rendering; the
		// collections store resolves a saved view / dynamic list source
		// behind its own visibility gate.
		filteredExportHandlers: filteredExportHandlers{
			writer:      NewFilteredExportWriter(pool),
			collections: collections.NewStore(pool),
		},
		orgRollupHandlers: orgRollupHandlers{pool: pool, now: time.Now},
		// The schema-change pool is boot-optional (decisions/0024); nil
		// here means Create/SetOptions stay their generated 501 until the
		// api role's WithSchemaPool rebuilds this over the real pool.
		customfieldsHandlers: customfields.NewHandlers(pool, nil),
		log:                  log,
	}
}

// contractAPI mounts the generated contract router with the ADR-0055
// admission layer, which rides INSIDE the router (it needs the matched
// route pattern) and shares the MCP surface's tier table, approvals
// staging, and live-authority gate — one gate, two transports.
func contractAPI(srv Server, pool *pgxpool.Pool, identitySvc *identity.Service) http.Handler {
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
	return api
}

// operationalMux mounts the contract surface next to the operational
// edges: health probes, metrics, the anonymous public paths, the A2
// authorization server, and the embedded SPA.
func operationalMux(srv Server, pool *pgxpool.Pool, log *slog.Logger, authH authHandlers, api http.Handler) *http.ServeMux {
	// Only /v1 rides the session middleware; the embedded SPA and the
	// health probe are static and unauthenticated (the SPA's every data
	// access goes back through /v1 — it holds no privileged path).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", httpserver.Healthz)
	mux.HandleFunc("/readyz", httpserver.Readyz(srv.readinessChecks(pool.Ping)...))
	mux.HandleFunc("/metrics", httpserver.Metrics(pool,
		func(ctx context.Context) (int64, error) { return events.OutboxBacklog(ctx, pool) },
		events.PublishedTotal))
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
	mux.Handle("/", web.Handler())
	return mux
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
