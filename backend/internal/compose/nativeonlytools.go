// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Native-only tool dependencies, guarded by the workspace's system-of-record
// mode.
//
// Most of the agent surface rides the datasource seam, so the Dispatcher
// already routes it per workspace. Three dependencies cannot: the compiled
// report engine, the retrieval seam behind the context intents, and the
// pipeline-risk lister all query the native domain tables directly.
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
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// sorModeProbe reports whether the acting workspace's system of record is the
// incumbent rather than our own tables. The guards below are reads, so they
// take Dispatcher.isOverlay (cached); a mode read that fails propagates, so an
// unresolved mode refuses the call instead of defaulting to native and
// answering wrongly.
type sorModeProbe func(ctx context.Context) (bool, error)

// nativeOnlyReportRunner guards run_report. The spec names this capability
// as one an incumbent has no analogue for, so the refusal is the declared
// answer, not a degradation.
func nativeOnlyReportRunner(mode sorModeProbe, run agents.ReportRunner) agents.ReportRunner {
	return func(ctx context.Context, report string, planArgs json.RawMessage) (json.RawMessage, error) {
		overlay, err := mode(ctx)
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
func refuseReportInOverlayMode(w http.ResponseWriter, r *http.Request, mode sorModeProbe) bool {
	overlay, err := mode(r.Context())
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
	if refuseReportInOverlayMode(w, r, s.sorDispatch.isOverlay) {
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
	if refuseReportInOverlayMode(w, r, s.sorDispatch.isOverlay) {
		return
	}
	s.reportHandlers.ExplainReport(w, r, report, params)
}

// nativeOnlyRetriever guards catch_me_up_on and prep_for_meeting, whose
// grounding is the full-text index and context graph — neither of which
// holds mirrored content.
type nativeOnlyRetriever struct {
	mode  sorModeProbe
	inner retrieval.Retriever
}

func (r nativeOnlyRetriever) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	overlay, err := r.mode(ctx)
	if err != nil {
		return nil, err
	}
	if overlay {
		return nil, apperrors.ErrUnsupportedBySoR
	}
	return r.inner.Search(ctx, q)
}

func (r nativeOnlyRetriever) AssembleContext(ctx context.Context, anchor datasource.EntityRef, opts retrieval.AssembleOptions) (retrieval.Context, error) {
	overlay, err := r.mode(ctx)
	if err != nil {
		return retrieval.Context{}, err
	}
	if overlay {
		return retrieval.Context{}, apperrors.ErrUnsupportedBySoR
	}
	return r.inner.AssembleContext(ctx, anchor, opts)
}

// nativeOnlySlippingLister guards whats_slipping_this_week, whose candidate
// set is the native deals store. The mirror serves no stage or pipeline
// dial, so there is no overlay query to fall back to.
func nativeOnlySlippingLister(mode sorModeProbe, list agents.SlippingLister) agents.SlippingLister {
	return func(ctx context.Context) ([]agents.SlippingDeal, error) {
		overlay, err := mode(ctx)
		if err != nil {
			return nil, err
		}
		if overlay {
			return nil, apperrors.ErrUnsupportedBySoR
		}
		return list(ctx)
	}
}
