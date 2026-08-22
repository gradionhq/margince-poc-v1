// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListDealDocuments serves the deal's Files area: its own uploads and the
// files of every message linked to it, each row scoped through its own parent.
func (h Handlers) ListDealDocuments(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, params crmcontracts.ListDealDocumentsParams,
) {
	in := DealDocumentFilters{
		IncludeHidden: params.IncludeHidden != nil && *params.IncludeHidden,
	}
	if params.Cursor != nil {
		c := string(*params.Cursor)
		in.Cursor = &c
	}
	if params.Limit != nil {
		l := int(*params.Limit)
		in.Limit = &l
	}
	if params.Category != nil {
		c := string(*params.Category)
		in.Category = &c
	}
	docs, page, err := h.store.ListDealDocuments(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK,
		crmcontracts.DealDocumentListResponse{Data: docs, Page: pageInfo(page)})
}

// HideDealDocument takes a file off this deal's Files area without touching
// the file.
func (h Handlers) HideDealDocument(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, attachmentID openapi_types.UUID) {
	if err := h.store.HideDealDocument(r.Context(), ids.UUID(id), ids.UUID(attachmentID)); err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnhideDealDocument lists the file on this deal again.
func (h Handlers) UnhideDealDocument(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, attachmentID openapi_types.UUID) {
	if err := h.store.UnhideDealDocument(r.Context(), ids.UUID(id), ids.UUID(attachmentID)); err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
