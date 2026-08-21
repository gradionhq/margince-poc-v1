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
	activity, err := h.store.UpdateActivity(r.Context(), pathID[ids.ActivityKind](id),
		activityUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}

func (h Handlers) ArchiveActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	activity, err := h.store.ArchiveActivity(r.Context(), pathID[ids.ActivityKind](id), nil)
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
	activity, err := h.store.RelinkActivity(r.Context(), pathID[ids.ActivityKind](id), RelinkActivityInput{
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

func (h Handlers) SetActivityAudience(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SetActivityAudienceParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.SetActivityAudienceRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := SetAudienceInput{Audience: string(req.Audience), IfVersion: ifVersion}
	if req.Members != nil {
		for _, m := range *req.Members {
			in.Members = append(in.Members, AudienceMember{SubjectType: string(m.SubjectType), SubjectID: ids.UUID(m.SubjectId)})
		}
	}
	activity, err := h.store.SetAudience(r.Context(), pathID[ids.ActivityKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}
