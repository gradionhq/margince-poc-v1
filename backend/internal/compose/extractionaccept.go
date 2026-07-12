// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The extraction accept-write (RD-T10): persisting a staged extraction's
// grounded fields onto the attachment's deal, with one audited timeline
// note per field. This is compose orchestration, not a single module's
// handler (the coldstart-accept precedent): activities owns the
// attachment gate and the notes, deals owns the deal write — no module
// may own that flow alone (ADR-0054 §3, a module never imports a
// sibling). The gate stack, in order: the attachment resolves under the
// caller's parent-visibility gate (invisible/missing → 404), only a
// deal-scoped attachment has a deal to write (422 unsupported_entity_type),
// the caller holds deal update (403), and every requested key must name a
// GROUNDED field inside the closed deal-writable allowlist — any refusal
// is whole-request, zero writes.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

// extractionExecutorID stamps the audit note of a field accepted exactly
// as extracted: the value is the machine's, released by the human's
// accept — the same PrincipalSystem exec shape as the coldstart-accept
// executor, with the accepting human on OnBehalfOf.
const extractionExecutorID = "agent:attachment-extractor"

// extractionAcceptSource marks the notes as this operation's effect —
// greppable next to attachment_access_request, never mistakable for a
// hand-authored note.
const extractionAcceptSource = "attachment_extraction_accept"

// The closed deal-writable allowlist, spelled once: the four scalar deal
// columns that are plain document facts. setAcceptedDealField's switch is
// the single consumer deciding what may land.
const (
	acceptFieldName          = "name"
	acceptFieldAmountMinor   = "amount_minor"
	acceptFieldCurrency      = "currency"
	acceptFieldExpectedClose = "expected_close_date"
)

// acceptDealEntity is the RBAC object and activity-link entity type of
// the one record kind this flow writes.
const acceptDealEntity = "deal"

// ExtractionAccept executes acceptAttachmentExtraction: re-run the SAME
// extractor the read staged against, validate the whole request, write
// the deal once, then audit per field.
type ExtractionAccept struct {
	attachments *activities.Store
	deals       *deals.Store
	extractor   extraction.Extractor
}

// NewExtractionAccept wires the engine over the shared pool; a nil
// extractor falls back to the honest-empty NoOp (matching the activities
// read's default), under which no key can ever be grounded — the accept
// refuses rather than writing an unevidenced value.
func NewExtractionAccept(pool *pgxpool.Pool, extractor extraction.Extractor) *ExtractionAccept {
	if extractor == nil {
		extractor = extraction.NoOpExtractor{}
	}
	return &ExtractionAccept{
		attachments: activities.NewStore(pool),
		deals:       deals.NewStore(pool),
		extractor:   extractor,
	}
}

// Accept runs the full gate stack and the two-step write. The deal write
// and the notes cannot share one transaction from here — neither store
// exposes a tx-taking variant of these paths — so the deal update commits
// first and a note failure surfaces with that update already durable
// (stop-at-first-failure; the error says so). Every failure BEFORE the
// deal write is side-effect free. The notes carry no capture natural key,
// so re-driving the request after a mid-notes failure re-applies the deal
// update (last-write-wins, harmless) but DUPLICATES the already-written
// notes — the deal's own audit_log row is intact either way, so nothing
// is lost, only repeated.
func (a *ExtractionAccept) Accept(ctx context.Context, attachmentID ids.UUID, req crmcontracts.AcceptExtractionRequest) (crmcontracts.AttachmentExtractionAcceptResponse, error) {
	var zero crmcontracts.AttachmentExtractionAcceptResponse

	// The same parent-visibility gate as every other attachment op: an
	// invisible or missing parent answers 404, existence-hiding.
	att, err := a.attachments.GetAttachmentMeta(ctx, attachmentID)
	if err != nil {
		return zero, err
	}
	if att.EntityType != crmcontracts.AttachmentEntityTypeDeal {
		return zero, &UnsupportedEntityTypeError{EntityType: string(att.EntityType)}
	}
	// Deal-update authority gates the whole flow, before the extractor
	// runs or any validation answer discloses what the extraction grounds.
	// Row-scope visibility was the meta gate's (the parent walk), and the
	// deals store re-asserts it inside its own write transaction.
	if err := auth.Require(ctx, acceptDealEntity, principal.ActionUpdate); err != nil {
		return zero, err
	}
	if anyAcceptedFieldEdited(req) {
		// An edited field's note is the human's own authored activity
		// (captured_by human:<uid>), so their activity grant is part of the
		// gate stack — checked before the deal write, never discovered
		// after it committed.
		if err := auth.Require(ctx, "activity", principal.ActionCreate); err != nil {
			return zero, err
		}
	}

	extracted, err := a.extractor.Extract(ctx, att.Id.String())
	if err != nil {
		return zero, err
	}
	accepted, patch, err := buildExtractionAcceptPatch(req, groundedExtractionFields(extracted))
	if err != nil {
		return zero, err
	}

	// ONE partial update carries every accepted field. IfVersion stays
	// nil: the operation carries no If-Match, and the store's unguarded
	// mode is its own sanctioned shape (row-locked last-write-wins) — the
	// house spelling of poc-1's "version 0". The store re-checks
	// visibility and every deal invariant (money pair, INV-CLOSE-PAST)
	// inside its transaction; a refusal there rolls the whole write back
	// before any note exists.
	dealID := ids.From[ids.DealKind](ids.UUID(att.EntityId))
	if _, err := a.deals.UpdateDeal(ctx, dealID, patch); err != nil {
		return zero, err
	}
	if err := a.auditAcceptedFields(ctx, ids.UUID(att.EntityId), accepted); err != nil {
		return zero, err
	}

	out := crmcontracts.AttachmentExtractionAcceptResponse{
		DealId:   att.EntityId,
		Accepted: make([]crmcontracts.AcceptedExtractionField, 0, len(accepted)),
	}
	for _, f := range accepted {
		out.Accepted = append(out.Accepted, crmcontracts.AcceptedExtractionField{
			Field:      f.Field,
			Value:      f.Value,
			Provenance: f.Provenance,
		})
	}
	return out, nil
}

// auditAcceptedFields writes one timeline note per accepted field, linked
// to the deal: subject names the field, body is the verbatim source quote
// the value was grounded in — the evidence stays on the timeline whoever
// typed the final value. Provenance rides captured_by, the way every
// write in this system carries it: an unedited field executes as the
// extractor (PrincipalSystem, agent:attachment-extractor, on behalf of
// the accepting human), an edited one is the human's own write under the
// request principal.
func (a *ExtractionAccept) auditAcceptedFields(ctx context.Context, dealID ids.UUID, accepted []acceptedExtractionField) error {
	human, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("compose: extraction accept reached the audit step without an acting principal")
	}
	execCtx := principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         extractionExecutorID,
		UserID:     human.UserID,
		OnBehalfOf: human.UserID,
	})
	for _, f := range accepted {
		noteCtx := execCtx
		if f.Edited {
			noteCtx = ctx
		}
		subject := "Extraction accepted: " + f.Field
		body := f.SourceQuote
		_, _, err := a.attachments.LogActivity(noteCtx, activities.LogActivityInput{
			Kind:    string(crmcontracts.ActivityKindNote),
			Subject: &subject,
			Body:    &body,
			Links:   []activities.ActivityLinkInput{{EntityType: acceptDealEntity, EntityID: dealID}},
			Source:  extractionAcceptSource,
		})
		if err != nil {
			return fmt.Errorf("audit note for accepted field %s (the deal update itself is committed): %w", f.Field, err)
		}
	}
	return nil
}

// acceptedExtractionField is one validated field on its way onto the
// deal, carrying everything the note and the response need.
type acceptedExtractionField struct {
	Field       string
	Value       string
	SourceQuote string
	Provenance  crmcontracts.AcceptedExtractionFieldProvenance
	Edited      bool
}

// groundedExtractionFields indexes the extractor's grounded fields by
// name. Omitted entries stay out: they carry no value to accept, so a key
// naming one refuses as not_grounded exactly like a key never extracted.
func groundedExtractionFields(fields []extraction.ExtractedField) map[string]extraction.ExtractedField {
	grounded := make(map[string]extraction.ExtractedField, len(fields))
	for _, f := range fields {
		if !f.Omitted {
			grounded[f.Field] = f
		}
	}
	return grounded
}

// buildExtractionAcceptPatch validates the request against the re-run
// extraction and folds it into ONE deals partial update — any refused key
// refuses the whole request (no partial acceptance). field_keys is a set:
// a repeated key is accepted once. The minItems: 1 the contract declares
// is enforced here — the generated router does not validate bodies.
func buildExtractionAcceptPatch(req crmcontracts.AcceptExtractionRequest, grounded map[string]extraction.ExtractedField) ([]acceptedExtractionField, deals.UpdateDealInput, error) {
	if len(req.FieldKeys) == 0 {
		return nil, deals.UpdateDealInput{}, &ExtractionAcceptError{
			Field: "field_keys", Code: "required",
			Message: "field_keys must name at least one grounded field",
		}
	}
	var patch deals.UpdateDealInput
	accepted := make([]acceptedExtractionField, 0, len(req.FieldKeys))
	seen := make(map[string]bool, len(req.FieldKeys))
	for i, key := range req.FieldKeys {
		if seen[key] {
			continue
		}
		seen[key] = true
		g, ok := grounded[key]
		if !ok {
			return nil, deals.UpdateDealInput{}, &ExtractionAcceptError{
				Field: fmt.Sprintf("field_keys[%d]", i), Code: "not_grounded",
				Message: key + " is not grounded in this attachment's extraction; only evidence-backed fields can be accepted",
			}
		}
		field := acceptedExtractionField{
			Field:       key,
			Value:       g.Value,
			SourceQuote: g.SourceQuote,
			Provenance:  crmcontracts.AcceptedExtractionFieldProvenanceAiExtracted,
		}
		if req.Edits != nil {
			if raw, edited := (*req.Edits)[key]; edited {
				value, err := editedFieldValue(key, raw)
				if err != nil {
					return nil, deals.UpdateDealInput{}, err
				}
				field.Value = value
				field.Provenance = crmcontracts.AcceptedExtractionFieldProvenanceHuman
				field.Edited = true
			}
		}
		if err := setAcceptedDealField(&patch, i, field); err != nil {
			return nil, deals.UpdateDealInput{}, err
		}
		accepted = append(accepted, field)
	}
	return accepted, patch, nil
}

// setAcceptedDealField coerces one accepted value onto its UpdateDealInput
// slot. The switch IS the closed allowlist, derived from what the deals
// partial-update path accepts as a plain document fact. Its remaining
// fields are deliberately refused: the row references (organization_id,
// owner_id, partner_org_id) are links to records, not facts a quote can
// carry, and each demands its own link-target visibility gate;
// forecast_category is a rep's pipeline judgment; wait_until is a
// workflow timer; a cf_* passthrough would hand the extractor an open
// column surface. A grounded field outside this set answers
// not_deal_writable, whole-request.
func setAcceptedDealField(patch *deals.UpdateDealInput, position int, field acceptedExtractionField) error {
	switch field.Field {
	case acceptFieldName:
		name := field.Value
		patch.Name = &name
	case acceptFieldAmountMinor:
		amount, err := strconv.ParseInt(field.Value, 10, 64)
		if err != nil {
			return &ExtractionAcceptError{
				Field: fmt.Sprintf("field_keys[%d]", position), Code: "invalid_integer",
				Message: "amount_minor must be an integer amount in minor units",
			}
		}
		patch.AmountMinor = &amount
	case acceptFieldCurrency:
		// values.NewMoney is the ONE spelling of a valid ISO-4217 code (the
		// amount is irrelevant to that check); its ParseError already
		// carries the field and machine code the wire mapping expects.
		if _, err := values.NewMoney(0, field.Value); err != nil {
			return err
		}
		currency := field.Value
		patch.Currency = &currency
	case acceptFieldExpectedClose:
		day, err := time.Parse("2006-01-02", field.Value)
		if err != nil {
			return &ExtractionAcceptError{
				Field: fmt.Sprintf("field_keys[%d]", position), Code: "invalid_date",
				Message: "expected_close_date must be a YYYY-MM-DD calendar date",
			}
		}
		patch.ExpectedClose = &day
	default:
		return &ExtractionAcceptError{
			Field: fmt.Sprintf("field_keys[%d]", position), Code: "not_deal_writable",
			Message: field.Field + " is not a field an extraction may write onto a deal",
		}
	}
	return nil
}

// editedFieldValue narrows one edits value (additionalProperties: true on
// the wire) to the string every extraction value is: a JSON string rides
// as-is, a JSON number is formatted (an amount edit arrives as a number
// naturally); anything else cannot name a deal scalar.
//
//craft:ignore naked-any the edits map is the contract's additionalProperties seam; this function is the narrowing point
func editedFieldValue(key string, raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return "", &ExtractionAcceptError{
			Field: "edits." + key, Code: "invalid_edit_value",
			Message: "an edited value must be a string or a number",
		}
	}
}

// anyAcceptedFieldEdited reports whether any requested key carries an
// edit — those notes are the human's own authored activities, so their
// activity grant joins the gate stack. An edit for a key outside
// field_keys is inert and gates nothing.
func anyAcceptedFieldEdited(req crmcontracts.AcceptExtractionRequest) bool {
	if req.Edits == nil {
		return false
	}
	for _, key := range req.FieldKeys {
		if _, ok := (*req.Edits)[key]; ok {
			return true
		}
	}
	return false
}

// UnsupportedEntityTypeError maps to 422 unsupported_entity_type:
// accepting extraction fields writes a DEAL, and an attachment scoped to
// any other parent has no deal to write.
type UnsupportedEntityTypeError struct{ EntityType string }

func (e *UnsupportedEntityTypeError) Error() string {
	return "extraction accept is only valid on a deal-scoped attachment, not " + e.EntityType
}

// ExtractionAcceptError is one refused accept input: the whole request
// refuses (no partial acceptance), naming the offending field and the
// machine code.
type ExtractionAcceptError struct{ Field, Code, Message string }

func (e *ExtractionAcceptError) Error() string { return e.Field + ": " + e.Message }

// attachmentExtractionHandlers is the transport for the accept-write; the
// engine above owns the flow.
type attachmentExtractionHandlers struct {
	accept *ExtractionAccept
}

func (h attachmentExtractionHandlers) AcceptAttachmentExtraction(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.AcceptExtractionRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	resp, err := h.accept.Accept(r.Context(), ids.UUID(id), req)
	if err != nil {
		writeExtractionAcceptErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// writeExtractionAcceptErr maps the accept flow's typed refusals onto the
// wire, mirroring the deals transport's spellings for the store errors
// this flow can trip (the resulting-row money pair, INV-CLOSE-PAST, the
// CHECK-constraint net), then falls through to the sentinel registry.
func writeExtractionAcceptErr(w http.ResponseWriter, r *http.Request, err error) {
	var unsupported *UnsupportedEntityTypeError
	if errors.As(err, &unsupported) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "unsupported_entity_type",
			Detail: unsupported.Error(),
		})
		return
	}
	var refused *ExtractionAcceptError
	if errors.As(err, &refused) {
		httperr.Write(w, r, httperr.Validation(refused.Field, refused.Code, refused.Message))
		return
	}
	var amountPair *deals.AmountCurrencyPairError
	if errors.As(err, &amountPair) {
		httperr.Write(w, r, httperr.Validation(acceptFieldCurrency, "amount_currency_pair", amountPair.Error()))
		return
	}
	var pastClose *deals.PastCloseDateError
	if errors.As(err, &pastClose) {
		httperr.Write(w, r, httperr.Validation(acceptFieldExpectedClose, "close_date_past", pastClose.Error()))
		return
	}
	if constraint, ok := storekit.CheckViolation(err); ok {
		httperr.Write(w, r, httperr.Validation(constraint, "constraint_violated",
			"the request violates the "+constraint+" business rule"))
		return
	}
	httperr.Write(w, r, err)
}
