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
	requests, page, err := h.store.ListDSRs(r.Context(), params.Limit, cursor)
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
	}
	if req.AssigneeId != nil {
		assignee := ids.UUID(*req.AssigneeId)
		in.AssigneeID = &assignee
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
	in := UpdateDSRInput{Resolution: req.Resolution}
	if req.Status != nil {
		status := string(*req.Status)
		in.Status = &status
	}
	if req.AssigneeId != nil {
		assignee := ids.UUID(*req.AssigneeId)
		in.AssigneeID = &assignee
	}
	// Fulfilling an erasure request EXECUTES the erasure first — the
	// status flip and the actual deletion must not drift apart. A
	// subject_ref that is not a person id (or a person already gone —
	// e.g. an earlier erasure) names nothing left to erase and the
	// request just closes.
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
			if personID, parseErr := ids.Parse(current.SubjectRef); parseErr == nil {
				err := h.eraser.ErasePerson(r.Context(), personID, "dsr:"+current.ID.String())
				if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
					writeConsentErr(w, r, err)
					return
				}
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
