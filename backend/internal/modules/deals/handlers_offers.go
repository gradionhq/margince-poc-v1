// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer transport surface (B-E03.20). One wire rule enforced HERE,
// before any store code runs: money totals are derived, never accepted —
// a body carrying any total spelling answers 422 totals_derived
// (B-E03.18), whatever else it says.

package deals

import (
	"encoding/json"
	"io"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// clientTotalKeys are every accepted-nowhere spelling of a money total.
// The x-extension request shapes surface unknown keys in
// AdditionalProperties; the plain line-item shapes decode alongside
// clientSuppliedTotals below. Both paths check against this one list.
// maxOfferBody bounds the hand-read create body; far above any real
// offer, far below abuse.
const maxOfferBody = 1 << 20

var clientTotalKeys = []string{
	"line_total", "line_total_minor", "line_net", "line_net_minor",
	"line_tax", "line_tax_minor", "net_minor", "tax_minor", "gross_minor", "total_minor",
}

// rejectClientTotals answers 422 when an extension-carrying request body
// smuggles a total; returns false when the response has been written.
func rejectClientTotals(w http.ResponseWriter, r *http.Request, extra map[string]interface{}) bool {
	for _, key := range clientTotalKeys {
		if _, found := extra[key]; found {
			httperr.Write(w, r, httperr.Validation(key, "totals_derived",
				"money totals are computed server-side from the line items and cannot be supplied"))
			return false
		}
	}
	return true
}

// clientSuppliedTotals rides embedded in the plain (non-extension)
// line-item decode targets so a smuggled total is SEEN instead of
// silently dropped by encoding/json.
type clientSuppliedTotals struct {
	LineTotal      *int64 `json:"line_total"`
	LineTotalMinor *int64 `json:"line_total_minor"`
	LineNet        *int64 `json:"line_net"`
	LineNetMinor   *int64 `json:"line_net_minor"`
	LineTax        *int64 `json:"line_tax"`
	LineTaxMinor   *int64 `json:"line_tax_minor"`
	NetMinor       *int64 `json:"net_minor"`
	TaxMinor       *int64 `json:"tax_minor"`
	GrossMinor     *int64 `json:"gross_minor"`
	TotalMinor     *int64 `json:"total_minor"`
}

func (c clientSuppliedTotals) reject(w http.ResponseWriter, r *http.Request) bool {
	for key, v := range map[string]*int64{
		"line_total": c.LineTotal, "line_total_minor": c.LineTotalMinor,
		"line_net": c.LineNet, "line_net_minor": c.LineNetMinor,
		"line_tax": c.LineTax, "line_tax_minor": c.LineTaxMinor,
		"net_minor": c.NetMinor, "tax_minor": c.TaxMinor,
		"gross_minor": c.GrossMinor, "total_minor": c.TotalMinor,
	} {
		if v != nil {
			httperr.Write(w, r, httperr.Validation(key, "totals_derived",
				"money totals are computed server-side from the line items and cannot be supplied"))
			return false
		}
	}
	return true
}

func lineInputRow(in crmcontracts.OfferLineItemInput) OfferLineInputRow {
	row := OfferLineInputRow{
		Position:       in.Position,
		ProductID:      idArg[ids.ProductKind](in.ProductId),
		Description:    in.Description,
		Unit:           in.Unit,
		Quantity:       formatQuantity(in.Quantity),
		UnitPriceMinor: in.UnitPriceMinor,
	}
	if in.DiscountPct != nil {
		v := formatPct(*in.DiscountPct)
		row.DiscountPct = &v
	}
	if in.TaxRate != nil {
		v := formatPct(*in.TaxRate)
		row.TaxRate = &v
	}
	return row
}

func (h Handlers) ListDealOffers(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListDealOffersParams) {
	in := ListDealOffersInput{Cursor: params.Cursor, Limit: params.Limit}
	if params.Status != nil {
		s := string(*params.Status)
		in.Status = &s
	}
	offers, page, err := h.store.ListDealOffers(r.Context(), pathID[ids.DealKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.OfferListResponse{Data: offers, Page: pageInfo(page)})
}

func (h Handlers) CreateOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.CreateOfferParams) {
	// Two decodes over one body: the generated shape (whose custom
	// UnmarshalJSON owns the extension map — embedding it would shadow
	// any sibling field), then a probe over the nested line items, whose
	// decode target has no extension map and would otherwise drop a
	// smuggled per-line total silently.
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxOfferBody))
	if err != nil {
		httperr.Write(w, r, httperr.Validation("body", "unreadable", "request body could not be read"))
		return
	}
	var req crmcontracts.CreateOfferRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}
	if !rejectClientTotals(w, r, req.AdditionalProperties) {
		return
	}
	var probe struct {
		LineItems []clientSuppliedTotals `json:"line_items"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}
	for _, li := range probe.LineItems {
		if !li.reject(w, r) {
			return
		}
	}
	in := CreateOfferInput{
		Currency:   req.Currency,
		BuyerOrgID: idArg[ids.OrganizationKind](req.BuyerOrgId),
		IntroText:  req.IntroText,
		TermsText:  req.TermsText,
		Source:     req.Source,
	}
	if req.ValidUntil != nil {
		v := req.ValidUntil.Format("2006-01-02")
		in.ValidUntil = &v
	}
	if req.LineItems != nil {
		for _, li := range *req.LineItems {
			in.LineItems = append(in.LineItems, lineInputRow(li))
		}
	}

	offer, err := h.store.CreateOffer(r.Context(), pathID[ids.DealKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/offers/"+offer.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, offer)
}

func (h Handlers) GetOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	offer, err := h.store.GetOffer(r.Context(), pathID[ids.OfferKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) UpdateOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateOfferParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateOfferRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if !rejectClientTotals(w, r, req.AdditionalProperties) {
		return
	}
	in := UpdateOfferInput{
		Currency:   req.Currency,
		BuyerOrgID: idArg[ids.OrganizationKind](req.BuyerOrgId),
		IntroText:  req.IntroText,
		TermsText:  req.TermsText,
		IfVersion:  ifVersion,
	}
	if req.ValidUntil != nil {
		v := req.ValidUntil.Format("2006-01-02")
		in.ValidUntil = &v
	}

	offer, err := h.store.UpdateOffer(r.Context(), pathID[ids.OfferKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) ArchiveOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	offer, err := h.store.ArchiveOffer(r.Context(), pathID[ids.OfferKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) AddOfferLineItem(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req struct {
		crmcontracts.OfferLineItemInput
		clientSuppliedTotals
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	if !req.reject(w, r) {
		return
	}
	offer, err := h.store.AddOfferLineItem(r.Context(), pathID[ids.OfferKind](id), lineInputRow(req.OfferLineItemInput))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, offer)
}

func (h Handlers) UpdateOfferLineItem(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, lineItemId openapi_types.UUID) {
	var req struct {
		crmcontracts.UpdateOfferLineItemRequest
		clientSuppliedTotals
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	if !req.reject(w, r) {
		return
	}
	in := UpdateOfferLineInput{
		Position:       req.Position,
		Description:    req.Description,
		Unit:           req.Unit,
		UnitPriceMinor: req.UnitPriceMinor,
	}
	if req.Quantity != nil {
		v := formatQuantity(*req.Quantity)
		in.Quantity = &v
	}
	if req.DiscountPct != nil {
		v := formatPct(*req.DiscountPct)
		in.DiscountPct = &v
	}
	if req.TaxRate != nil {
		v := formatPct(*req.TaxRate)
		in.TaxRate = &v
	}
	offer, err := h.store.UpdateOfferLineItem(r.Context(), pathID[ids.OfferKind](id), ids.UUID(lineItemId), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) RemoveOfferLineItem(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, lineItemId openapi_types.UUID) {
	offer, err := h.store.RemoveOfferLineItem(r.Context(), pathID[ids.OfferKind](id), ids.UUID(lineItemId))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) SendOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SendOfferParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	offer, err := h.store.SendOffer(r.Context(), pathID[ids.OfferKind](id), ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) AcceptOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.AcceptOfferParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	offer, err := h.store.AcceptOffer(r.Context(), pathID[ids.OfferKind](id), ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) RejectOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RejectOfferParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.RejectOfferRequest
	if r.ContentLength > 0 && !httperr.Decode(w, r, &req) {
		return
	}
	offer, err := h.store.RejectOffer(r.Context(), pathID[ids.OfferKind](id), req.Reason, ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, offer)
}

func (h Handlers) RegenerateOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RegenerateOfferParams) {
	offer, err := h.store.RegenerateOffer(r.Context(), pathID[ids.OfferKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/offers/"+offer.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, offer)
}
