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

func uuidArg(u *openapi_types.UUID) *ids.UUID {
	if u == nil {
		return nil
	}
	v := ids.UUID(*u)
	return &v
}

func dealCreateInput(req crmcontracts.CreateDealRequest) (CreateDealInput, error) {
	if req.Name == "" {
		return CreateDealInput{}, &RequiredFieldError{Field: "name"}
	}
	in := CreateDealInput{
		Name:           req.Name,
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
		PipelineID:     ids.UUID(req.PipelineId),
		StageID:        ids.UUID(req.StageId),
		Source:         req.Source,
		OrganizationID: uuidArg(req.OrganizationId),
		OwnerID:        uuidArg(req.OwnerId),
	}
	if req.ExpectedCloseDate != nil {
		in.ExpectedClose = &req.ExpectedCloseDate.Time
	}
	return in, nil
}

func dealUpdateInput(req crmcontracts.UpdateDealRequest, ifVersion *int64) UpdateDealInput {
	in := UpdateDealInput{
		Name:           req.Name,
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
		OrganizationID: uuidArg(req.OrganizationId),
		OwnerID:        uuidArg(req.OwnerId),
		PartnerOrgID:   uuidArg(req.PartnerOrgId),
		IfVersion:      ifVersion,
	}
	if req.ExpectedCloseDate != nil {
		in.ExpectedClose = &req.ExpectedCloseDate.Time
	}
	if req.ForecastCategory != nil {
		cat := string(*req.ForecastCategory)
		in.ForecastCat = &cat
	}
	if req.WaitUntil != nil {
		in.WaitUntil = &req.WaitUntil.Time
	}
	return in
}
