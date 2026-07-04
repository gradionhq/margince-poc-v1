package crmcore

import (
	"net/http"

	"github.com/gradionhq/margince/backend/crm-core/internal/store"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListActivities(w http.ResponseWriter, r *http.Request, params crmcontracts.ListActivitiesParams) {
	in := store.ListActivitiesInput{
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
		id := ids.UUID(*params.EntityId)
		in.EntityType = &et
		in.EntityID = &id
	}

	activities, page, err := h.store.ListActivities(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, crmcontracts.ActivityListResponse{Data: activities, Page: pageInfo(page)})
}

func (h Handlers) LogActivity(w http.ResponseWriter, r *http.Request, _ crmcontracts.LogActivityParams) {
	var req crmcontracts.CreateActivityRequest
	if !decode(w, r, &req) {
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
	writeJSON(w, status, activity)
}

func (h Handlers) GetActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	activity, err := h.store.GetActivity(r.Context(), ids.UUID(id), true)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, activity)
}
