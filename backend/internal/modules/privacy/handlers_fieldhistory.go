// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// GetFieldHistory implements (GET /field-history).
func (h Handlers) GetFieldHistory(w http.ResponseWriter, r *http.Request, params crmcontracts.GetFieldHistoryParams) {
	entityType := string(params.EntityType)
	if !fieldHistoryEntityTypes[entityType] {
		httperr.Write(w, r, httperr.Validation("entity_type", "invalid_entity_type",
			"entity_type must be one of person, organization, deal, lead, activity"))
		return
	}
	f := FieldHistoryFilter{
		EntityType: entityType,
		EntityID:   ids.UUID(params.EntityId),
		Field:      params.Field,
		Cursor:     params.Cursor,
		Limit:      params.Limit,
	}
	if params.ActorType != nil {
		at := string(*params.ActorType)
		if !fieldHistoryActorTypes[at] {
			httperr.Write(w, r, httperr.Validation("actor_type", "invalid_actor_type",
				"actor_type must be one of human, agent, system, connector"))
			return
		}
		f.ActorType = &at
	}

	page, err := ListFieldHistory(r.Context(), h.pool, f)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	data := make([]crmcontracts.FieldHistoryEntry, 0, len(page.Entries))
	for _, e := range page.Entries {
		data = append(data, fieldHistoryEntryToWire(e))
	}
	resp := crmcontracts.FieldHistoryListResponse{
		Data: data,
		Page: crmcontracts.PageInfo{HasMore: page.HasMore},
	}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// fieldHistoryEntryToWire mirrors auditEntryToWire's conversion style: uuid
// ids pass through openapi_types.UUID, a nullable uuid only sets the
// pointer when present, and the jsonb-shaped evidence stays absent (never
// an empty object) when the store recorded none.
func fieldHistoryEntryToWire(e FieldHistoryEntry) crmcontracts.FieldHistoryEntry {
	out := crmcontracts.FieldHistoryEntry{
		Id:         openapi_types.UUID(e.ID),
		EntityType: crmcontracts.FieldHistoryEntryEntityType(e.EntityType),
		EntityId:   openapi_types.UUID(e.EntityID),
		Field:      e.Field,
		OldValue:   e.OldValue,
		NewValue:   e.NewValue,
		ChangedAt:  e.ChangedAt,
		ActorType:  crmcontracts.FieldHistoryEntryActorType(e.ActorType),
		ActorId:    e.ActorID,
	}
	if e.PassportID != nil {
		id := openapi_types.UUID(*e.PassportID)
		out.PassportId = &id
	}
	if e.Evidence != nil {
		evidence := e.Evidence
		out.Evidence = &evidence
	}
	return out
}
