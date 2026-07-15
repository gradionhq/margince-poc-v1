// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// contextMaxItemsMin/Max/Default mirror the crm.yaml `max_items` query
// parameter on GET /records/{entity_type}/{id}/context (minimum: 1,
// maximum: 25, default 5) — an out-of-range value is a client error
// (422), not a value to silently clamp or default away.
const (
	contextMaxItemsMin     = 1
	contextMaxItemsMax     = 25
	contextMaxItemsDefault = 5
)

// isContextAnchor reports whether entityType names a record the graph
// walk can anchor on — derived from the module's one entity table
// (every searchable type except activity, which is a link rather than a
// thing links hang off). That set is exactly the contract's path-param
// enum {person, organization, deal, lead}, so the validation rule has no
// parallel list to drift from.
func isContextAnchor(entityType string) bool {
	for i := range searchBranches {
		if searchBranches[i].entity == entityType {
			return !searchBranches[i].activityWalk
		}
	}
	return false
}

// GetRecordContext shadows the generated 501 stub with the assembled
// context graph walk (Retriever.AssembleContext): anchor profile, recent
// touches, and related records, each item provenance-stamped. The walk
// runs inside the request's RLS-scoped transaction, so an anchor outside
// the caller's row scope surfaces as apperrors.ErrNotFound — the same
// existence-hiding posture as the sibling /history endpoint — never a
// leak of another tenant's neighborhood.
func (h Handlers) GetRecordContext(w http.ResponseWriter, r *http.Request,
	entityType string, id crmcontracts.Id, params crmcontracts.GetRecordContextParams,
) {
	if !isContextAnchor(entityType) {
		httperr.Write(w, r, httperr.Validation("entity_type", "invalid_entity_type",
			"entity_type must be one of person, organization, deal, lead"))
		return
	}
	if params.MaxItems != nil && (*params.MaxItems < contextMaxItemsMin || *params.MaxItems > contextMaxItemsMax) {
		httperr.Write(w, r, httperr.Validation("max_items", "out_of_range",
			"max_items must be between 1 and 25"))
		return
	}
	maxItems := contextMaxItemsDefault
	if params.MaxItems != nil {
		maxItems = *params.MaxItems
	}

	anchor := datasource.EntityRef{Type: datasource.EntityType(entityType), ID: ids.UUID(id)}
	assembled, err := h.retriever.AssembleContext(r.Context(), anchor, retrieval.AssembleOptions{MaxItems: maxItems})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	resp := crmcontracts.ContextResponse{
		Anchor: crmcontracts.ContextEntityRef{
			Type: crmcontracts.ContextEntityRefType(assembled.Anchor.Type),
			Id:   openapi_types.UUID(assembled.Anchor.ID),
		},
		Sections: make([]crmcontracts.ContextSection, 0, len(assembled.Sections)),
	}
	for _, section := range assembled.Sections {
		items := make([]crmcontracts.ContextItem, 0, len(section.Items))
		for _, item := range section.Items {
			items = append(items, contextItemWire(item))
		}
		resp.Sections = append(resp.Sections, crmcontracts.ContextSection{Name: section.Name, Items: items})
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// contextItemWire maps one assembled-context item onto the wire shape:
// summary is omitted (not empty-stringed) when the graph walk had none,
// and evidence is omitted rather than an empty array, matching the
// contract's optional fields.
func contextItemWire(item retrieval.Item) crmcontracts.ContextItem {
	out := crmcontracts.ContextItem{
		Ref: crmcontracts.ContextEntityRef{
			Type: crmcontracts.ContextEntityRefType(item.Ref.Type),
			Id:   openapi_types.UUID(item.Ref.ID),
		},
	}
	if item.Summary != "" {
		out.Summary = ptr(item.Summary)
	}
	if len(item.Evidence) > 0 {
		ev := make([]crmcontracts.ContextEvidence, 0, len(item.Evidence))
		for _, e := range item.Evidence {
			ev = append(ev, crmcontracts.ContextEvidence{Snippet: e.Snippet, Source: e.Source})
		}
		out.Evidence = &ev
	}
	return out
}
