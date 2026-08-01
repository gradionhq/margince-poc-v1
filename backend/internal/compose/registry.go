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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// NewRegistry wires the full 🟢/🟡 tool set over the composite provider.
// The admission gate re-derives authority through the shared/ports/authz
// seam, which identity implements — injected here so platform/auth never
// imports a module (ADR-0054 §5).
func NewRegistry(pool *pgxpool.Pool, send SendPath) *agents.Registry {
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, nil, send)
}

// NewRegistryWithIncumbent is NewRegistry plus the per-workspace live-incumbent
// resolver the overlay write-back path (Create/Update/Archive) reaches HubSpot
// through — the wiring a role with a vault (the api server) installs so the MCP
// tool surface can actually write back, not just answer errNoWriteIncumbent.
func NewRegistryWithIncumbent(pool *pgxpool.Pool, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *agents.Registry {
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, resolveIncumbent, send)
}

func registryWithDraftBrain(pool *pgxpool.Pool, brain completer, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *agents.Registry {
	if brain == nil {
		return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), nil, resolveIncumbent, send)
	}
	return registryWithGate(pool, auth.NewGate(identity.NewService(pool)), newReplyDrafter(pool, brain, nil), resolveIncumbent, send)
}

func registryWithGate(pool *pgxpool.Pool, gate *auth.Gate, drafter activities.EmailDrafter, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *agents.Registry {
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
	provider := NewDispatcher(NewProvider(pool), NewOverlayProvider(pool, failClosedOverlayMeter(), resolveIncumbent), pool)
	registry := agents.NewRegistry(approvalsAdapter{svc: approvals.NewService(pool)}, gate)
	// The guards take the Dispatcher as an overlayModeChecker — the interface
	// whose method IS the uncached read, so no wiring here can hand them the
	// cached mode. See overlayModeChecker for why that distinction is typed.
	sorMode := overlayModeChecker(provider)
	agents.RegisterCoreTools(registry, provider, provider, provider, fieldOwnership{pool: pool})
	agents.RegisterReportTool(registry, nativeOnlyReportRunner(sorMode, reportToolRunner(newReportEngine(pool))))
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
		introPathLister(pool), atRiskLister(pool))
	agents.RegisterCommsTools(registry, newCommsAdapter(pool, drafter, send))
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
			if err := json.Unmarshal(planArgs, &req); err != nil {
				return nil, err
			}
		}
		outcome, err := engine.Run(ctx, report, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"report":       outcome.Report,
			"plan":         outcome.Plan,
			"columns":      outcome.Columns,
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
