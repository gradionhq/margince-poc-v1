// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// MergePerson: POST /people/{id}/merge — merge this person (A, the path id)
// into target_id (B, the survivor). Returns the survivor. The store owns
// the collision-aware relinking and the restrictive consent rule; this
// handler is wire-only. Agent 🟡 governance is applied by the ADR-0055
// admission gate that wraps this route (same staging as the merge_records
// tool), not by this handler.
func (h Handlers) MergePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.MergePersonParams) {
	var req crmcontracts.MergePersonJSONBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	survivor, err := h.store.MergePerson(r.Context(), ids.UUID(id), ids.UUID(req.TargetId))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, survivor)
}

func (h Handlers) ListPeople(w http.ResponseWriter, r *http.Request, params crmcontracts.ListPeopleParams) {
	in := ListPeopleInput{
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
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.PersonListResponse{Data: people, Page: pageInfo(page)})
}

func (h Handlers) CreatePerson(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreatePersonParams) {
	var req crmcontracts.CreatePersonRequest
	if !httperr.Decode(w, r, &req) {
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
	httperr.WriteJSON(w, http.StatusCreated, person)
}

func (h Handlers) GetPerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	person, err := h.store.GetPerson(r.Context(), ids.UUID(id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, person)
}

func (h Handlers) UpdatePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdatePersonParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdatePersonRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	person, err := h.store.UpdatePerson(r.Context(), ids.UUID(id), personUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, person)
}

// ArchivePerson: DELETE = archive, returning the archived entity (200,
// architecture/11 §8 — never a bare 204 for domain rows).
func (h Handlers) ArchivePerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	person, err := h.store.ArchivePerson(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, person)
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}
