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

// GetRecordHistory implements (GET /records/{entity_type}/{id}/history).
// entityType arrives as a bare, generator-unvalidated string (an inline
// path param, not a named enum type) — this handler is the only
// enforcement point for the [person, organization, deal, lead, activity]
// vocabulary, same as GetFieldHistory's entity_type query param.
func (h Handlers) GetRecordHistory(w http.ResponseWriter, r *http.Request,
	entityType string, id crmcontracts.Id, params crmcontracts.GetRecordHistoryParams,
) {
	if !fieldHistoryEntityTypes[entityType] {
		httperr.Write(w, r, httperr.Validation("entity_type", "invalid_entity_type",
			"entity_type must be one of person, organization, deal, lead, activity"))
		return
	}
	f := RecordHistoryFilter{
		EntityType: entityType,
		EntityID:   ids.UUID(id),
		Cursor:     params.Cursor,
		Limit:      params.Limit,
	}

	page, err := ListRecordHistory(r.Context(), h.pool, f)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	data := make([]crmcontracts.AuditHistoryEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		data = append(data, recordHistoryEntryToWire(entry))
	}
	resp := crmcontracts.AuditHistoryListResponse{
		Data: data,
		Page: crmcontracts.PageInfo{HasMore: page.HasMore},
	}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// recordHistoryEntryToWire mirrors fieldHistoryEntryToWire's conversion
// style: uuid ids pass through openapi_types.UUID, a nullable uuid only
// sets the pointer when present, and before/after — already masked by
// omission in recordHistoryEntry — stay absent (a nil pointer, never an
// empty object) when the store recorded no image for that side, so a
// hidden key can never resurface as a phantom entry on the wire.
func recordHistoryEntryToWire(e RecordHistoryEntry) crmcontracts.AuditHistoryEntry {
	out := crmcontracts.AuditHistoryEntry{
		Id:                openapi_types.UUID(e.ID),
		ActorType:         crmcontracts.AuditHistoryEntryActorType(e.ActorType),
		ActorId:           e.ActorID,
		Action:            e.Action,
		OccurredAt:        e.OccurredAt,
		AuthorizationRule: e.AuthorizationRule,
		OnBehalfOfName:    e.OnBehalfOfName,
		Summary:           e.Summary,
	}
	if e.OnBehalfOf != nil {
		onBehalfOf := openapi_types.UUID(*e.OnBehalfOf)
		out.OnBehalfOf = &onBehalfOf
	}
	if e.Before != nil {
		before := e.Before
		out.Before = &before
	}
	if e.After != nil {
		after := e.After
		out.After = &after
	}
	return out
}
