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
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
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
// The rule, the message and the wire shape live in ONE place for the whole tree
// (httperr.RequireBodyID) — eight modules need it, and eleven copies of the same
// three lines is eleven places for one refusal to be spelled differently. This
// stays as the module's local name for it because it is what converts the
// generated contract type: httperr deliberately does not import the contracts.
//
// RequiredFieldError is still the module's own error for a required non-id FIELD
// (a deal without a name, a line item without a description) — a different claim
// with a different reader.
func requireBodyID(field string, id openapi_types.UUID) error {
	return httperr.RequireBodyID(field, ids.UUID(id))
}

// advanceDealInput maps the advance body onto the store input.
//
// It exists so the guard sits in a mapping both transports could share rather
// than inside the HTTP handler. The MCP twin (advance_deal) is guarded at
// Registry.Invoke and the agent gate resolves this tool's tier before the handler
// runs, so `to_stage_id` has three readers — and one rule with three spellings is
// three chances for a caller to be told something different for one mistake.
func advanceDealInput(req crmcontracts.AdvanceDealRequest, ifVersion *int64) (AdvanceDealInput, error) {
	// Unchecked, the zero UUID travels to the stage lookup, whose composite
	// WHERE matches nothing and answers a bare not-found — for a deal that is in
	// the URL and exists, sending the caller hunting for a stage it never named.
	if err := requireBodyID("to_stage_id", req.ToStageId); err != nil {
		return AdvanceDealInput{}, err
	}
	return AdvanceDealInput{
		ToStageID:  pathID[ids.StageKind](req.ToStageId),
		LostReason: req.LostReason,
		IfVersion:  ifVersion,
	}, nil
}

// stageCreateInput maps the create-stage body onto the store input. A stage
// without a pipeline has nowhere to be: unguarded, the zero UUID reaches the
// pipeline probe and answers not-found for a pipeline the caller never named.
func stageCreateInput(req crmcontracts.CreateStageRequest) (CreateStageInput, error) {
	if err := requireBodyID("pipeline_id", req.PipelineId); err != nil {
		return CreateStageInput{}, err
	}
	in := CreateStageInput{
		PipelineID:     pathID[ids.PipelineKind](req.PipelineId),
		Name:           req.Name,
		Position:       req.Position,
		WinProbability: req.WinProbability,
	}
	if req.Semantic != nil {
		in.Semantic = string(*req.Semantic)
	}
	return in, nil
}

func dealCreateInput(req crmcontracts.CreateDealRequest) (CreateDealInput, error) {
	if req.Name == "" {
		return CreateDealInput{}, &RequiredFieldError{Field: "name"}
	}
	// Provenance first, and before the structural checks below: a caller writing
	// the importer's namespace is claiming to BE the importer, and that is refused
	// on the attempt rather than only on an otherwise-complete body. Answering
	// "pipeline_id is required" to a forged-provenance write would tell the caller
	// how to make the forgery land.
	if err := provenance.Refuse("source", req.Source); err != nil {
		return CreateDealInput{}, err
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
