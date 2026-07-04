package crmcore

// Contract request → store input mappings, in ONE place: the HTTP
// handlers and the SoR provider (the MCP surface's door) both decode the
// same crm.yaml shapes, and a defaulting rule that lived in only one of
// them would make the two surfaces silently disagree.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/crm-core/internal/store"
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

func personCreateInput(req crmcontracts.CreatePersonRequest) (store.CreatePersonInput, error) {
	if req.FullName == "" {
		return store.CreatePersonInput{}, &RequiredFieldError{Field: "full_name"}
	}
	in := store.CreatePersonInput{
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
	if req.Emails != nil {
		for i, e := range *req.Emails {
			email := store.PersonEmailInput{Email: string(e.Email), EmailType: "work", Position: i}
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
			phone := store.PersonPhoneInput{Phone: p.Phone, PhoneType: "work", Position: i}
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

func personUpdateInput(req crmcontracts.UpdatePersonRequest, ifVersion *int64) store.UpdatePersonInput {
	in := store.UpdatePersonInput{
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
	return in
}

func organizationCreateInput(req crmcontracts.CreateOrganizationRequest) (store.CreateOrganizationInput, error) {
	if req.DisplayName == "" {
		return store.CreateOrganizationInput{}, &RequiredFieldError{Field: "display_name"}
	}
	in := store.CreateOrganizationInput{
		DisplayName: req.DisplayName,
		LegalName:   req.LegalName,
		Industry:    req.Industry,
		Source:      req.Source,
		OwnerID:     uuidArg(req.OwnerId),
		ParentOrgID: uuidArg(req.ParentOrgId),
	}
	if req.SizeBand != nil {
		band := string(*req.SizeBand)
		in.SizeBand = &band
	}
	if req.Domains != nil {
		for _, d := range *req.Domains {
			in.Domains = append(in.Domains, store.OrgDomainInput{
				Domain:    d.Domain,
				IsPrimary: d.IsPrimary != nil && *d.IsPrimary,
			})
		}
	}
	return in, nil
}

func organizationUpdateInput(req crmcontracts.UpdateOrganizationRequest, ifVersion *int64) store.UpdateOrganizationInput {
	in := store.UpdateOrganizationInput{
		DisplayName: req.DisplayName,
		LegalName:   req.LegalName,
		Industry:    req.Industry,
		OwnerID:     uuidArg(req.OwnerId),
		ParentOrgID: uuidArg(req.ParentOrgId),
		IfVersion:   ifVersion,
	}
	if req.SizeBand != nil {
		band := string(*req.SizeBand)
		in.SizeBand = &band
	}
	return in
}

func dealCreateInput(req crmcontracts.CreateDealRequest) (store.CreateDealInput, error) {
	if req.Name == "" {
		return store.CreateDealInput{}, &RequiredFieldError{Field: "name"}
	}
	in := store.CreateDealInput{
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

func dealUpdateInput(req crmcontracts.UpdateDealRequest, ifVersion *int64) store.UpdateDealInput {
	in := store.UpdateDealInput{
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

func leadCreateInput(req crmcontracts.CreateLeadRequest) store.CreateLeadInput {
	in := store.CreateLeadInput{
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

func leadUpdateInput(req crmcontracts.UpdateLeadRequest, ifVersion *int64) store.UpdateLeadInput {
	in := store.UpdateLeadInput{
		FullName:        req.FullName,
		Title:           req.Title,
		CompanyName:     req.CompanyName,
		CandidateOrgKey: req.CandidateOrgKey,
		Score:           req.Score,
		OwnerID:         uuidArg(req.OwnerId),
		IfVersion:       ifVersion,
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

func activityLogInput(req crmcontracts.CreateActivityRequest) (store.LogActivityInput, error) {
	if req.Kind == "" {
		return store.LogActivityInput{}, &RequiredFieldError{Field: "kind"}
	}
	in := store.LogActivityInput{
		Kind:         string(req.Kind),
		Subject:      req.Subject,
		Body:         req.Body,
		OccurredAt:   req.OccurredAt,
		DueAt:        req.DueAt,
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
			in.Links = append(in.Links, store.ActivityLinkInput{
				EntityType: string(link.EntityType),
				EntityID:   ids.UUID(link.EntityId),
			})
		}
	}
	return in, nil
}
