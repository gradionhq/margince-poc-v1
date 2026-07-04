// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the module's transport slice; compose embeds it so the
// generated list/tag stubs are shadowed by real code.
type Handlers struct {
	store *Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

func (h Handlers) ListLists(w http.ResponseWriter, r *http.Request, params crmcontracts.ListListsParams) {
	var entityType *string
	if params.EntityType != nil {
		v := string(*params.EntityType)
		entityType = &v
	}
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	lists, err := h.store.ListLists(r.Context(), entityType, includeArchived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.List, 0, len(lists))
	for _, l := range lists {
		data = append(data, wireList(l))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ListListResponse{Data: data, Page: crmcontracts.PageInfo{}})
}

func (h Handlers) CreateList(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateListRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateListInput{Name: req.Name, EntityType: string(req.EntityType)}
	if req.ListType != nil {
		in.ListType = string(*req.ListType)
	}
	if req.Definition != nil {
		in.Definition = *req.Definition
	}
	if req.OwnerId != nil {
		owner := ids.UUID(*req.OwnerId)
		in.OwnerID = &owner
	}
	if req.TeamId != nil {
		team := ids.UUID(*req.TeamId)
		in.TeamID = &team
	}
	list, err := h.store.CreateList(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireList(list))
}

func (h Handlers) GetList(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	list, err := h.store.GetList(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireList(list))
}

func (h Handlers) ArchiveList(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	list, err := h.store.ArchiveList(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireList(list))
}

func (h Handlers) ListListMembers(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListListMembersParams) {
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	members, page, err := h.store.ListMembers(r.Context(), ids.UUID(id), limit, cursor)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.ListMember, 0, len(members))
	for _, m := range members {
		data = append(data, wireMember(m))
	}
	info := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		info.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ListMemberListResponse{Data: data, Page: info})
}

func (h Handlers) AddListMember(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.AddListMemberRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	member, err := h.store.AddMember(r.Context(), ids.UUID(id), string(req.EntityType), ids.UUID(req.EntityId))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireMember(member))
}

func (h Handlers) ListTags(w http.ResponseWriter, r *http.Request, params crmcontracts.ListTagsParams) {
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	tags, err := h.store.ListTags(r.Context(), includeArchived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Tag, 0, len(tags))
	for _, t := range tags {
		data = append(data, wireTag(t))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.TagListResponse{Data: data, Page: crmcontracts.PageInfo{}})
}

func (h Handlers) CreateTag(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateTagRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	tag, err := h.store.CreateTag(r.Context(), req.Name, req.Color)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireTag(tag))
}

func (h Handlers) ArchiveTag(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	tag, err := h.store.ArchiveTag(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireTag(tag))
}

func (h Handlers) ApplyTag(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.ApplyTagRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	applied, err := h.store.ApplyTag(r.Context(), ids.UUID(id), string(req.EntityType), ids.UUID(req.EntityId))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireTaggable(applied))
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var bad *BadInputError
	if errors.As(err, &bad) {
		httperr.Write(w, r, httperr.Validation(bad.Field, "invalid", bad.Reason))
		return
	}
	httperr.Write(w, r, err)
}
