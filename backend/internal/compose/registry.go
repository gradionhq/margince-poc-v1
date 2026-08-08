// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The governed MCP tool surface, assembled: the agents registry over the
// composite datasource provider, with the approvals engine injected as
// the staging/redemption dependency — composed here so agents never
// imports a sibling module (ADR-0054 §9).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// NewRegistry wires the full 🟢/🟡 tool set over the composite provider.
// The admission gate re-derives authority through the shared/ports/authz
// seam, which identity implements — injected here so platform/auth never
// imports a module (ADR-0054 §5).
func NewRegistry(pool *pgxpool.Pool, send SendPath) *agents.Registry {
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, nil, send, companyEnricher{})
}

// NewRegistryWithIncumbent is NewRegistry plus the per-workspace live-incumbent
// resolver the overlay write-back path (Create/Update/Archive) reaches HubSpot
// through — the wiring a role with a vault (the api server) installs so the MCP
// tool surface can actually write back, not just answer errNoWriteIncumbent.
func NewRegistryWithIncumbent(pool *pgxpool.Pool, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *agents.Registry {
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, resolveIncumbent, send, companyEnricher{})
}

func registryWithDraftBrain(pool *pgxpool.Pool, brain completer, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *agents.Registry {
	if brain == nil {
		return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, resolveIncumbent, send, companyEnricher{})
	}
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), newReplyDrafter(pool, brain, nil), resolveIncumbent, send, companyEnricher{})
}

// registryWithGate composes the tool surface. The read-bound charger arrives as
// an option rather than a parameter because only the API server — the one role
// that serves agent principals through the MCP and REST doors — has a meter to
// charge. The Surface-B runner and the workflow paths run as the human or the
// system that started them, and readmeter governs agents only, so a registry
// built without one is not an unmetered agent surface; it is a surface no agent
// reaches.
func registryWithGate(pool *pgxpool.Pool, gate *auth.Gate, drafter activities.EmailDrafter, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath, enricher agents.CompanyEnricher, opts ...agents.RegistryOption) *agents.Registry {
	// The Dispatcher is the datasource seam every core/slipping tool
	// rides: a native-mode workspace lands on the composite SoR
	// Provider exactly as before, an overlay-mode workspace's reads land
	// on the mirror (design.md §4.2/§4.6) — chosen per call from
	// ctx, never fixed at registry construction time.
	//
	// No tool reaches Dispatcher.Freshness, the only route to a force-fresh
	// reservation on this provider, so this surface has no spend of its own to
	// account for and its OVB meter is a fail-closed placeholder (no Redis),
	// never charged; the live reservations and charges live in the refetch and
	// reconcile pollers, which take their own Redis-backed meters. When a
	// metered force-fresh path lands for a tool, this becomes a Redis-backed
	// NewOverlayMeter like the REST surface's, sharing the same per-workspace
	// windows.
	native := NewProvider(pool)
	provider := NewDispatcher(native, NewOverlayProvider(pool, failClosedOverlayMeter(), resolveIncumbent), pool)
	registry := agents.NewRegistry(approvalsAdapter{svc: approvals.NewService(pool)}, gate, opts...)
	// The guards take the Dispatcher as an overlayModeChecker — the interface
	// whose method IS the uncached read, so no wiring here can hand them the
	// cached mode. See overlayModeChecker for why that distinction is typed.
	sorMode := overlayModeChecker(provider)
	agents.RegisterCoreTools(registry, provider, provider, provider, fieldOwnership{pool: pool})
	// list_records reads its rows through the Dispatcher like every other
	// record verb, and its filter VOCABULARY off the native provider: the
	// vocabulary is a property of the deployment's own stores, resolved once at
	// boot, while whether a given workspace's rows come from those stores or
	// from a mirror is a per-call question the Dispatcher answers. An overlay
	// workspace refuses the filtered call rather than answering it unnarrowed.
	agents.RegisterListTool(registry, provider, native)
	// The three lifecycle transitions reach their owning modules directly
	// rather than through the Dispatcher: each one's behaviour IS that
	// module's entry point, which is what the REST route calls too.
	relinker, disqualifier, advancer := lifecycleSeams(pool)
	// disqualify_lead is the one of the three the overlay provider cannot
	// serve for a mirrored type, so it takes the guard the REST middleware
	// applies to the same verb; relink and project-phase are not SoR record
	// writes and stay available in either mode.
	agents.RegisterLifecycleTools(registry, provider, relinker, nativeOnlyDisqualifier(sorMode, disqualifier), advancer)
	// enrich rides the site-read seam rather than the datasource one: it reads
	// the company's OWN website, which no record provider can answer.
	agents.RegisterEnrichTool(registry, provider, enricher)
	// Pipeline config, and it registers next to the core CRUD set because it is
	// what makes two of those verbs reachable: create_record for a deal and
	// advance_deal both name ids no other tool yields. Config is not a record,
	// so it rides its own seam rather than the datasource one — and that seam
	// needs the overlay guard the record verbs get from the Dispatcher for free.
	agents.RegisterPipelineTool(registry, nativeOnlyPipelines(sorMode, pipelineLister(pool)))
	agents.RegisterReportTool(registry, nativeOnlyReportRunner(sorMode, reportToolRunner(newReportEngine(pool))), reportToolCatalog())
	// The intent tools ground on the graph walk (no embed lane needed);
	// the comms tools ride the same store paths as the HTTP transport.
	// The overlay guard stays OUTERMOST so a mirror-backed workspace is
	// refused before either read runs; the risk decorator sits inside it and
	// adds the coverage findings a deal anchor would otherwise assemble
	// without.
	agents.RegisterIntentTools(registry, nativeOnlyRetriever{
		mode: sorMode,
		inner: riskAwareRetriever{
			pool:  pool,
			inner: search.NewRetriever(search.NewStore(pool), nil),
		},
	})
	// The pipeline-risk intents: the candidate set rides the deals
	// module's row-scoped list, the drafts land through the provider.
	agents.RegisterSlippingTools(registry, nativeOnlySlippingLister(sorMode, slippingLister(pool)), followUpDrafter(provider))
	// The relationship-graph reads (ADR-0078): who here knows this contact,
	// how a deal is covered, who can get us into an account, and which of the
	// caller's deals the coverage rules flag. All 🟢 — they name people, they
	// change nothing.
	agents.RegisterNetworkTools(registry, whoKnowsLister(pool), coverageReader(pool),
		nativeOnlyIntroPath(sorMode, introPathLister(pool)),
		nativeOnlyAtRisk(sorMode, atRiskLister(pool)))
	agents.RegisterCommsTools(registry, newCommsAdapter(pool, drafter, send), provider)
	// The composed extension set's governed tools ride the same registry
	// and admission gate as the core tools, registered last so a name that
	// collides with a core verb fails loudly (RegisterExtensions stashed
	// them at boot, before this ran).
	//
	// They need no native-only guard: extension.ToolHandler is handed a context
	// and raw JSON and nothing else — no provider, no pool, no store — and the
	// boot adapter injects none, so an extension tool cannot read a domain table
	// to answer wrongly for an overlay workspace. If that surface ever grants
	// record access, it has to arrive mode-routed through the datasource seam,
	// or gain the guard the three dependencies above take.
	registerComposedTools(registry)
	return registry
}

// reportToolRunner adapts the engine to the tool seam: decode the
// plan arguments, run, re-encode the contract-shaped result.
func reportToolRunner(engine *reportEngine) agents.ReportRunner {
	return func(ctx context.Context, report string, planArgs json.RawMessage) (json.RawMessage, error) {
		var req reportRequest
		if len(planArgs) > 0 {
			// STRICT: a plan argument this engine does not serve is refused by
			// name, not dropped. crm.yaml's runReport body declares `as_of_date`
			// and this engine has no field for it, so a lenient decode answered
			// an agent's request for a historical snapshot with current state and
			// no warning — a silent wrong answer, which is worse than a refusal
			// because nothing tells the caller to look again.
			// An UNSERVED key is named, before the shape refusal. The strict
			// decode alone answered "a plan argument is not the shape this tool
			// takes" and then described the three arguments the caller had not
			// sent — so a caller who sent `as_of_date` was told to re-check
			// three shapes that were never wrong, and could loop on it. Which
			// key is unserved is a question this package can answer exactly, so
			// it does, rather than restating the decoder's prose.
			if unserved := unservedPlanArguments(planArgs); len(unserved) > 0 {
				return nil, httperr.Validation("arguments", "malformed_json",
					"this tool does not take "+strings.Join(unserved, ", ")+
						"; its plan arguments are `"+slotFilters+"`, `"+slotGroupBy+"` and `"+slotAggregates+"`")
			}
			if err := strictDecodeReportPlan(planArgs, &req); err != nil {
				// Server-authored. The REST twin forwards the decoder's own text
				// under the field `body`, which is wrong here twice over: this tool
				// has no `body` argument, and the Go decoder names internal types
				// (`compose.reportRequest`) an agent can neither read nor act on.
				//
				// The field is `arguments` — what the MCP surface actually calls the
				// object the caller supplied — because the decoder cannot say WHICH
				// of the three plan arguments is misshapen, and naming one would
				// point at an argument that may well be correct. The message carries
				// all three shapes, which is the part a caller acts on.
				return nil, httperr.Validation("arguments", "malformed_json",
					"a plan argument is not the shape this tool takes: `filters` is an object, "+
						"`group_by` an array of strings, `aggregates` an array of {fn, field, as} objects")
			}
		}
		outcome, err := engine.Run(ctx, report, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"report":  outcome.Report,
			"plan":    outcome.Plan,
			"columns": outcome.Columns,
			// Never null: every other list-shaped answer on this surface
			// normalizes, because a model reads null as "unknown" where an
			// empty array says "none matched". reportOutcome.Rows guarantees
			// it, so this is the shape both transports already agree on.
			"rows":         outcome.Rows,
			"total_rows":   len(outcome.Rows),
			"generated_at": outcome.GeneratedAt,
		})
	}
}

// approvalsAdapter maps the tool surface's staging/redemption dependency
// onto the approvals module.
type approvalsAdapter struct{ svc *approvals.Service }

// Stage forwards a refused 🟡 tool call to the approvals engine. It passes
// NO target_version: the engine resolves the pin itself inside the staging
// transaction, for every target type that has a version column to read. This
// adapter used to nil a caller-supplied pin for the types redemption could
// not re-verify — correct as far as it went, but it also meant the pin was
// whatever the caller happened to offer for the types it COULD, which on the
// REST path was an optional request header.
func (a approvalsAdapter) Stage(ctx context.Context, in agents.StageRequest) (ids.ApprovalID, error) {
	return a.svc.Stage(ctx, approvals.StageInput{
		Kind:           in.Tool,
		ProposedChange: in.ProposedChange,
		DiffHash:       in.DiffHash,
		TargetType:     in.TargetType,
		TargetID:       in.TargetID,
		Summary:        in.Summary,
	})
}

func (a approvalsAdapter) Redeem(ctx context.Context, approvalID ids.ApprovalID, tool, diffHash string) (int64, bool, error) {
	return a.svc.Redeem(ctx, approvalID, tool, diffHash)
}
