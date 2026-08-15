// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// GetAttachmentExtraction and RequestAttachmentAccess (RD-T10): the staged,
// evidence-or-omit AI-extraction read and the audited "someone wants in"
// courtesy note. Both inherit the attachment's row-scope gate exactly like
// every other attachment op (Store.GetAttachmentMeta) — the same 404 an
// invisible parent or a missing attachment answers everywhere else on this
// surface. The accept-write that persists grounded fields onto a deal is
// compose orchestration (compose/attachment_extraction.go), not here — this
// file only ever reads and audits, never mutates a deal.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// GetAttachmentExtraction answers the poll with the newest reading of this
// attachment: its status, the fields it grounded, and what it honestly omitted
// (RD-WIRE-N-2). A pure read — zero writes, no model call, no extractor here at
// all. The reading itself is compose orchestration behind a 202, because a
// model call takes seconds and can fail and so cannot happen inside the request
// that asks for it.
//
// 404 when this attachment has never been read, which is the honest difference
// between nobody asking and a reading that got nothing. Valid for ANY
// entity_type: a non-deal attachment reads fine, since accepting fields onto a
// deal (not this op) is what is deal-only.
func (h Handlers) GetAttachmentExtraction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	read, err := h.store.LatestExtractionRead(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, extractionReport(read))
}

// extractionReport maps the run record onto the contract's wire shape,
// splitting a grounded field (always carrying its evidence) from one the
// reading honestly could not offer. Both slices stay non-nil even when empty,
// so the wire body is `[]`, never `null`.
func extractionReport(read ExtractionRead) crmcontracts.AttachmentExtraction {
	out := crmcontracts.AttachmentExtraction{
		Id:           openapi_types.UUID(read.ID),
		Status:       crmcontracts.AttachmentExtractionStatus(read.Status),
		StatusDetail: read.StatusDetail,
		CreatedAt:    read.CreatedAt,
		FinishedAt:   read.FinishedAt,
		Fields:       make([]crmcontracts.ExtractedField, 0, len(read.Fields)),
		Omitted:      make([]crmcontracts.OmittedExtractionField, 0),
	}
	for _, f := range read.Fields {
		if f.Omitted {
			out.Omitted = append(out.Omitted, crmcontracts.OmittedExtractionField{
				Field:  f.Field,
				Reason: crmcontracts.OmittedExtractionFieldReason(f.OmittedReason),
			})
			continue
		}
		out.Fields = append(out.Fields, crmcontracts.ExtractedField{
			Field:         f.Field,
			Value:         f.Value,
			SourceQuote:   f.SourceQuote,
			PageOrSection: f.PageOrSection,
			Confidence:    crmcontracts.ExtractedFieldConfidence(f.Confidence),
		})
	}
	return out
}

// requestAccessSource marks the audit note LogActivity writes for
// RequestAttachmentAccess — distinct from a human's own "manual" note so the
// courtesy record is greppable as this op's effect, not a hand-authored one.
const requestAccessSource = "attachment_access_request"

// requestAccessLinks ties the courtesy note back to the attachment's parent
// when the activity_link table supports that entity kind (person /
// organization / deal). An activity or lead parent has no activity_link
// column for its own kind, so the note is written unlinked for those —
// still findable through the parent's own audit trail, just not surfaced on
// its timeline.
func requestAccessLinks(entityType crmcontracts.AttachmentEntityType, entityID ids.UUID) []ActivityLinkInput {
	switch entityType {
	case crmcontracts.AttachmentEntityTypePerson, crmcontracts.AttachmentEntityTypeOrganization, crmcontracts.AttachmentEntityTypeDeal:
		return []ActivityLinkInput{{EntityType: string(entityType), EntityID: entityID}}
	default:
		return nil
	}
}

// requestAccessBody renders the courtesy note's body: the filename, so the
// timeline entry is legible without opening the attachment.
func requestAccessBody(filename string) *string {
	body := "Access requested: " + filename
	return &body
}

// RequestAttachmentAccess writes one audited timeline note carrying the
// requesting principal and answers {requested: true}. poc-v1 has no
// restricted-but-disclosed attachment state — an out-of-scope parent is
// always 404 here, never a locked-row placeholder like
// poc-1's RD-AC-2 disclosure model. Visibility already IS access in this
// system, so this op cannot unlock anything a caller could not already
// see: it is a courtesy audit trail for a caller who can already see the
// row, gated identically to every other attachment read (an invisible or
// missing parent answers 404, exactly as if the attachment did not exist).
func (h Handlers) RequestAttachmentAccess(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	att, err := h.store.GetAttachmentMeta(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	_, _, err = h.store.LogActivity(r.Context(), LogActivityInput{
		Kind:   string(crmcontracts.ActivityKindNote),
		Body:   requestAccessBody(att.Filename),
		Links:  requestAccessLinks(att.EntityType, ids.UUID(att.EntityId)),
		Source: requestAccessSource,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RequestAccessResponse{Requested: true})
}
