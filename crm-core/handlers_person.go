package crmcore

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crm-core/internal/store"
	"github.com/gradionhq/margince/backend/kernel/ids"
)

// MergePerson: POST /people/{id}/merge — merge this person (A, the path id)
// into target_id (B, the survivor). Returns the survivor. The store owns
// the collision-aware relinking and the restrictive consent rule; this
// handler is wire-only. 🟡 for agents rides the merge_records tool, not
// this REST path (agents are read-only on REST — C1).
func (h Handlers) MergePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.MergePersonParams) {
	var req crmcontracts.MergePersonJSONBody
	if !decode(w, r, &req) {
		return
	}
	survivor, err := h.store.MergePerson(r.Context(), ids.UUID(id), ids.UUID(req.TargetId))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, survivor)
}

func (h Handlers) ListPeople(w http.ResponseWriter, r *http.Request, params crmcontracts.ListPeopleParams) {
	in := store.ListPeopleInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Query:           params.Q,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	if params.OwnerId != nil {
		owner := ids.UUID(*params.OwnerId)
		in.OwnerID = &owner
	}

	people, page, err := h.store.ListPeople(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, crmcontracts.PersonListResponse{Data: people, Page: pageInfo(page)})
}

func (h Handlers) CreatePerson(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreatePersonParams) {
	var req crmcontracts.CreatePersonRequest
	if !decode(w, r, &req) {
		return
	}
	in, err := personCreateInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	person, err := h.store.CreatePerson(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/people/"+person.Id.String())
	writeJSON(w, http.StatusCreated, person)
}

func (h Handlers) GetPerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	person, err := h.store.GetPerson(r.Context(), ids.UUID(id), true)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}

func (h Handlers) UpdatePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdatePersonParams) {
	ifVersion, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdatePersonRequest
	if !decode(w, r, &req) {
		return
	}

	person, err := h.store.UpdatePerson(r.Context(), ids.UUID(id), personUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}

// ArchivePerson: DELETE = archive, returning the archived entity (200,
// architecture/11 §8 — never a bare 204 for domain rows).
func (h Handlers) ArchivePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	person, err := h.store.ArchivePerson(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, person)
}

func pageInfo(p store.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}
