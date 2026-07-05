// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) UpdateActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateActivityParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateActivityRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateActivityInput{
		Subject:    req.Subject,
		Body:       req.Body,
		OccurredAt: req.OccurredAt,
		DueAt:      req.DueAt,
		RemindAt:   req.RemindAt,
		IsDone:     req.IsDone,
		IfVersion:  ifVersion,
	}
	if req.AssigneeId != nil {
		assignee := ids.UUID(*req.AssigneeId)
		in.AssigneeID = &assignee
	}
	activity, err := h.store.UpdateActivity(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}

func (h Handlers) ArchiveActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	activity, err := h.store.ArchiveActivity(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}

func (h Handlers) RelinkActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RelinkActivityParams) {
	var req struct {
		EntityType            string   `json:"entity_type"`
		EntityID              ids.UUID `json:"entity_id"`
		ReplaceExistingOfType bool     `json:"replace_existing_of_type"`
	}
	if !httperr.Decode(w, r, &req) {
		return
	}
	activity, err := h.store.RelinkActivity(r.Context(), ids.UUID(id), RelinkActivityInput{
		EntityType:            req.EntityType,
		EntityID:              req.EntityID,
		ReplaceExistingOfType: req.ReplaceExistingOfType,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}
