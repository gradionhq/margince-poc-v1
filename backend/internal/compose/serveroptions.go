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

	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// Option customizes the wiring for one process role; everything not
// optioned keeps its safe default.
type Option func(*Server, *pgxpool.Pool)

// WithPasswordReset wires the A74 forgot-password flow onto the identity
// surface: the operator's transactional mailer plus the public base URL
// the emailed link points at. Without it the reset endpoints answer
// their explicit 501 and the capabilities probe reports
// password_reset=false (A107 — the login UI renders only what works).
func WithPasswordReset(m mailer.Mailer, publicBaseURL string) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.authHandlers = s.WithPasswordReset(m, publicBaseURL)
	}
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
		s.activitiesHandlers = s.activitiesHandlers.WithBlobstore(store)
		s.dealsHandlers = s.dealsHandlers.WithBlobstore(store)
		s.peopleHandlers = s.peopleHandlers.WithBlobstore(store)
		// Erasure must reach the attachment bytes, not only the rows, so the
		// DSR erase path gets a blob-aware eraser (Art. 17).
		s.consentHandlers = s.WithEraser(privacy.NewEraser(pool).WithBlobstore(store))
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
// (--schema-dsn / MARGINCE_SCHEMA_DSN). It feeds the
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

// WithDataReset wires the non-production admin data-reset endpoint
// (POST /admin/reset-data): pool is the app-role pool the sweep runs
// through, schemaPool (may be nil) is the owner-privileged pool that
// finalizes cf_* column drops — nil skips that finalize step, the reset
// itself still succeeds. Absent this option, or outside a non-production
// posture, the endpoint answers 404 (dataResetHandlers' zero value has a
// nil pool, the same closed default).
func WithDataReset(pool, schemaPool *pgxpool.Pool, seeds deployconfig.Seeds, env runtimeenv.Environment) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.dataResetHandlers = dataResetHandlers{
			pool: pool, schemaPool: schemaPool, seeds: seeds, env: env, log: s.log,
		}
	}
}

// Every send option below records onto s.send and NOTHING else. The HTTP
// handlers' own store is reconciled from that one value once every option has
// run (New's applySendPath), and the tool surface is rebuilt over it here, so
// no option can configure one transport and leave the others silently
// without.

// WithPublicBaseURL sets the canonical scheme+host the buyer-facing
// unsubscribe/preference links resolve to (B-E11.32). It is configured at
// boot, never derived from a request: the link carries the recipient's
// unsubscribe token. Without it a marketing send refuses rather than emit
// a forgeable link.
func WithPublicBaseURL(base string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.send.PublicBaseURL = base
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
			approvals: approvals.NewService(pool),
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
			people:    people.NewStore(pool),
			approvals: approvals.NewService(pool),
		}}
	}
}

// WithExtractor wires the staged AI-extraction seam ONCE for both
// surfaces that consume it: the activities read (getAttachmentExtraction)
// and the accept-write re-run the SAME extractor, so what the accept
// validates field_keys against is exactly what the read staged. Without
// it both fall back to the honest-empty NoOp — the read answers
// {fields: [], omitted: []} and the accept refuses every key as
// not_grounded, never writing an unevidenced value.
func WithExtractor(extractor extraction.Extractor) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.activitiesHandlers = s.WithExtractor(extractor)
		s.attachmentExtractionHandlers = attachmentExtractionHandlers{accept: NewExtractionAccept(pool, extractor)}
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

// WithAccountBrief binds the summarize lane both of the company view's
// grounded-prose surfaces are written by — the standing brief and the
// prepared "Ask Margince" questions — and the routing version that
// identifies the binding in every cached brief's fingerprint.
//
// Without it both serve their deterministic floor rather than failing: a
// role that runs no model still answers the endpoints, and generated_by
// tells the reader which of the two they have. routingVersion rides the
// fingerprint so re-pointing this lane rewrites cached briefs instead of
// leaving text attributed to a model that no longer writes it.
func WithAccountBrief(brain completer, routingVersion string) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.orgBriefSvc = orgbrief.NewService(pool, s.org360Svc, brain, routingVersion, time.Now)
		s.orgBriefHandlers = orgbrief.NewHandlers(s.orgBriefSvc, s.sorDispatch.isOverlay)
	}
}
