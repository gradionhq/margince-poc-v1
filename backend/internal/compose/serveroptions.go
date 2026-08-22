// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Option is the per-process-role customization surface for Server: every
// role starts from newServer's safe defaults and layers on exactly the
// Options its deployment needs (a bus relay, a blobstore, a model lane, …).
// What's not optioned in stays its safe default — declared by omission,
// never a silent guess at request time.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// Option customizes the wiring for one process role; everything not
// optioned keeps its safe default.
type Option func(*Server, *pgxpool.Pool)

// WithOperatorMail wires the operator's own transactional mailer — the
// ADR-0056 transport the INSTALLATION sends through, as distinct from a
// rep's mailbox, which is what correspondence goes out on.
//
// It is named for the transport rather than for one consumer because it
// has more than one. Password reset is the consumer that exists today, and
// the invite mail rides the same door; the emailed daily digest
// (UC-NOTIFY-03) is the next one, and it would otherwise have had to be
// wired through an option whose name says password reset.
//
// Without it, forgot-password answers its explicit 501 and the
// capabilities probe reports password_reset=false (A107 — the login UI
// renders only what works). The link base is NOT wired here; it arrives
// through WithPublicBaseURL, because an installation with no mailer still
// builds set-password links (ADR-0061 Amendment 1).
func WithOperatorMail(m mailer.Mailer) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithPasswordReset(m)
	}
}

// WithDealRoomInviteMail wires the operator relay into Deal Room invitations.
//
// It rides the SAME mailer as password reset rather than a second channel: both
// are product-originated transactional mail an operator configures once, and a
// separate relay would let an installation deliver one and silently not the
// other. The link base arrives separately through WithPublicBaseURL, for the
// reason stated there — a buyer link carries a live credential, so its origin
// must never come from a request Host.
//
// NOT WIRED BY ANY ROLE YET, deliberately. The link this would mail points at a
// buyer screen the SPA does not serve, so a recipient would land on the
// not-found page having spent their one credential getting there. Until that
// screen and the credential exchange exist, the invite response hands the raw
// credential to the seller, who passes it on — the same path an installation
// with no mail relay already takes. cmd/api adds this option in the slice that
// builds the buyer surface.
func WithDealRoomInviteMail(m mailer.Mailer) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.dealroomsHandlers = s.WithInviteMailer(m)
	}
}

// WithMCPResource injects the canonical MCP resource URL — public_base_url
// + "/mcp" — onto the identity discovery handlers, so the RFC 9728
// protected-resource document names the MCP server URL itself rather than
// the bare request origin. cmd computes the value from --public-base-url;
// an OAuth audience decision must never be derived from the Host header.
// The connector's Origin guard reads its allowlist from the same value: the
// origin a browser client may present is the origin the resource document
// names, so the two cannot drift apart through a second flag.
func WithMCPResource(resource string) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithMCPResource(resource)
		s.mcpAllowedOrigin = mcpOriginOf(resource)
	}
}

// WithOAuthAccessTokenTTL shortens the passport the OAuth handshake mints —
// the code exchange and every rotation alike. Without it a connector's access
// token keeps the passport default (30 days), which is the posture every
// deployment ran before this knob existed; with it an operator can take that to
// connector norms (minutes plus refresh) without a code change, and the refresh
// machinery is what makes that cheap. cmd passes it from
// --oauth-access-token-ttl / MARGINCE_OAUTH_ACCESS_TOKEN_TTL.
func WithOAuthAccessTokenTTL(ttl time.Duration) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithOAuthAccessTokenTTL(ttl)
	}
}

// WithMCPConnector turns the remote MCP connector on: the /mcp transport,
// the OAuth authorization server and both discovery documents are mounted
// together. Without it none of those routes exists — an installation that
// never declared the connector serves no client registration and no
// passport-minting token endpoint, so it needs no runtime guard for them.
// cmd passes it from the deployment file's mcp.connector_enabled.
func WithMCPConnector() Option {
	return func(s *Server, _ *pgxpool.Pool) { s.mcpConnectorEnabled = true }
}

// WithBusReady adds the event-bus probe to /readyz. The api role passes
// it when it runs the inline relay: a process that must ship events is
// not ready while the bus is unreachable.
func WithBusReady(check func(context.Context) error) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.busReady = check }
}

// WithBlobstore wires the object store: it feeds the /readyz probe and
// backs the attachment handlers, the offer PDF render endpoint, and the
// organization-logo stream. Without it those endpoints stay their
// generated/explicit 501, so a role that stores no objects declares that
// by omission rather than nil-derefing at request time. Several handler
// sets promote a WithBlobstore method, so s.WithBlobstore itself would be
// an ambiguous selector — each call is qualified through its own embedded
// field instead.
func WithBlobstore(store blobstore.Store) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.blob = store
		// A buyer's document download reads the same store the seller's
		// attachment upload wrote to.
		s.dealroomsHandlers = s.WithDocumentStore(store)
		// Captured mail carries files too. Recorded as the STORE, which the sink
		// turns into a writer when it is built: a keeper assigned here would be
		// dropped by a WithCaptureConfig that runs afterwards and assigns the
		// whole struct, and that failure has no error and nothing missing to
		// see until somebody looks for a file that never arrived.
		s.captureConfig.Blob = store
		s.activitiesHandlers = s.activitiesHandlers.WithBlobstore(store)
		s.dealsHandlers = s.dealsHandlers.WithBlobstore(store)
		s.peopleHandlers = s.peopleHandlers.WithBlobstore(store)
		// Erasure must reach the attachment bytes, not only the rows, so the
		// DSR erase path gets a blob-aware eraser (Art. 17).
		s.consentHandlers = s.WithEraser(privacy.NewEraser(InstallationDB(pool)).WithBlobstore(store))
		// The controller's release on the retention surface is an erasure too,
		// and reaches the same bytes.
		s.privacyHandlers = s.privacyHandlers.WithBlobstore(store)
		// The data reset sweeps the same bytes for a whole workspace. Set here
		// as well as read in WithDataReset so neither option order leaves the
		// reset silently unable to reach the object store.
		s.dataResetHandlers.blob = store
		// An uploaded import source is object bytes like any other, and the
		// import refuses rather than pretends when a role stores none. The
		// field is the embedded importHandlers'; Server's own `blob` above is
		// the readiness probe's copy.
		s.blobs = store
		// Same two-way wiring for the onboarding confirmation, which collects
		// the mark its anchor declined to adopt (WithDeepRead reads s.blob).
		if s.siteReadHandlers.engine != nil {
			s.siteReadHandlers.engine.blob = store
		}
	}
}

// WithKeyvault wires the secret store: it feeds the /readyz probe and backs
// the capture connector-credential path (Authenticate seals the credential
// bundle, Sync resolves it). Without it a role that persists or resolves
// connector credentials declares that gap at wiring time rather than
// nil-derefing at Authenticate — a capture-capable role must pass this or
// fail to boot (enforced in cmd).
//
// It ALSO installs the outbound send pre-flight (WithSendAuthority) over the
// registry it just ensured exists, so the channel half of that check — is
// there a live bot bound for this provider? — is live on every
// capture-capable role, Google app or not: NewCaptureRegistry registers
// Telegram unconditionally, so the registry answers that question correctly
// even with no Gmail/Graph app configured. A role that later configures
// Gmail (WithGmailCapture) re-wires this over its own richer registry, which
// upgrades the mailbox half without ever making the channel half depend on
// that config.
func WithKeyvault(vault keyvault.Vault) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.vault = vault
		// Backfilled for the same reason the object store is: WithDataReset may
		// have already run, and a reset that cannot reach the vault leaves the
		// sealed credentials of the installation it just wiped resident.
		s.dataResetHandlers.vault = vault
		// Rebuild the capture registry with the vault so the connector-
		// credential paths (Connect seals, Sync resolves) have their custodian.
		// The standing IMAP connect rides this same registry and needs no
		// OAuth app; WithGmailCapture later replaces this with its own
		// gmail-carrying registry when the app is configured.
		if s.connectorHandlers.registry == nil {
			s.connectorHandlers = connectorHandlers{
				registry:  NewCaptureRegistry(pool, vault, s.captureConfig),
				authority: identity.NewService(pool),
			}
		}
		// The overlay incumbent connection lifecycle needs the same
		// custodian: Connect seals the private-app token, Disconnect
		// resolves-then-deletes it. s.overlayMeter is the Server's own
		// shared instance (constructed unconditionally in newServer) so
		// GetOverlayBudget answers from the SAME meter contractAPI's
		// Dispatcher spends force-fresh reads against.
		s.overlayHandlers = NewOverlayHandlers(pool, vault, s.overlayMeter, s.log, s.overlayBackfillLimit, s.sorDispatch.Invalidate)
		// Now that the vault is wired, install the live per-workspace
		// incumbent resolver on the overlay read dispatch — force-fresh
		// reads can reach HubSpot (Authoritative:true), no longer degrading
		// to the mirror unconditionally. newServer built the dispatch with a
		// nil resolver because the vault arrives only here; the dispatch is
		// a shared pointer, so this reaches the same instance that serves
		// reads. Boot-time only (before serving), so it never races a Read.
		// Guarded for the isolated-option unit tests that apply WithKeyvault
		// to a Server with no dispatch wired; the real newServer path always
		// has one.
		if s.sorDispatch != nil {
			s.sorDispatch.SetOverlayIncumbentResolver(s.resolveOverlayIncumbent(pool))
		}
		// The channel connect path needs the same custodian: it seals the bot
		// token and destroys it on disconnect. A role that composed no channel
		// transport is left that way (channelconnect.go).
		s.channelHandlers = s.WithVault(vault)
		// The pre-flight reads whichever registry the lines above just
		// ensured exists — the SAME one, never a second construction — so a
		// mailbox or bot connected through it is a mailbox or bot the check
		// asks about. WithGmailCapture below re-wires this same call over its
		// own registry when the Google app is configured; until then this is
		// the only place the channel branch gets to run at all.
		installSendPreflight(s, pool)
	}
}

// WithOverlayBackfillLimit bounds the overlay initial mirror backfill at
// limit records per object class (dev/demo — MARGINCE_OVERLAY_BACKFILL_LIMIT).
// It must be applied BEFORE WithKeyvault (which builds the overlay handlers
// off s.overlayBackfillLimit); cmd/api orders them that way. 0 is uncapped.
func WithOverlayBackfillLimit(limit int) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.overlayBackfillLimit = limit }
}

// WithOverlayMeter Rebinds the Server's shared OVB meter to the live,
// Redis-backed meter cmd built. newServer constructs the meter fail-closed
// (nil Redis) and shares that ONE pointer with the read dispatch and the
// budget handlers, so this RebindFrom reaches every holder regardless of
// option order — force-fresh reads and the budget surface all meter against
// the same Redis windows. Taking the already-built *overlaybudget.Meter
// (not a *redis.Client) keeps the raw-Redis dependency in cmd, never in
// compose. Without this option the meter stays fail-closed (every
// force-fresh read sheds to the mirror), the honest posture for a role with
// no Redis.
func WithOverlayMeter(meter *overlaybudget.Meter) Option {
	return func(s *Server, _ *pgxpool.Pool) { s.overlayMeter.RebindFrom(meter) }
}

// WithAgentQuota Rebinds the Server's shared MCP-SESS-* meter to the live,
// Redis-backed one cmd built. newServer constructs it fail-closed (nil Redis)
// and hands that ONE pointer to both halves of the bound — the admission gate
// that refuses on it and the tool registry that charges it — so this
// RebindFrom reaches both together and they can never end up counting against
// different windows.
//
// Taking the already-built *agentquota.Meter (not a *redis.Client) keeps the
// raw-Redis dependency in cmd, never in compose. Without this option the meter
// stays fail-closed: a role serving the agent surface with no Redis cannot
// tell whether an agent has passed any of its bounds, and answers that it has.
//
// The COST ceiling is installed here rather than in cmd because both halves of
// that division live behind the pool this option is handed: the workspace's AI
// budget and the credentials sharing it. cmd owns the Redis client; compose
// owns what the workspace's own numbers mean.
func WithAgentQuota(meter *agentquota.Meter) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.quotaMeter.RebindFrom(meter.WithCostCeiling(newPassportShareCeiling(pool, meter.Window())))
	}
}

// WithRetrievalEmbedder binds this role's embed lane to the REQUEST path, so
// hybrid retrieval can use its vector half for a caller and not only for a
// background job.
//
// It rebuilds the tool registry, because the registry is where the lane is
// consumed: the intent retriever, search_context and the query executor are all
// constructed inside it. An option that set the field without rebuilding would
// leave a Server whose embedder is bound and whose tools still rank lexically —
// a divergence nothing would report, because a lexically ranked page looks
// exactly like a semantic one that found little.
//
// Without this option the lane stays unbound, which is a real deployment (a role
// with no model path, or a routing config that binds no embeddings model) rather
// than a broken one: every ranked answer says which lane ranked it.
func WithRetrievalEmbedder(embedder search.Embedder) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.retrievalEmbedder = embedder
		s.rebuildToolRegistry(pool)
	}
}

// readinessChecks assembles the /readyz dependency probes for this role.
// Postgres and the runtime role it connects as are always probed; the bus,
// the object store, the secret vault, and the schema pool are probed only
// when this role wired them, so a split deployment answers ready on exactly
// what it depends on. A wedged dependency must fail readiness — a probe is
// never dropped to keep the pod in rotation.
//
// runtimeRole takes the same shape as pgPing rather than a pool, because the
// two unit-testable states here are the answers, not the connections: both
// arrive as the caller's readings of the one pool routes.go serves from.
func (s *Server) readinessChecks(pgPing, runtimeRole func(context.Context) error) []httpserver.ReadyCheck {
	checks := []httpserver.ReadyCheck{
		{Name: "postgres", Check: pgPing},
		// Boot already refused a pool holding an exemption; this reports the
		// same fact for the rest of the process's life, because the role's
		// attributes are cluster state a grant can change under a running
		// replica without restarting it.
		{Name: "runtime-role", Check: runtimeRole},
	}
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

// WithPublicBaseURL sets the canonical scheme+host the buyer-facing
// unsubscribe/preference links resolve to (B-E11.32). It is configured at
// boot, never derived from a request: the link carries the recipient's
// unsubscribe token. Without it a marketing send refuses rather than emit
// a forgeable link.
func WithPublicBaseURL(base string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.send.PublicBaseURL = base
		// The identity surface builds set-password deep links on the same
		// canonical base, and for the same reason: the link carries a live
		// single-use credential, so it must never be derived from a request
		// Host an attacker controls.
		s.authHandlers = s.WithPasswordLinkBase(base)
		// A Deal Room invitation carries the same kind of credential and is
		// bound to the same canonical origin for the same reason.
		s.dealroomsHandlers = s.WithInviteLinkBase(base)
		s.rebuildToolRegistry(pool)
	}
}

// WithDelivery wires the machinery an accepted send is staged for
// transmission with, onto BOTH send transports this role serves: the HTTP
// handler and the MCP send_email tool. Without it a send refuses rather than
// log an activity claiming a message went out.
//
// It carries BOTH staging shapes (DeliveryMachinery), so the mail send and the
// channel reply are wired by one call: they are the same machinery, and a role
// that wired one without the other would serve a surface accepting messages
// nothing will carry.
func WithDelivery(stager DeliveryMachinery) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.send.Delivery = stager
		s.rebuildToolRegistry(pool)
	}
}

// WithSendAuthority wires the send pre-flight onto the same transports, so a
// user with no send-capable mailbox — or a workspace with no bot bound — is told
// what to do about it instead of being handed a 202 for a message that can only
// park.
func WithSendAuthority(authority activities.SendAuthority) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.send.SendAuthority = authority
		s.rebuildToolRegistry(pool)
	}
}

// WithColdStart enables the cold-start read-back over the given fetch
// and model seams. Without it the operation stays an explicit 501 —
// the api role must DECLARE its model path, never pick one silently.
func WithColdStart(fetch PageFetcher, brain completer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.coldstartHandlers = coldstartHandlers{engine: &coldStartEngine{
			extract:   evidenceExtractor{fetch: fetch, brain: brain},
			approvals: approvals.NewService(InstallationDB(pool)),
		}}
	}
}

// WithScrape enables per-organization enrichment (scrapeCompany) over the same
// fetch and model seams as the read-back. Without it the operation stays an
// explicit 501 — the api role must DECLARE its model path, never pick one
// silently.
func WithScrape(fetch PageFetcher, brain completer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.scrapeHandlers = scrapeHandlers{engine: &scrapeEngine{
			extract:   evidenceExtractor{fetch: fetch, brain: brain},
			people:    people.NewStore(InstallationDB(pool)),
			approvals: approvals.NewService(InstallationDB(pool)),
		}}
	}
}

// WithBrief enables the Morning-Brief L2 ranker (B-E05.2) over the given
// model lane. Without it the brief still serves fully on the deterministic
// §10.1 composite — the L2 layer is advisory over that floor, never a
// prerequisite for the home surface.
func WithBrief(brain completer) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.WithL2Ranker(brain, s.log)
	}
}
