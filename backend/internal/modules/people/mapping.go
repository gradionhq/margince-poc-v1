// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

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

func personCreateInput(req crmcontracts.CreatePersonRequest) (CreatePersonInput, error) {
	if req.FullName == "" {
		return CreatePersonInput{}, &RequiredFieldError{Field: "full_name"}
	}
	in := CreatePersonInput{
		FullName:  req.FullName,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Title:     req.Title,
		Source:    req.Source,
		OwnerID:   uuidArg(req.OwnerId),
	}
	if req.Social != nil {
		in.Social = *req.Social
	}
	in.Address = req.Address
	if req.Emails != nil {
		for i, e := range *req.Emails {
			email := PersonEmailInput{Email: string(e.Email), EmailType: "work", Position: i}
			if e.EmailType != nil {
				email.EmailType = string(*e.EmailType)
			}
			if e.IsPrimary != nil {
				email.IsPrimary = *e.IsPrimary
			}
			if e.Position != nil {
				email.Position = *e.Position
			}
			in.Emails = append(in.Emails, email)
		}
	}
	if req.Phones != nil {
		for i, p := range *req.Phones {
			phone := PersonPhoneInput{Phone: p.Phone, PhoneType: "work", Position: i}
			if p.PhoneType != nil {
				phone.PhoneType = string(*p.PhoneType)
			}
			if p.IsPrimary != nil {
				phone.IsPrimary = *p.IsPrimary
			}
			if p.Position != nil {
				phone.Position = *p.Position
			}
			in.Phones = append(in.Phones, phone)
		}
	}
	return in, nil
}

func personUpdateInput(req crmcontracts.UpdatePersonRequest, ifVersion *int64) UpdatePersonInput {
	in := UpdatePersonInput{
		FullName:  req.FullName,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Title:     req.Title,
		OwnerID:   uuidArg(req.OwnerId),
		IfVersion: ifVersion,
	}
	if req.Social != nil {
		in.Social = *req.Social
	}
	in.Address = req.Address
	return in
}

func organizationCreateInput(req crmcontracts.CreateOrganizationRequest) (CreateOrganizationInput, error) {
	if req.DisplayName == "" {
		return CreateOrganizationInput{}, &RequiredFieldError{Field: "display_name"}
	}
	in := CreateOrganizationInput{
		DisplayName: req.DisplayName,
		LegalName:   req.LegalName,
		Industry:    req.Industry,
		Source:      req.Source,
		OwnerID:     uuidArg(req.OwnerId),
		ParentOrgID: uuidArg(req.ParentOrgId),
	}
	in.Address = req.Address
	if req.SizeBand != nil {
		band := string(*req.SizeBand)
		in.SizeBand = &band
	}
	if req.Domains != nil {
		for _, d := range *req.Domains {
			in.Domains = append(in.Domains, OrgDomainInput{
				Domain:    d.Domain,
				IsPrimary: d.IsPrimary != nil && *d.IsPrimary,
			})
		}
	}
	return in, nil
}

func organizationUpdateInput(req crmcontracts.UpdateOrganizationRequest, ifVersion *int64) UpdateOrganizationInput {
	in := UpdateOrganizationInput{
		DisplayName: req.DisplayName,
		LegalName:   req.LegalName,
		Industry:    req.Industry,
		OwnerID:     uuidArg(req.OwnerId),
		ParentOrgID: uuidArg(req.ParentOrgId),
		IfVersion:   ifVersion,
	}
	in.Address = req.Address
	if req.SizeBand != nil {
		band := string(*req.SizeBand)
		in.SizeBand = &band
	}
	return in
}
func leadCreateInput(req crmcontracts.CreateLeadRequest) CreateLeadInput {
	in := CreateLeadInput{
		FullName:        req.FullName,
		Title:           req.Title,
		CompanyName:     req.CompanyName,
		CandidateOrgKey: req.CandidateOrgKey,
		SourceSystem:    req.SourceSystem,
		SourceID:        req.SourceId,
		Source:          req.Source,
		OwnerID:         uuidArg(req.OwnerId),
	}
	if req.Email != nil {
		email := string(*req.Email)
		in.Email = &email
	}
	if req.Status != nil {
		in.Status = string(*req.Status)
	}
	return in
}

func leadUpdateInput(req crmcontracts.UpdateLeadRequest, ifVersion *int64) UpdateLeadInput {
	in := UpdateLeadInput{
		FullName:            req.FullName,
		Title:               req.Title,
		CompanyName:         req.CompanyName,
		CandidateOrgKey:     req.CandidateOrgKey,
		Score:               req.Score,
		ScoreOverrideReason: req.ScoreOverrideReason,
		OwnerID:             uuidArg(req.OwnerId),
		IfVersion:           ifVersion,
	}
	if req.Email != nil {
		email := string(*req.Email)
		in.Email = &email
	}
	if req.Status != nil {
		s := string(*req.Status)
		in.Status = &s
	}
	return in
}
