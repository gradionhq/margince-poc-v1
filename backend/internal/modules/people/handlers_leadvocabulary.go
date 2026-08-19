// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Transport for the two lead vocabularies: sources and disqualification
// reasons. The whole list is the page — an installation holds a handful.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListLeadSources(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListLeadSources(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

func (h Handlers) CreateLeadSource(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateLeadSourceParams) {
	var req crmcontracts.CreateLeadSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateLeadSourceInput{Label: req.Label}
	if req.Key != nil {
		in.Key = *req.Key
	}
	if req.Intent != nil {
		in.Intent = SourceIntent(*req.Intent)
	}
	if req.SortOrder != nil {
		in.SortOrder = *req.SortOrder
	}
	out, err := h.store.CreateLeadSource(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/lead-sources/"+out.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, out)
}

func (h Handlers) UpdateLeadSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateLeadSourceParams) {
	var req crmcontracts.UpdateLeadSourceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateLeadSourceInput{Label: req.Label, SortOrder: req.SortOrder, Active: req.Active}
	if req.Intent != nil {
		intent := SourceIntent(*req.Intent)
		in.Intent = &intent
	}
	out, err := h.store.UpdateLeadSource(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

func (h Handlers) DeleteLeadSource(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.DeleteLeadSource(r.Context(), ids.UUID(id)); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handlers) ListLeadDisqualifyReasons(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListLeadDisqualifyReasons(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LeadDisqualifyReasonListResponse{Data: out})
}

func (h Handlers) CreateLeadDisqualifyReason(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateLeadDisqualifyReasonParams) {
	var req crmcontracts.CreateLeadDisqualifyReasonRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateLeadDisqualifyReasonInput{Label: req.Label}
	if req.SortOrder != nil {
		in.SortOrder = *req.SortOrder
	}
	out, err := h.store.CreateLeadDisqualifyReason(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/lead-disqualify-reasons/"+out.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, out)
}

func (h Handlers) UpdateLeadDisqualifyReason(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateLeadDisqualifyReasonParams) {
	var req crmcontracts.UpdateLeadDisqualifyReasonRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	out, err := h.store.UpdateLeadDisqualifyReason(r.Context(), ids.UUID(id),
		UpdateLeadDisqualifyReasonInput{Label: req.Label, SortOrder: req.SortOrder, Active: req.Active})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

func (h Handlers) DeleteLeadDisqualifyReason(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.DeleteLeadDisqualifyReason(r.Context(), ids.UUID(id)); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
