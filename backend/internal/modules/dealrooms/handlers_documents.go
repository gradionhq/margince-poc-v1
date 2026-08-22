// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"maps"
	"net/http"
	"slices"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// ListDealRoomDocuments returns what the room puts in front of its buyer.
func (h Handlers) ListDealRoomDocuments(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	docs, page, err := h.store.ListDocuments(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomDocumentListResponse{
		Data: docs,
		Page: pageInfo(page),
	})
}

// AddDealRoomDocument puts an attachment of the deal in front of the buyer.
func (h Handlers) AddDealRoomDocument(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.AddDealRoomDocumentRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := addDocumentInput(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	doc, err := h.store.AddDocument(r.Context(), pathID(id), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, doc)
}

// UpdateDealRoomDocument renames, regroups or reorders one document.
func (h Handlers) UpdateDealRoomDocument(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, documentID openapi_types.UUID, _ crmcontracts.UpdateDealRoomDocumentParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateDealRoomDocumentRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := updateDocumentInput(req, ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	doc, err := h.store.UpdateDocument(r.Context(), pathID(id), documentIDOf(documentID), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, doc)
}

// RemoveDealRoomDocument takes a document out of the room.
func (h Handlers) RemoveDealRoomDocument(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, documentID openapi_types.UUID, _ crmcontracts.RemoveDealRoomDocumentParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	doc, err := h.store.RemoveDocument(r.Context(), pathID(id), documentIDOf(documentID), ifVersion)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, doc)
}

// addDocumentInput validates the add. The group is checked here, before the
// schema CHECK would refuse it as a 500 naming a constraint.
func addDocumentInput(req crmcontracts.AddDealRoomDocumentRequest) (AddDocumentInput, error) {
	if err := provenance.Refuse("source", req.Source); err != nil {
		return AddDocumentInput{}, err
	}
	// An omitted attachment_id decodes to the zero UUID with no error and would
	// otherwise be reported as a file that does not exist.
	if err := httperr.RequireBodyID(fieldAttachmentID, ids.UUID(req.AttachmentId)); err != nil {
		return AddDocumentInput{}, err
	}
	if err := refuseUnknownGroup(string(req.GroupKey)); err != nil {
		return AddDocumentInput{}, err
	}
	in := AddDocumentInput{
		AttachmentID: ids.UUID(req.AttachmentId),
		GroupKey:     string(req.GroupKey),
		Source:       req.Source,
	}
	if req.Title != nil {
		title, err := cleanTitle(*req.Title)
		if err != nil {
			return AddDocumentInput{}, err
		}
		in.Title = title
	}
	if req.Position != nil {
		in.Position = *req.Position
	}
	return in, nil
}

func updateDocumentInput(req crmcontracts.UpdateDealRoomDocumentRequest, ifVersion *int64) (UpdateDocumentInput, error) {
	in := UpdateDocumentInput{Position: req.Position, IfVersion: ifVersion}
	if req.GroupKey != nil {
		if err := refuseUnknownGroup(string(*req.GroupKey)); err != nil {
			return UpdateDocumentInput{}, err
		}
		group := string(*req.GroupKey)
		in.GroupKey = &group
	}
	if req.Title != nil {
		title, err := cleanTitle(*req.Title)
		if err != nil {
			return UpdateDocumentInput{}, err
		}
		in.Title = &title
	}
	return in, nil
}

// refuseUnknownGroup holds the closed set of four. The message lists them, so
// a caller who misspelled one can fix it without reading the schema.
func refuseUnknownGroup(group string) error {
	if documentGroups[group] {
		return nil
	}
	return &fieldError{
		field: "group_key",
		code:  "unknown_group",
		msg:   "group_key must be one of: " + strings.Join(slices.Sorted(maps.Keys(documentGroups)), ", "),
	}
}
