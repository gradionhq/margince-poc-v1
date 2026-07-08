// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListActivities(w http.ResponseWriter, r *http.Request, params crmcontracts.ListActivitiesParams) {
	in := ListActivitiesInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	if params.Kind != nil {
		k := string(*params.Kind)
		in.Kind = &k
	}
	if params.EntityType != nil && params.EntityId != nil {
		et := string(*params.EntityType)
		// The entity filter targets the polymorphic activity_link seam, so
		// the id stays untyped (rule 6) — the paired entity_type names it.
		id := ids.UUID(*params.EntityId)
		in.EntityType = &et
		in.EntityID = &id
	}

	activities, page, err := h.store.ListActivities(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ActivityListResponse{Data: activities, Page: pageInfo(page)})
}

func (h Handlers) LogActivity(w http.ResponseWriter, r *http.Request, _ crmcontracts.LogActivityParams) {
	var req crmcontracts.CreateActivityRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := activityLogInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	activity, created, err := h.store.LogActivity(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK // idempotent capture replay
	}
	w.Header().Set("Location", "/v1/activities/"+activity.Id.String())
	httperr.WriteJSON(w, status, activity)
}

func (h Handlers) GetActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	activity, err := h.store.GetActivity(r.Context(), pathID[ids.ActivityKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, activity)
}
