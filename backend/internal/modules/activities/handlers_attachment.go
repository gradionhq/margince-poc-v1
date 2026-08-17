// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// maxAttachmentBytes caps one upload, so a client cannot exhaust memory
// streaming a too-large file.
//
// It IS the chassis ceiling rather than a number of its own: this is the widest
// route that carries a file, and a route cannot widen what the chassis already
// bounded, so a separate literal here could only ever be equal or dead. Two
// constants that must agree are one constant.
const maxAttachmentBytes = httperr.MaxMultipartBodyBytes

// UploadAttachment stores an uploaded file against an entity. Multipart is
// parsed here (the JSON decoder cannot carry bytes); the store owns the
// RBAC gate, provenance, and the write shape.
func (h Handlers) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	// Equal to the ceiling the chassis already applied, and kept anyway: it is
	// what makes this handler correct when mounted without that middleware, and
	// it is what the waiver below points at.
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes)
	//nolint:gosec // r.Body is bounded by http.MaxBytesReader above, so total parse size is capped; the arg only sets the in-memory/spill threshold.
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		httperr.WriteMultipartRefusal(w, r, err, maxAttachmentBytes)
		return
	}
	entityType := r.FormValue("entity_type")
	if !crmcontracts.AttachmentEntityType(entityType).Valid() {
		httperr.Write(w, r, httperr.Validation("entity_type", "invalid_enum",
			"entity_type must be one of person, organization, deal, activity, lead"))
		return
	}
	entityID, err := ids.Parse(r.FormValue("entity_id"))
	if err != nil {
		httperr.Write(w, r, httperr.Validation("entity_id", "invalid_uuid", "entity_id must be a UUID"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httperr.Write(w, r, httperr.Validation("file", "required", "a file part is required"))
		return
	}
	defer func(ctx context.Context) {
		if cerr := file.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing uploaded file part", "err", cerr)
		}
	}(r.Context())
	body, err := io.ReadAll(file)
	if err != nil {
		httperr.Write(w, r, httperr.Validation("file", "too_large",
			fmt.Sprintf("the file exceeds the %d-byte limit or could not be read", maxAttachmentBytes)))
		return
	}

	// The agreement this document is about, when the uploader named one. An
	// absent part files the document against no contract, which is the ordinary
	// case: most client paper is not contract paper.
	var contractID *ids.UUID
	if raw := r.FormValue("contract_id"); raw != "" {
		parsed, perr := ids.Parse(raw)
		if perr != nil {
			httperr.Write(w, r, httperr.Validation("contract_id", "invalid_uuid", "contract_id must be a UUID"))
			return
		}
		contractID = &parsed
	}

	att, err := h.store.UploadAttachment(r.Context(), AttachmentInput{
		EntityType:  entityType,
		EntityID:    entityID,
		Filename:    header.Filename,
		ContentType: header.Header.Get("Content-Type"),
		Body:        body,
		ContractID:  contractID,
	})
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/attachments/"+att.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, att)
}

// ListAttachments returns one entity's attachment metadata (cursor-paginated).
func (h Handlers) ListAttachments(w http.ResponseWriter, r *http.Request, params crmcontracts.ListAttachmentsParams) {
	var cursor *string
	if params.Cursor != nil {
		c := string(*params.Cursor)
		cursor = &c
	}
	var limit *int
	if params.Limit != nil {
		l := int(*params.Limit)
		limit = &l
	}
	atts, page, err := h.store.ListAttachments(r.Context(),
		string(params.EntityType), ids.UUID(params.EntityId), cursor, limit)
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.AttachmentListResponse{Data: atts, Page: pageInfo(page)})
}

// DownloadAttachment streams an attachment's bytes; Content-Disposition
// names the file so a browser saves it rather than rendering it inline.
func (h Handlers) DownloadAttachment(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	meta, rc, err := h.store.OpenAttachment(r.Context(), ids.UUID(id))
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	contentType := "application/octet-stream"
	if meta.ContentType != nil && *meta.ContentType != "" {
		contentType = *meta.ContentType
	}
	var size int64
	if meta.ByteSize != nil {
		size = *meta.ByteSize
	}
	httperr.StreamObject(w, r, httperr.StreamedObject{
		Body:        rc,
		ContentType: contentType,
		Filename:    meta.Filename,
		Size:        size,
	}, "attachment "+id.String())
}

// DeleteAttachment soft-archives an attachment (its object is purged by the
// erasure/retention path, not here).
func (h Handlers) DeleteAttachment(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.store.ArchiveAttachment(r.Context(), ids.UUID(id)); err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAttachmentErr maps a role that wired no object store to a 501, and
// otherwise defers to the module's shared store-error mapping.
func writeAttachmentErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrBlobstoreUnconfigured) {
		httperr.NotImplemented(w, r, "attachments")
		return
	}
	writeStoreErr(w, r, err)
}

// ListOrganizationDocuments serves the account's document library. Every row is
// scoped through its own primary parent, so a file on a record the caller
// cannot read contributes neither a row nor a count (DOC-WIRE-1).
func (h Handlers) ListOrganizationDocuments(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, params crmcontracts.ListOrganizationDocumentsParams,
) {
	in := DocumentFilters{
		PinnedOnly: params.PinnedOnly != nil && *params.PinnedOnly,
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
	if params.DocState != nil {
		s := string(*params.DocState)
		in.DocState = &s
	}
	if params.ContractId != nil {
		contractID := ids.UUID(*params.ContractId)
		in.ContractID = &contractID
	}
	docs, page, err := h.store.ListOrganizationDocuments(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK,
		crmcontracts.AttachmentListResponse{Data: docs, Page: pageInfo(page)})
}

// UpdateAttachmentMetadata sets what a document means: its category, its display
// title, its lifecycle state, its pin and what it replaces (DOC-WIRE-2).
func (h Handlers) UpdateAttachmentMetadata(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.UpdateAttachmentMetadataRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	var in DocumentMetadata
	if req.Category != nil {
		c := string(*req.Category)
		in.Category = &c
	}
	if req.DocState != nil {
		s := string(*req.DocState)
		in.DocState = &s
	}
	in.Pinned = req.Pinned
	// A JSON null is an EDIT — "this document replaces nothing after all" — and
	// an absent field is not. openapi's nullable pointer collapses the two, so
	// the raw body decides which one arrived.
	if raw, present := httperr.PresentField(r, "title"); present {
		in.ClearTitle = raw == nil
		if raw != nil {
			in.Title = req.Title
		}
	}
	if raw, present := httperr.PresentField(r, "supersedes_id"); present {
		in.ClearSupersedes = raw == nil
		if raw != nil && req.SupersedesId != nil {
			target := ids.UUID(*req.SupersedesId)
			in.Supersedes = &target
		}
	}
	out, err := h.store.UpdateAttachmentMetadata(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeAttachmentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}
