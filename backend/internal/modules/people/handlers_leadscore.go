// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The Explain-This-Score surface (AC-S7, ADR-0105/A156): read the
// decomposition behind a lead's score, and enter or withdraw the human
// inputs that feed it.

import (
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// explainScoreHistoryLimit bounds one page of the series. The read filters
// every entry's sources through the caller's scope, so an unbounded page
// is unbounded work as well as an unbounded response.
const explainScoreHistoryLimit = 50

// ExplainLeadScore serves GET /leads/{id}/score — the factor
// decomposition behind the lead's number.
func (h Handlers) ExplainLeadScore(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ExplainLeadScoreParams) {
	in := ExplainLeadScoreInput{Limit: explainScoreHistoryLimit}
	if params.History != nil {
		in.History = *params.History
	}
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	if params.Limit != nil && *params.Limit > 0 && *params.Limit <= explainScoreHistoryLimit {
		in.Limit = *params.Limit
	}
	out, err := h.store.ExplainLeadScore(r.Context(), pathID[ids.LeadKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// SetLeadManualSignal serves PUT /leads/{id}/manual-signals, where a rep
// supplies what capture cannot fetch.
func (h Handlers) SetLeadManualSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.SetLeadManualSignalRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// A reason is what separates a scoring input from a number somebody
	// typed. The column enforces it too; refusing here names the field.
	if strings.TrimSpace(req.Reason) == "" {
		httperr.Write(w, r, httperr.Validation(fieldKeyReason, "reason_required",
			"say why this value is right — a scoring input nobody can account for is worse than none"))
		return
	}
	out, err := h.store.SetLeadManualSignal(r.Context(), pathID[ids.LeadKind](id), SetLeadManualSignalInput{
		Factor:     string(req.Factor),
		Band:       req.Band,
		SignalKind: string(req.SignalKind),
		Confidence: req.Confidence,
		Reason:     req.Reason,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// ClearLeadManualSignal serves DELETE
// /leads/{id}/manual-signals/{factor}.
func (h Handlers) ClearLeadManualSignal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, factor string) {
	if err := h.store.ClearLeadManualSignal(r.Context(), pathID[ids.LeadKind](id), factor); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
