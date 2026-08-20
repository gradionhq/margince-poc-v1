// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The inventory: which transport handler sets the contract surface is made of.
//
// Split from server.go, which the file itself already distinguished — that one
// is how the surface is ASSEMBLED (New, the routes, the middleware); this is
// what it is assembled FROM. The list grows with every module that gains a
// transport, so keeping it beside the assembly meant a new handler set nudged
// an unrelated file toward its ceiling.

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/compose/network"
	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/orgbrief"
	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/licensecheck"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
)

// Server satisfies crmcontracts.ServerInterface by embedding: every
// module transport handler set together covers the full contract
// surface.
type Server struct {
	authHandlers
	peopleHandlers
	dealsHandlers
	contractsHandlers
	commissionsHandlers
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
	transcriptReadHandlers
	// transcriptOnLanding starts a reading when a transcript is WRITTEN, not
	// only when one is requested. Set by WithTranscriptRead; nil in a
	// deployment with no brain for it, and then a transcript is simply stored.
	transcriptOnLanding activities.TranscriptReadEnqueue
	documentReadHandlers
	scrapeHandlers
	connectorHandlers
	backfillHandlers
	aiRoutingHandlers
	captureSettingsHandlers
	ownDomainHandlers
	installationSettingsHandlers
	licenseHandlers
	consumerMailDomainHandlers
	blockedDomainHandlers
	captureExclusionHandlers
	claimHandlers
	importHandlers
	channelHandlers
	traceHandlers
	pipelineTraceHandlers
	filteredExportHandlers
	filterPreviewHandlers
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
	// The composed-extension inventory (handlers_extensions.go). Stateless — it
	// reads the package's own boot-written accessors — so it is embedded as the
	// zero value rather than assembled in serverassembly.go.
	extensionsHandlers
	// The transport directory (handlers_channelproviders.go): stateless, embedded the same way.
	channelProvidersHandlers
	org360Handlers
	person360Handlers
	personBriefHandlers
	meetingBriefHandlers
	personResearchHandlers
	personDraftHandlers
	orgBriefHandlers
	orgDossierHandlers
	accountDraftHandlers
	financeHandlers
	integrationsHandlers

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

	// bootstrapSeeds are the deployment file's `seeds`, carried here so a
	// CLAIM lays down the same module defaults a configured bootstrap would.
	// Injected by WithBootstrapSeeds; the zero value seeds the built-in
	// defaults, which is what an installation with no `seeds` section gets on
	// either path.
	bootstrapSeeds deployconfig.Seeds

	// busReady is the /readyz bus probe, injected only by the process
	// role that runs the inline relay — a split deployment's api answers
	// ready on Postgres alone.
	busReady func(context.Context) error

	// uploadLimits are the deployment's per-route body ceilings for the routes
	// that carry files (OPS-CFG-12), injected by WithUploadLimits. They govern
	// three things that must agree: the chassis ceiling each route rides, the
	// cap its handler parses under, and the number the installation read
	// publishes to a client. Defaulted in New, so a composition never told
	// about a deployment file still gets the compiled-in ceilings rather than
	// zero — which would refuse every upload.
	uploadLimits deployconfig.UploadLimits

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

	// licensePosture answers this installation's entitlement at scrape time,
	// set by WithLicensePosture. A function rather than a value because the
	// posture is re-resolved while the process runs — a license lapses on a
	// calendar, not on a deploy — and nil means a role that resolved none
	// reports no license section at all.
	licensePosture func() licensecheck.Posture

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
	// person360Svc is the person page's composite read, held for the same
	// reason org360Svc is: the relationship brief is assembled from THIS gated
	// read rather than a second one that could drift from what the page shows.
	person360Svc *person360.Service

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
