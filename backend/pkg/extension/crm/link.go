// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package crm

// LinkTo returns the request with one more subject linked to it.
//
// It exists because the contract declares an activity's links inline, so the
// generated field is a slice of an ANONYMOUS struct: a unit adding a link
// without this would have to re-spell that struct — field names, types and json
// tags — at every call site, and get all three exactly right for the assignment
// to compile. That is a cost the published surface should pay once.
//
// Written by hand beside the generated file rather than generated with it: it
// is a convenience over the contract, not a shape the contract declares, and a
// generator that invented helpers would be inventing surface.
func (r CreateActivityRequest) LinkTo(entityType CreateActivityRequestLinksEntityType, entityID string) CreateActivityRequest {
	// CLONED, not appended to in place. The receiver is a value, so a caller
	// reasonably reads two LinkTo calls off one base request as two independent
	// requests — and an append into shared spare capacity would have the second
	// overwrite the link the first is still holding.
	links := []struct {
		EntityId   string                               `json:"entity_id"` //nolint:staticcheck // the generated field's own name; renaming breaks the assignment
		EntityType CreateActivityRequestLinksEntityType `json:"entity_type"`
	}{}
	if r.Links != nil {
		links = append(links, *r.Links...)
	}
	links = append(links, struct {
		EntityId   string                               `json:"entity_id"` //nolint:staticcheck // the generated field's own name; renaming breaks the assignment
		EntityType CreateActivityRequestLinksEntityType `json:"entity_type"`
	}{EntityId: entityID, EntityType: entityType})
	r.Links = &links
	return r
}
