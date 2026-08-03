// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Contract request → store input, and store row → contract response, for
// relationship edges — in ONE place, because two surfaces now decode these
// shapes: the HTTP handlers and the SoR provider the MCP tool surface comes
// through. While the mapping was inline in the handler there was only one
// caller and one place to be wrong; there are two now, and a defaulting rule in
// only one of them makes the surfaces silently disagree.
//
// Extracting it found a defect that had been live on the REST path the whole
// time: the create handler never read `project_id`, and the response never
// rendered it. So `POST /v1/relationships` with kind=project_stakeholder — a
// member of the contract's own kind enum — lost its project endpoint on the way
// in and died on rel_project_stakeholder_shape, blaming a pair the caller had
// supplied. Copying that into the provider would have shipped it twice.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// relationshipCreateInput maps the contract create body onto the store input.
// Every endpoint the body declares travels, including project_id: which pair a
// kind needs is the rel_*_shape CHECKs' rule, and dropping a field here turns
// their refusal into a lie about what the caller sent.
func relationshipCreateInput(req crmcontracts.CreateRelationshipRequest) CreateRelationshipInput {
	in := CreateRelationshipInput{
		Kind:              string(req.Kind),
		Role:              req.Role,
		Source:            req.Source,
		IsCurrentPrimary:  req.IsCurrentPrimary != nil && *req.IsCurrentPrimary,
		PersonID:          idArg[ids.PersonKind](req.PersonId),
		OrganizationID:    idArg[ids.OrganizationKind](req.OrganizationId),
		CounterpartyOrgID: idArg[ids.OrganizationKind](req.CounterpartyOrgId),
		DealID:            idArg[ids.DealKind](req.DealId),
		ProjectID:         idArg[ids.ProjectKind](req.ProjectId),
	}
	// The two dates are wire DATES and store timestamps, so each needs its
	// Time lifted out; a nil date stays nil, which the store reads as "not
	// stated" rather than as the zero instant.
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.EndedAt != nil {
		in.EndedAt = &req.EndedAt.Time
	}
	return in
}

// relationshipUpdateInput maps the contract patch onto the store input. The
// patch carries no endpoints at all — an edge's ends are what it IS, so moving
// one is a new edge and an archive, never an update — which is why this takes
// the version pin the create mapper has no use for.
func relationshipUpdateInput(req crmcontracts.UpdateRelationshipRequest, ifVersion *int64) UpdateRelationshipInput {
	in := UpdateRelationshipInput{
		Role:             req.Role,
		IsCurrentPrimary: req.IsCurrentPrimary,
		IfVersion:        ifVersion,
	}
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.EndedAt != nil {
		in.EndedAt = &req.EndedAt.Time
	}
	return in
}

// wireRelationship renders one edge for the wire. Every endpoint column the row
// holds is rendered, project_id included: an edge that read back without the
// endpoint that defines it left both surfaces describing a
// project_stakeholder seat that named no project.
func wireRelationship(rel relationshipRow) crmcontracts.Relationship {
	out := crmcontracts.Relationship{
		Id:          openapi_types.UUID(rel.ID),
		WorkspaceId: openapi_types.UUID(rel.WorkspaceID.UUID),
		Kind:        crmcontracts.RelationshipKind(rel.Kind),
		Source:      rel.Source,
		CapturedBy:  &rel.CapturedBy,
		CreatedAt:   rel.CreatedAt,
		UpdatedAt:   rel.UpdatedAt,
		ArchivedAt:  rel.ArchivedAt,
		Role:        rel.Role,
	}
	version := crmcontracts.RowVersion(rel.Version)
	out.Version = &version
	out.IsCurrentPrimary = &rel.IsCurrentPrimary
	out.PersonId = uuidPtr(untypedPtr(rel.PersonID))
	out.OrganizationId = uuidPtr(untypedPtr(rel.OrganizationID))
	out.CounterpartyOrgId = uuidPtr(untypedPtr(rel.CounterpartyOrgID))
	out.DealId = uuidPtr(untypedPtr(rel.DealID))
	out.ProjectId = uuidPtr(untypedPtr(rel.ProjectID))
	if rel.StartedAt != nil {
		out.StartedAt = &openapi_types.Date{Time: *rel.StartedAt}
	}
	if rel.EndedAt != nil {
		out.EndedAt = &openapi_types.Date{Time: *rel.EndedAt}
	}
	return out
}
