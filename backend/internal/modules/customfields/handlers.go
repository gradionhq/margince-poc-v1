// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// Handlers is the module's transport surface (Handlers→Service, per this
// module's engine shape): the five contract operations over the
// custom-field catalog. Wire concerns only — decode, validate the
// wire-only shape, map the engine's typed refusals to the contract's
// error codes; the Service owns the transactional write and the RBAC
// gate at its entry points.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the module's transport surface over the Service.
type Handlers struct {
	svc *Service
}

// NewHandlers wires the module. schemaPool MAY be nil (decisions/0024
// "unwired by default"): Create/SetOptions then answer their generated
// 501 rather than nil-derefing at request time.
func NewHandlers(pool, schemaPool *pgxpool.Pool) Handlers {
	return Handlers{svc: NewService(pool, schemaPool)}
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// ListCustomFields backs the admin field table: object is required (the
// closed 5-enum), status narrows to one lifecycle state — omitted
// returns both active and retired (CUSTOM-FIELDS-WIRE-1).
func (h Handlers) ListCustomFields(w http.ResponseWriter, r *http.Request, params crmcontracts.ListCustomFieldsParams) {
	in := ListInput{
		Object: string(params.Object),
		Cursor: params.Cursor,
		Limit:  params.Limit,
	}
	if params.Status != nil {
		s := string(*params.Status)
		in.Status = &s
	}
	fields, page, err := h.svc.List(r.Context(), in)
	if err != nil {
		writeCustomFieldErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.CustomFieldListResponse{Data: fields, Page: pageInfo(page)})
}

// CreateCustomField runs the governed engine's single-transaction add
// (CUSTOM-FIELDS-WIRE-2): schema change + catalog row + one audit entry
// land or roll back together. Always 🟡 — an agent caller is gated
// upstream by agentGate (compose/agentgate.go); a human's direct call is
// itself the approval.
func (h Handlers) CreateCustomField(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateCustomFieldParams) {
	var req crmcontracts.CreateCustomFieldRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	spec := FieldSpec{
		Object:   string(req.Object),
		Label:    req.Label,
		Type:     string(req.Type),
		Currency: req.Currency,
		Source:   req.Source,
	}
	if req.Options != nil {
		spec.Options = *req.Options
	}
	field, err := h.svc.Create(r.Context(), spec)
	if err != nil {
		writeCustomFieldErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/custom-fields/"+field.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, field)
}

// RenameCustomField updates the catalog label only (CUSTOM-FIELDS-WIRE-3):
// merge-PATCH, so an absent label is the same "nothing to rename" refusal
// as an explicit empty string — column_name/object/type never move.
func (h Handlers) RenameCustomField(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RenameCustomFieldParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.RenameCustomFieldRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	var label string
	if req.Label != nil {
		label = *req.Label
	}
	field, err := h.svc.Rename(r.Context(), ids.UUID(id), label, ifVersion)
	if err != nil {
		writeCustomFieldErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, field)
}

// RetireCustomField soft-retires the field (CUSTOM-FIELDS-WIRE-4):
// status flips to retired, archived_at stays null, the physical column
// and every value in it are preserved.
func (h Handlers) RetireCustomField(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RetireCustomFieldParams) {
	field, err := h.svc.Retire(r.Context(), ids.UUID(id))
	if err != nil {
		writeCustomFieldErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, field)
}

// UpdateCustomFieldOptions replaces a picklist's allowed option set and
// regenerates the physical column's CHECK constraint (CUSTOM-FIELDS-PARAM-5)
// — the one lifecycle mutation besides Create that runs DDL.
func (h Handlers) UpdateCustomFieldOptions(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateCustomFieldOptionsParams) {
	var req crmcontracts.UpdateCustomFieldOptionsRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	field, err := h.svc.SetOptions(r.Context(), ids.UUID(id), req.Options)
	if err != nil {
		writeCustomFieldErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, field)
}

// writeCustomFieldErr maps the engine's typed refusals onto the wire
// shapes the contract names, then falls through to httperr.Write's
// sentinel registry — which already resolves apperrors.ErrNotFound (a
// missing/out-of-workspace row), apperrors.ErrVersionSkew (a stale
// If-Match), apperrors.ErrConflict for every conflict spelling (the
// wrapped duplicate-slug conflict, *ColumnTakenError via its Is method,
// ErrFieldRetired, and the retryable ErrTableBusy lock-timeout answer),
// and apperrors.ErrPermissionDenied for every RBAC deny — customfields
// adds no branch for any of those.
func writeCustomFieldErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrSchemaChangesUnavailable) {
		// Mirrors the unwired-blobstore posture (activities.ErrBlobstoreUnconfigured):
		// a deployment that never mounted --schema-dsn declares the gap by
		// omission rather than nil-derefing the schema pool at request time.
		httperr.NotImplemented(w, r, "custom-field schema changes")
		return
	}
	var verr *ValidationError
	if errors.As(err, &verr) {
		httperr.Write(w, r, validationDetails(verr.Errors))
		return
	}
	if errors.Is(err, ErrStructural) {
		httperr.Write(w, r, structuralChangeRefused())
		return
	}
	if errors.Is(err, ErrNotPicklist) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "not_picklist",
			Detail: "Only a picklist field's options can be edited.",
		})
		return
	}
	if errors.Is(err, ErrLastOption) {
		httperr.Write(w, r, httperr.Validation(fieldOptions, "min_one_required", "A picklist needs at least one option"))
		return
	}
	httperr.Write(w, r, err)
}

// validationDetails renders the engine's complete field-violation list as
// the contract's `details.errors[{field,code}]` 422 shape in one round
// trip — the engine carries no per-field message, so none is fabricated
// here (unlike httperr.Validation's single-field helper, which always has
// exactly one message to echo).
func validationDetails(errs []FieldError) *httperr.DetailedError {
	wire := make([]map[string]string, len(errs))
	for i, e := range errs {
		wire[i] = map[string]string{"field": e.Field, "code": e.Code}
	}
	return &httperr.DetailedError{
		Status:  http.StatusUnprocessableEntity,
		Code:    "validation_error",
		Detail:  "One or more fields are invalid.",
		Details: map[string]any{"errors": wire},
	}
}

// structuralChangeRefused is the contract's exact 422 shape for a label
// judged structural (CUSTOM-FIELDS-AC-4/AC-8) — detail text and
// details.route match the crm.yaml example verbatim.
func structuralChangeRefused() *httperr.DetailedError {
	return &httperr.DetailedError{
		Status: http.StatusUnprocessableEntity,
		Code:   "structural_change_refused",
		Detail: "This looks like a new object, relationship, or logic — not a scalar attribute on an existing object. Runtime custom fields only add bounded scalar columns; a structural change ships as a reviewed source change instead.",
		Details: map[string]any{
			"route": "source_development_path",
		},
	}
}
