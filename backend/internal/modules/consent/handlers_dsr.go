// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	// Fulfilling an erasure request EXECUTES the erasure before UpdateDSR
	// ever runs, so every precondition UpdateDSR would enforce must already
	// hold here — the status flip and the actual deletion must not drift
	// apart, and nothing may be erased on a request that UpdateDSR is going
	// to refuse to close.
	if in.Status != nil && *in.Status == "fulfilled" {
		current, err := h.store.GetDSR(r.Context(), ids.UUID(id))
		if err != nil {
			writeConsentErr(w, r, err)
			return
		}
		if verr := validateDSRUpdate(current, in); verr != nil {
			writeConsentErr(w, r, verr)
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
			personID, parseErr := ids.Parse(current.SubjectRef)
			if parseErr != nil {
				// Same fail-closed rule: a subject_ref that names no person
				// means we never looked, not that there was nothing to
				// erase. Closing here would certify a deletion that never
				// ran. (An already-erased person is the ErrNotFound case
				// below — that one genuinely has nothing left to do.)
				writeConsentErr(w, r, &ValidationError{
					Field:  fieldSubjectRef,
					Reason: "an erasure request must name a person id before it can be fulfilled",
				})
				return
			}
			err := h.eraser.ErasePerson(r.Context(), personID, "dsr:"+current.ID.String())
			if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
				writeConsentErr(w, r, err)
				return
			}
		}
	}
	updated, err := h.store.UpdateDSR(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireDSR(updated))
}
