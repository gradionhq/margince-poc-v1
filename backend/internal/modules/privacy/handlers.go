// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is privacy's transport surface: the audit-log governance
// read and the field-history projection (the erasure/SAR/retention
// engines run behind the DSR queue and the worker, not their own
// routes).
type Handlers struct {
	// db binds the installation's workspace itself (ADR-0091 §9 step 3).
	db *database.DB
}

// NewHandlers wires the transport over the installation-bound pool.
func NewHandlers(db *database.DB) Handlers { return Handlers{db: db} }

// ListAuditLog implements (GET /audit-log).
func (h Handlers) ListAuditLog(w http.ResponseWriter, r *http.Request, params crmcontracts.ListAuditLogParams) {
	f := AuditFilter{
		Actor:      params.Actor,
		EntityType: params.EntityType,
		Action:     params.Action,
		From:       params.From,
		To:         params.To,
		Cursor:     params.Cursor,
		Limit:      params.Limit,
	}
	if params.EntityId != nil {
		entityID := ids.UUID(*params.EntityId)
		f.EntityID = &entityID
	}

	page, err := ListAuditLog(r.Context(), h.db, f)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	data := make([]crmcontracts.AuditLogEntry, 0, len(page.Entries))
	for _, e := range page.Entries {
		entry, err := auditEntryToWire(e)
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		data = append(data, entry)
	}
	resp := struct {
		Data []crmcontracts.AuditLogEntry `json:"data"`
		Page crmcontracts.PageInfo        `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

func auditEntryToWire(e AuditEntry) (crmcontracts.AuditLogEntry, error) {
	out := crmcontracts.AuditLogEntry{
		Id:                openapi_types.UUID(e.ID),
		ActorType:         crmcontracts.AuditLogEntryActorType(e.ActorType),
		ActorId:           e.ActorID,
		Action:            crmcontracts.AuditLogEntryAction(e.Action),
		EntityType:        e.EntityType,
		AuthorizationRule: e.AuthorizationRule,
		OccurredAt:        e.OccurredAt,
	}
	if e.PassportID != nil {
		id := openapi_types.UUID(e.PassportID.UUID)
		out.PassportId = &id
	}
	if e.OnBehalfOf != nil {
		id := openapi_types.UUID(e.OnBehalfOf.UUID)
		out.OnBehalfOf = &id
	}
	// entity_id is NOT NULL since 0075 (audit_log is record-mutations-only);
	// the contract field is non-optional to match. The domain read model
	// still carries a pointer for historical rows, so guard defensively.
	if e.EntityID != nil {
		out.EntityId = openapi_types.UUID(*e.EntityID)
	}
	var err error
	if out.Before, err = decodeJSONObject(e.Before); err != nil {
		return crmcontracts.AuditLogEntry{}, err
	}
	if out.After, err = decodeJSONObject(e.After); err != nil {
		return crmcontracts.AuditLogEntry{}, err
	}
	if out.Evidence, err = decodeJSONObject(e.Evidence); err != nil {
		return crmcontracts.AuditLogEntry{}, err
	}
	return out, nil
}

// decodeJSONObject renders a stored jsonb image for the wire; a NULL
// column stays absent.
func decodeJSONObject(raw []byte) (*map[string]interface{}, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
