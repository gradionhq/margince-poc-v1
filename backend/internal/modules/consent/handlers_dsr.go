// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListDataSubjectRequests(w http.ResponseWriter, r *http.Request, params crmcontracts.ListDataSubjectRequestsParams) {
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	status := ""
	if params.Status != nil {
		if !params.Status.Valid() {
			writeConsentErr(w, r, &ValidationError{Field: fieldStatus, Reason: "not a queue state"})
			return
		}
		status = string(*params.Status)
	}
	requests, page, err := h.store.ListDSRs(r.Context(), params.Limit, cursor, status)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	data := make([]crmcontracts.DataSubjectRequest, 0, len(requests))
	for _, d := range requests {
		data = append(data, wireDSR(d))
	}
	info := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		info.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"data": data, "page": info})
}

func (h Handlers) CreateDataSubjectRequest(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateDataSubjectRequestParams) {
	var req crmcontracts.CreateDataSubjectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateDSRInput{
		Kind:       string(req.Kind),
		SubjectRef: req.SubjectRef,
		DueAt:      req.DueAt,
		AssigneeID: idArg[ids.UserKind](req.AssigneeId),
	}
	created, err := h.store.CreateDSR(r.Context(), in)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireDSR(created))
}

func (h Handlers) UpdateDataSubjectRequest(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.UpdateDataSubjectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateDSRInput{Resolution: req.Resolution, AssigneeID: idArg[ids.UserKind](req.AssigneeId)}
	if req.Status != nil {
		status := string(*req.Status)
		in.Status = &status
	}
	// Fulfilling an erasure request EXECUTES the irreversible scrub, so it
	// cannot ride the plain UpdateDSR path: the erase and the status flip must
	// be serialized against every other officer touching this row, or a
	// concurrent reject/fulfil could interleave and leave a subject erased on a
	// request the queue still shows open. FulfilErasure owns that serialization
	// — it locks the request FOR UPDATE and holds the lock across the erase,
	// refuses a subject_ref that names no person, and only then finalizes.
	if in.Status != nil && *in.Status == "fulfilled" {
		current, err := h.store.GetDSR(r.Context(), ids.UUID(id))
		if err != nil {
			writeConsentErr(w, r, err)
			return
		}
		if current.Kind == "erasure" {
			if h.eraser == nil {
				// Fail closed: fulfilling an erasure on a surface with no
				// erase path wired would certify a deletion that never
				// happened.
				writeConsentErr(w, r, errors.New("consent: erasure fulfillment has no erase path wired"))
				return
			}
			updated, err := h.store.FulfilErasure(r.Context(), ids.UUID(id), in, h.eraser.ErasePerson)
			if err != nil {
				writeConsentErr(w, r, err)
				return
			}
			httperr.WriteJSON(w, http.StatusOK, wireDSR(updated))
			return
		}
	}
	updated, err := h.store.UpdateDSR(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireDSR(updated))
}
