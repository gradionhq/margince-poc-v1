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

func uuidArg(u *openapi_types.UUID) *ids.UUID {
	if u == nil {
		return nil
	}
	v := ids.UUID(*u)
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
		AssigneeID:   uuidArg(req.AssigneeId),
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
