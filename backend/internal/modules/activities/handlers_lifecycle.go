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

// ArchiveActivity retires one activity, honouring If-Match where the caller
// named a version — including the one an approval's release forwards.
func (h Handlers) ArchiveActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ArchiveActivityParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	activity, err := h.store.ArchiveActivity(r.Context(), pathID[ids.ActivityKind](id), ifVersion)
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

// RelinkThread moves every writable activity of one conversation in one
// transaction; the count and the ids are the answer.
func (h Handlers) RelinkThread(w http.ResponseWriter, r *http.Request, _ crmcontracts.RelinkThreadParams) {
	var req crmcontracts.RelinkThreadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	out, err := h.store.RelinkThread(r.Context(), req.ThreadKey, RelinkActivityInput{
		EntityType:            string(req.EntityType),
		EntityID:              ids.UUID(req.EntityId),
		ReplaceExistingOfType: req.ReplaceExistingOfType != nil && *req.ReplaceExistingOfType,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, relinkBatchWire(out))
}

// RelinkActivities moves a named set of activities in one transaction, or
// none of them.
func (h Handlers) RelinkActivities(w http.ResponseWriter, r *http.Request, _ crmcontracts.RelinkActivitiesParams) {
	var req crmcontracts.RelinkActivitiesRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	activityIDs := make([]ids.UUID, 0, len(req.ActivityIds))
	for _, id := range req.ActivityIds {
		activityIDs = append(activityIDs, ids.UUID(id))
	}
	out, err := h.store.RelinkActivities(r.Context(), activityIDs, RelinkActivityInput{
		EntityType:            string(req.EntityType),
		EntityID:              ids.UUID(req.EntityId),
		ReplaceExistingOfType: req.ReplaceExistingOfType != nil && *req.ReplaceExistingOfType,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, relinkBatchWire(out))
}

// relinkBatchWire projects the store's answer onto the contract shape.
func relinkBatchWire(out RelinkBatchResult) crmcontracts.RelinkBatchResult {
	return crmcontracts.RelinkBatchResult{Relinked: out.Relinked}
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
