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
	"strconv"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// maxAttachmentBytes caps one upload. The body is bounded by
// http.MaxBytesReader so a client cannot exhaust memory streaming a
// too-large file; the limit is deliberately modest for the PoC surface.
const maxAttachmentBytes = 25 << 20 // 25 MiB

// UploadAttachment stores an uploaded file against an entity. Multipart is
// parsed here (the JSON decoder cannot carry bytes); the store owns the
// RBAC gate, provenance, and the write shape.
func (h Handlers) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes)
	//nolint:gosec // r.Body is bounded by http.MaxBytesReader above, so total parse size is capped; the arg only sets the in-memory/spill threshold.
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		httperr.Write(w, r, httperr.Validation("file", "invalid_multipart",
			"the request must be multipart/form-data within the size limit"))
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

	att, err := h.store.UploadAttachment(r.Context(), AttachmentInput{
		EntityType:  entityType,
		EntityID:    entityID,
		Filename:    header.Filename,
		ContentType: header.Header.Get("Content-Type"),
		Body:        body,
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
	defer func(ctx context.Context) {
		if cerr := rc.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing attachment object reader", "err", cerr)
		}
	}(r.Context())

	contentType := "application/octet-stream"
	if meta.ContentType != nil && *meta.ContentType != "" {
		contentType = *meta.ContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", meta.Filename))
	if meta.ByteSize != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*meta.ByteSize, 10))
	}
	// The status is already 200 once bytes flow; a copy failure (usually a
	// client disconnect mid-download) can only be logged, not re-reported.
	if _, err := io.Copy(w, rc); err != nil {
		slog.WarnContext(r.Context(), "streaming attachment download", "attachment", id.String(), "err", err)
	}
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

// GetAttachmentExtraction stays an explicit 501 until the `shared/ports/extraction`
// seam is wired into this handler set (RD-T10) — the contract and the
// Tier-0 Extractor port land first.
func (h Handlers) GetAttachmentExtraction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetAttachmentExtraction")
}

// RequestAttachmentAccess stays an explicit 501 until the audit-note write
// lands with the extraction seam (RD-T10).
func (h Handlers) RequestAttachmentAccess(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "RequestAttachmentAccess")
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
