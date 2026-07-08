// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// pathID asserts a contract path id as entity K's id — the widening
// point between the wire and the typed store surface (the route already
// names the entity, so the assertion lives here, not in the store).
func pathID[K ids.EntityKind](id crmcontracts.Id) ids.ID[K] {
	return ids.From[K](ids.UUID(id))
}

// idArg asserts an optional wire UUID (a body field) as entity K's id;
// nil stays nil.
func idArg[K ids.EntityKind](u *openapi_types.UUID) *ids.ID[K] {
	if u == nil {
		return nil
	}
	v := ids.From[K](ids.UUID(*u))
	return &v
}

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
	archived := storekit.LiveOnly
	if params.IncludeArchived != nil && *params.IncludeArchived {
		archived = storekit.IncludeArchived
	}
	lists, truncated, err := h.store.ListLists(r.Context(), entityType, archived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.List, 0, len(lists))
	for _, l := range lists {
		data = append(data, wireList(l))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ListListResponse{Data: data, Page: crmcontracts.PageInfo{HasMore: truncated}})
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
	in.OwnerID = idArg[ids.UserKind](req.OwnerId)
	in.TeamID = idArg[ids.TeamKind](req.TeamId)
	list, err := h.store.CreateList(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireList(list))
}

func (h Handlers) GetList(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	list, err := h.store.GetList(r.Context(), pathID[ids.ListKind](id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireList(list))
}

func (h Handlers) ArchiveList(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	list, err := h.store.ArchiveList(r.Context(), pathID[ids.ListKind](id))
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
	members, page, err := h.store.ListMembers(r.Context(), pathID[ids.ListKind](id), limit, cursor)
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
	// req.EntityId is a polymorphic member target (any entity), so it stays
	// an untyped ids.UUID; the store validates it against the list's own
	// entity_type and the row-scope link gate.
	member, err := h.store.AddMember(r.Context(), pathID[ids.ListKind](id), string(req.EntityType), ids.UUID(req.EntityId))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireMember(member))
}

func (h Handlers) ListTags(w http.ResponseWriter, r *http.Request, params crmcontracts.ListTagsParams) {
	archived := storekit.LiveOnly
	if params.IncludeArchived != nil && *params.IncludeArchived {
		archived = storekit.IncludeArchived
	}
	tags, truncated, err := h.store.ListTags(r.Context(), archived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Tag, 0, len(tags))
	for _, t := range tags {
		data = append(data, wireTag(t))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.TagListResponse{Data: data, Page: crmcontracts.PageInfo{HasMore: truncated}})
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
	tag, err := h.store.ArchiveTag(r.Context(), pathID[ids.TagKind](id))
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
	// req.EntityId is a polymorphic tag target (any entity), so it stays an
	// untyped ids.UUID; the store row-scope-gates it as a link target.
	applied, err := h.store.ApplyTag(r.Context(), pathID[ids.TagKind](id), string(req.EntityType), ids.UUID(req.EntityId))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireTaggable(applied))
}

func (h Handlers) ListSavedViews(w http.ResponseWriter, r *http.Request, params crmcontracts.ListSavedViewsParams) {
	var resource *string
	if params.Resource != nil {
		v := string(*params.Resource)
		resource = &v
	}
	archived := storekit.LiveOnly
	if params.IncludeArchived != nil && *params.IncludeArchived {
		archived = storekit.IncludeArchived
	}
	views, truncated, err := h.store.ListSavedViews(r.Context(), resource, archived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.SavedView, 0, len(views))
	for _, v := range views {
		data = append(data, wireSavedView(v))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SavedViewListResponse{Data: data, Page: crmcontracts.PageInfo{HasMore: truncated}})
}

func (h Handlers) CreateSavedView(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateSavedViewRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	view, err := h.store.CreateSavedView(r.Context(), CreateSavedViewInput{
		Resource: string(req.Resource),
		Name:     req.Name,
		Query:    req.Query,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireSavedView(view))
}

func (h Handlers) GetSavedView(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	view, err := h.store.GetSavedView(r.Context(), pathID[ids.SavedViewKind](id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSavedView(view))
}

func (h Handlers) UpdateSavedView(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateSavedViewParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateSavedViewRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateSavedViewInput{Name: req.Name, IfVersion: ifVersion}
	if req.Query != nil {
		q := *req.Query
		in.Query = &q
	}
	view, err := h.store.UpdateSavedView(r.Context(), pathID[ids.SavedViewKind](id), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSavedView(view))
}

func (h Handlers) ArchiveSavedView(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	view, err := h.store.ArchiveSavedView(r.Context(), pathID[ids.SavedViewKind](id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSavedView(view))
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var bad *BadInputError
	if errors.As(err, &bad) {
		httperr.Write(w, r, httperr.Validation(bad.Field, "invalid", bad.Reason))
		return
	}
	// A rejected dynamic-segment / saved-view filter surfaces the offending
	// field and machine-readable code (data-model §13.5 → 422).
	var pred *storekit.PredicateError
	if errors.As(err, &pred) {
		httperr.Write(w, r, httperr.Validation(pred.Field, pred.Code, pred.Message))
		return
	}
	httperr.Write(w, r, err)
}
