// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Contract request → store input mappings, in ONE place: the HTTP
// handlers and the SoR provider (the MCP surface's door) both decode the
// same crm.yaml shapes, and a defaulting rule that lived in only one of
// them would make the two surfaces silently disagree.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// RequiredFieldError maps to 422 on both surfaces.
type RequiredFieldError struct{ Field string }

func (e *RequiredFieldError) Error() string { return e.Field + " is required" }

// pathID asserts a contract path id as entity K's id — the widening
// point between the wire and the typed store surface (the route already
// names the entity, so the assertion lives here, not in the store).
func pathID[K ids.EntityKind](id crmcontracts.Id) ids.ID[K] {
	return ids.From[K](ids.UUID(id))
}

// idArg asserts an optional wire UUID (body field or query parameter)
// as entity K's id; nil stays nil.
func idArg[K ids.EntityKind](u *openapi_types.UUID) *ids.ID[K] {
	if u == nil {
		return nil
	}
	v := ids.From[K](ids.UUID(*u))
	return &v
}

func activityLogInput(req crmcontracts.CreateActivityRequest) (LogActivityInput, error) {
	if req.Kind == "" {
		return LogActivityInput{}, &RequiredFieldError{Field: "kind"}
	}
	in := LogActivityInput{
		Kind:         string(req.Kind),
		Subject:      req.Subject,
		Body:         req.Body,
		OccurredAt:   req.OccurredAt,
		DueAt:        req.DueAt,
		RemindAt:     req.RemindAt,
		SourceSystem: req.SourceSystem,
		SourceID:     req.SourceId,
		Source:       req.Source,
		AssigneeID:   idArg[ids.UserKind](req.AssigneeId),
	}
	if req.Direction != nil {
		d := string(*req.Direction)
		in.Direction = &d
	}
	if req.Links != nil {
		for _, link := range *req.Links {
			in.Links = append(in.Links, ActivityLinkInput{
				EntityType: string(link.EntityType),
				EntityID:   ids.UUID(link.EntityId),
			})
		}
	}
	return in, nil
}
