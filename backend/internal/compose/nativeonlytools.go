// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Native-only tool dependencies, guarded by the workspace's system-of-record
// mode.
//
// Most of the agent surface rides the datasource seam, so the Dispatcher
// already routes it per workspace. Four dependencies cannot: the compiled
// report engine, the retrieval seam behind the context intents, the
// pipeline-risk lister and the query-plan executor all query the native domain
// tables directly.
//
// Reports have a seam verb, but not this one: RunReport carries an ad-hoc
// ReportPlan, while the tool names a prebuilt report key and answers with
// plan and derivation metadata that has no seam shape. The context intents
// and the pipeline scan have no verb at all, and the mirror holds no
// context-graph or pipeline projection for them to read.
//
// Handed an overlay workspace, those engines would run against native tables
// holding none of its records and return a well-formed empty answer — a
// silent break, which ADR-0018's bounded-capability guarantee forbids
// outright: a tool either behaves identically across modes or returns a
// DECLARED unsupported-by-SoR result (AC-OV-2). "No deals are slipping" is a
// worse failure than "this is not available here", because only one of them
// is visibly wrong. So the composition layer wraps each dependency here,
// where cross-module edges belong, and the tools stay mode-unaware.

import (
	"context"
	"encoding/json"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// The guards below take overlayModeChecker (overlaywrite.go), so the cached
// read cannot be handed to one. A mode read that fails propagates, so an
// unresolved mode refuses the call rather than defaulting to native.

// nativeOnlyReportRunner guards run_report. The spec names this capability
// as one an incumbent has no analogue for, so the refusal is the declared
// answer, not a degradation.
func nativeOnlyReportRunner(mode overlayModeChecker, run agents.ReportRunner) agents.ReportRunner {
	return func(ctx context.Context, report string, planArgs json.RawMessage) (json.RawMessage, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return nil, err
		}
		if overlay {
			return nil, apperrors.ErrUnsupportedBySoR
		}
		return run(ctx, report, planArgs)
	}
}

// refuseReportInOverlayMode is the REST half, shared by both report
// operations. It reports whether it answered the request, so a caller runs
// the native engine only when the workspace actually has one.
//
// The answer is the ErrUnsupportedBySoR sentinel, not a validation error:
// run_report is an L1 MCP tool, and the contract binds every one of them to
// pass identically or return a declared `unsupported_by_sor` (422). A
// validation error would tell the caller their input was wrong when the
// request was fine and the capability simply is not there — and it would
// answer a different machine code than the same verb's tool half. The
// `unsupported_in_overlay_mode` spelling next door is for refused query
// DIALS, which genuinely are input.
func refuseReportInOverlayMode(w http.ResponseWriter, r *http.Request, mode overlayModeChecker) bool {
	overlay, err := mode.isOverlayUncached(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return true
	}
	if !overlay {
		return false
	}
	httperr.Write(w, r, apperrors.ErrUnsupportedBySoR)
	return true
}

// RunReport shadows the embedded reportHandlers so the mode guard runs
// before the native engine ever sees the request.
func (s Server) RunReport(w http.ResponseWriter, r *http.Request, report string) {
	if refuseReportInOverlayMode(w, r, s.sorDispatch) {
		return
	}
	s.reportHandlers.RunReport(w, r, report)
}

// ExplainReport shadows the drill-through sibling. It runs the same native
// engine and returns the SOURCE ROWS behind one aggregate, so leaving it
// unguarded would hand an overlay workspace the identical well-formed empty
// answer one route over — and the whole argument above is that a hidden
// screen is not a server-side gate.
func (s Server) ExplainReport(w http.ResponseWriter, r *http.Request, report string, params crmcontracts.ExplainReportParams) {
	if refuseReportInOverlayMode(w, r, s.sorDispatch) {
		return
	}
	s.reportHandlers.ExplainReport(w, r, report, params)
}

// nativeOnlyRetriever guards catch_me_up_on and prep_for_meeting, whose
// grounding is the full-text index and context graph — neither of which
// holds mirrored content.
type nativeOnlyRetriever struct {
	mode  overlayModeChecker
	inner retrieval.Retriever
}

func (r nativeOnlyRetriever) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	overlay, err := r.mode.isOverlayUncached(ctx)
	if err != nil {
		return nil, err
	}
	if overlay {
		return nil, apperrors.ErrUnsupportedBySoR
	}
	return r.inner.Search(ctx, q)
}

func (r nativeOnlyRetriever) AssembleContext(ctx context.Context, anchor datasource.EntityRef, opts retrieval.AssembleOptions) (retrieval.Context, error) {
	overlay, err := r.mode.isOverlayUncached(ctx)
	if err != nil {
		return retrieval.Context{}, err
	}
	if overlay {
		return retrieval.Context{}, apperrors.ErrUnsupportedBySoR
	}
	return r.inner.AssembleContext(ctx, anchor, opts)
}

// nativeOnlyIntroPath guards intro_path_to. It grounds on the interaction
// projection and the native employment table, neither of which the incumbent
// mirror holds — so in overlay mode it would answer "nobody here can get you in",
// which is a believable answer rather than a visible failure.
func nativeOnlyIntroPath(mode overlayModeChecker, list agents.IntroPathLister) agents.IntroPathLister {
	return func(ctx context.Context, orgID ids.UUID) ([]agents.IntroRoute, bool, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return nil, false, err
		}
		if overlay {
			return nil, false, apperrors.ErrUnsupportedBySoR
		}
		return list(ctx, orgID)
	}
}

// nativeOnlyAtRisk guards at_risk_relationships, over the caller's own native
// deal rows and the same interaction projection. Unguarded it would answer
// "nothing is at risk" for a workspace whose deals it cannot see.
func nativeOnlyAtRisk(mode overlayModeChecker, list agents.AtRiskLister) agents.AtRiskLister {
	return func(ctx context.Context) (agents.AtRiskReport, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return agents.AtRiskReport{}, err
		}
		if overlay {
			return agents.AtRiskReport{}, apperrors.ErrUnsupportedBySoR
		}
		return list(ctx)
	}
}

// nativeOnlyPipelines guards list_pipelines. The mirror holds no pipeline
// projection — an incumbent's pipelines and stages are never mirrored — so the
// native tables hold none of an overlay workspace's configuration and the
// unguarded answer would be "this workspace has no pipelines". A caller
// believing that concludes the deal verbs are unusable, when what is true is
// that this capability is not served in overlay mode.
func nativeOnlyPipelines(mode overlayModeChecker, list agents.PipelineLister) agents.PipelineLister {
	return func(ctx context.Context) ([]agents.Pipeline, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return nil, err
		}
		if overlay {
			return nil, apperrors.ErrUnsupportedBySoR
		}
		return list(ctx)
	}
}

// nativeOnlySlippingLister guards whats_slipping_this_week AND
// draft_follow_ups_for — both, because RegisterSlippingTools hands this one
// lister to each of them, so the drafter's candidate set is the same guarded
// read. The candidates come from the native deals store, and the mirror serves no
// stage or pipeline dial, so there is no overlay query to fall back to.
func nativeOnlySlippingLister(mode overlayModeChecker, list agents.SlippingLister) agents.SlippingLister {
	return func(ctx context.Context) ([]agents.SlippingDeal, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return nil, err
		}
		if overlay {
			return nil, apperrors.ErrUnsupportedBySoR
		}
		return list(ctx)
	}
}

// nativeOnlyDisqualifier guards disqualify_lead, and it is the tool half of a
// refusal REST already makes.
//
// The middleware in overlaywrite.go refuses this verb for every principal on
// the REST surface: `disqualify_lead` is a record write (overlayRecordWriteTools)
// against a mirrored type (`lead`), and it has no overlayWriteVerbs entry, so
// the provider cannot serve it and the native `lead` table is empty in overlay
// mode. The tool reaches the people store directly — the same entry point the
// route calls, which is the point — so nothing on that path passes the
// middleware, and without this the tool would commit to the empty native table
// while the route refused. A tool and its route are two transports onto one
// behaviour or they are a silent divergence (ADR-0018/AC-OV-2).
//
// TestEveryUnservableRecordWriteVerbRefusesOnItsToolPath derives which verbs
// need this, so a lifecycle seam added for another such verb fails the gate
// rather than shipping unguarded.
func nativeOnlyDisqualifier(mode overlayModeChecker, disqualifier agents.LeadDisqualifier) disqualifierGuard {
	return disqualifierGuard{mode: mode, inner: disqualifier}
}

type disqualifierGuard struct {
	mode  overlayModeChecker
	inner agents.LeadDisqualifier
}

func (g disqualifierGuard) DisqualifyLead(ctx context.Context, id ids.UUID) (json.RawMessage, error) {
	overlay, err := g.mode.isOverlayUncached(ctx)
	if err != nil {
		return nil, err
	}
	if overlay {
		return nil, apperrors.ErrUnsupportedBySoR
	}
	return g.inner.DisqualifyLead(ctx, id)
}

// nativeOnlyQueryRunner guards query_workspace, for the reason every other
// guard in nativeonlytools.go exists: the executor reads the native domain
// tables directly, and an overlay workspace's records are not in them. An
// answer assembled from tables holding none of this workspace's data is a
// well-formed empty result — visibly right and actually wrong.
func nativeOnlyQueryRunner(mode overlayModeChecker, run agents.QueryRunner) agents.QueryRunner {
	return func(ctx context.Context, plan json.RawMessage) (agents.QueryAnswer, error) {
		overlay, err := mode.isOverlayUncached(ctx)
		if err != nil {
			return agents.QueryAnswer{}, err
		}
		if overlay {
			return agents.QueryAnswer{}, apperrors.ErrUnsupportedBySoR
		}
		return run(ctx, plan)
	}
}
