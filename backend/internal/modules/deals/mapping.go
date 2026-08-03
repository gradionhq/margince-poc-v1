// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

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

// FieldFault names the missing required field, on every surface.
func (e *RequiredFieldError) FieldFault() (field, code, message string) {
	return e.Field, "required", e.Error()
}

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

// requireBodyID refuses a required non-pointer id the body simply omitted.
//
// A required id is generated as a NON-POINTER openapi_types.UUID, so an absent
// key decodes to the zero UUID with no error — "required" in the contract is a
// claim only this check makes true. What made that worth a named helper is
// where the zero value lands: it reaches a lookup that matches nothing and
// comes back as a bare not-found, which names no argument on either surface. A
// caller is then told the record it never mentioned does not exist.
func requireBodyID(field string, id openapi_types.UUID) error {
	if ids.UUID(id).IsZero() {
		return &RequiredFieldError{Field: field}
	}
	return nil
}

func dealCreateInput(req crmcontracts.CreateDealRequest) (CreateDealInput, error) {
	if req.Name == "" {
		return CreateDealInput{}, &RequiredFieldError{Field: "name"}
	}
	// A deal is born INTO a stage of a pipeline, and neither is defaultable here:
	// which pipeline a workspace means is a config question, and guessing would
	// file deals somewhere nobody chose. Unchecked, both zero UUIDs travel to
	// ensureOpenBirthStage, whose composite lookup answers a bare ErrNotFound
	// naming neither.
	if err := requireBodyID("pipeline_id", req.PipelineId); err != nil {
		return CreateDealInput{}, err
	}
	if err := requireBodyID("stage_id", req.StageId); err != nil {
		return CreateDealInput{}, err
	}
	in := CreateDealInput{
		Name:           req.Name,
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
		PipelineID:     pathID[ids.PipelineKind](req.PipelineId),
		StageID:        pathID[ids.StageKind](req.StageId),
		Source:         req.Source,
		OrganizationID: idArg[ids.OrganizationKind](req.OrganizationId),
		ProjectID:      idArg[ids.ProjectKind](req.ProjectId),
		OwnerID:        idArg[ids.UserKind](req.OwnerId),
		CustomFields:   req.AdditionalProperties,
	}
	if req.ExpectedCloseDate != nil {
		in.ExpectedClose = &req.ExpectedCloseDate.Time
	}
	return in, nil
}

func dealUpdateInput(req crmcontracts.UpdateDealRequest, ifVersion *int64) UpdateDealInput {
	in := UpdateDealInput{
		Name:                  req.Name,
		AmountMinor:           req.AmountMinor,
		Currency:              req.Currency,
		OrganizationID:        idArg[ids.OrganizationKind](req.OrganizationId),
		ProjectID:             idArg[ids.ProjectKind](req.ProjectId),
		OwnerID:               idArg[ids.UserKind](req.OwnerId),
		PartnerOrganizationID: idArg[ids.OrganizationKind](req.PartnerOrgId),
		IfVersion:             ifVersion,
		CustomFields:          req.AdditionalProperties,
	}
	if req.ExpectedCloseDate != nil {
		in.ExpectedClose = &req.ExpectedCloseDate.Time
	}
	if req.ForecastCategory != nil {
		cat := string(*req.ForecastCategory)
		in.ForecastCategory = &cat
	}
	if req.WaitUntil != nil {
		in.WaitUntil = &req.WaitUntil.Time
	}
	return in
}
