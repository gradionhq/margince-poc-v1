// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// multipartSpillBytes is how much of an upload is held in memory before the
// rest goes to a temp file.
//
// Deliberately far below the ceiling. `ParseMultipartForm`'s argument is the
// in-memory threshold, not a cap — passing the ceiling means nothing ever
// spills, so concurrent uploads scale resident memory by their own size with no
// admission control anywhere. The body is bounded by the MaxBytesReader above
// it either way; this only decides where the bytes live while being read.
const multipartSpillBytes = 1 << 20

// errUploadLimitUnset reports that this composition never told the handler what
// its ceiling is. A wiring fault, not a request fault, so it answers 500 rather
// than refusing the caller's file for a size nobody set.
var errUploadLimitUnset = errors.New("activities: no upload ceiling configured for this role")

// WithUploadLimit returns handlers that parse an upload under the deployment's
// ceiling for this route (OPS-CFG-12). Compose calls it.
//
// The zero value refuses every upload rather than defaulting to something
// generous: an unconfigured ceiling is a wiring mistake, and a handler that
// silently invents its own number is how the chassis and the parse end up
// disagreeing about what fits.
func (h Handlers) WithUploadLimit(bytes int64) Handlers {
	h.uploadLimit = bytes
	return h
}

// UploadAttachment stores an uploaded file against an entity. Multipart is
// parsed here (the JSON decoder cannot carry bytes); the store owns the
// RBAC gate, provenance, and the write shape.
func (h Handlers) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	if h.uploadLimit <= 0 {
		// Answered as OUR fault, because it is: nobody wired the ceiling. The
		// alternative — carrying on with a zero bound — refuses the caller's
		// perfectly good file and tells them it exceeds a 0 MB limit, which
		// sends them off to shrink a file that was never the problem.
		httperr.Write(w, r, errUploadLimitUnset)
		return
	}
	// The same ceiling the chassis already applied, and applied again anyway: it
	// is what makes this handler correct when mounted without that middleware.
	// It can only ever tighten — a MaxBytesReader cannot widen a body an outer
	// one already bounded — so the two agreeing is the point, not a redundancy.
	r.Body = http.MaxBytesReader(w, r.Body, h.uploadLimit)
	//nolint:gosec // G120 wants a bound here, and the bound is the MaxBytesReader above: this argument is only the in-memory/spill threshold, and it is deliberately far below the ceiling so the parse spills rather than holding the upload resident.
	if err := r.ParseMultipartForm(multipartSpillBytes); err != nil {
		httperr.WriteMultipartRefusal(w, r, err, h.uploadLimit)
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

	// The part is handed on as a reader, not as bytes: the store hashes it and
	// streams it to object storage, so the whole file never has to be resident
	// at once.
	att, err := h.store.UploadAttachment(r.Context(), AttachmentInput{
		EntityType:  entityType,
		EntityID:    entityID,
		Filename:    header.Filename,
		ContentType: header.Header.Get("Content-Type"),
		Content:     file,
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
