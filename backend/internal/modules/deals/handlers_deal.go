// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListDeals(w http.ResponseWriter, r *http.Request, params crmcontracts.ListDealsParams) {
	in := ListDealsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	if params.PipelineId != nil {
		v := ids.UUID(*params.PipelineId)
		in.PipelineID = &v
	}
	if params.StageId != nil {
		v := ids.UUID(*params.StageId)
		in.StageID = &v
	}
	if params.OwnerId != nil {
		v := ids.UUID(*params.OwnerId)
		in.OwnerID = &v
	}
	if params.OrganizationId != nil {
		v := ids.UUID(*params.OrganizationId)
		in.OrganizationID = &v
	}
	in.Stalled = params.Stalled
	if params.Status != nil {
		s := string(*params.Status)
		in.Status = &s
	}

	deals, page, err := h.store.ListDeals(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealListResponse{Data: deals, Page: pageInfo(page)})
}

func (h Handlers) CreateDeal(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateDealParams) {
	var req crmcontracts.CreateDealRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if (req.AmountMinor == nil) != (req.Currency == nil) {
		httperr.Write(w, r, httperr.Validation("currency", "amount_currency_pair", "amount_minor and currency come together or not at all"))
		return
	}
	in, err := dealCreateInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	deal, err := h.store.CreateDeal(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/deals/"+deal.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, deal)
}

func (h Handlers) GetDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	deal, err := h.store.GetDeal(r.Context(), ids.UUID(id), true)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, deal)
}

func (h Handlers) UpdateDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateDealParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateDealRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	deal, err := h.store.UpdateDeal(r.Context(), ids.UUID(id), dealUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, deal)
}

// AdvanceDeal is the stage-move verb. Won/lost derives from the target
// stage's semantic server-side; the request's optional status field is
// advisory and never trusted over the pipeline configuration.
func (h Handlers) AdvanceDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.AdvanceDealParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.AdvanceDealRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	deal, err := h.store.AdvanceDeal(r.Context(), ids.UUID(id), AdvanceDealInput{
		ToStageID:  ids.UUID(req.ToStageId),
		LostReason: req.LostReason,
		IfVersion:  ifVersion,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, deal)
}

func (h Handlers) ArchiveDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	deal, err := h.store.ArchiveDeal(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, deal)
}
