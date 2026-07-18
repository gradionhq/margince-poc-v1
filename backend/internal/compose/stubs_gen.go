// Code generated from internal/contracts/api_gen.go ServerInterface; DO NOT EDIT.
// Regenerate: make gen (tools/gen-stubs).

package compose

import (
	nethttp "net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// stubs satisfies every crmcontracts.ServerInterface operation with an
// explicit 501: the whole contract surface exists from day one, and an
// unimplemented call is loud, never a silent 404. Server embeds stubs
// (one level deep) and module handlers shadow the operations they implement.
type stubs struct{}

var _ crmcontracts.ServerInterface = stubs{}

func (stubs) ListActivities(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListActivitiesParams) {
	httperr.NotImplemented(w, r, "ListActivities")
}

func (stubs) LogActivity(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.LogActivityParams) {
	httperr.NotImplemented(w, r, "LogActivity")
}

func (stubs) ArchiveActivity(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveActivity")
}

func (stubs) GetActivity(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetActivity")
}

func (stubs) UpdateActivity(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateActivityParams) {
	httperr.NotImplemented(w, r, "UpdateActivity")
}

func (stubs) DraftEmail(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DraftEmail")
}

func (stubs) RelinkActivity(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RelinkActivityParams) {
	httperr.NotImplemented(w, r, "RelinkActivity")
}

func (stubs) SendEmail(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.SendEmailParams) {
	httperr.NotImplemented(w, r, "SendEmail")
}

func (stubs) ListAgentTools(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ListAgentTools")
}

func (stubs) ListApprovals(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListApprovalsParams) {
	httperr.NotImplemented(w, r, "ListApprovals")
}

func (stubs) GetApproval(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetApproval")
}

func (stubs) ApproveApproval(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.ApproveApprovalParams) {
	httperr.NotImplemented(w, r, "ApproveApproval")
}

func (stubs) RejectApproval(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "RejectApproval")
}

func (stubs) ListAttachments(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListAttachmentsParams) {
	httperr.NotImplemented(w, r, "ListAttachments")
}

func (stubs) UploadAttachment(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "UploadAttachment")
}

func (stubs) DeleteAttachment(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DeleteAttachment")
}

func (stubs) DownloadAttachment(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DownloadAttachment")
}

func (stubs) GetAttachmentExtraction(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetAttachmentExtraction")
}

func (stubs) AcceptAttachmentExtraction(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "AcceptAttachmentExtraction")
}

func (stubs) RequestAttachmentAccess(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "RequestAttachmentAccess")
}

func (stubs) ListAuditLog(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListAuditLogParams) {
	httperr.NotImplemented(w, r, "ListAuditLog")
}

func (stubs) GetAuthCapabilities(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "GetAuthCapabilities")
}

func (stubs) RequestPasswordReset(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "RequestPasswordReset")
}

func (stubs) Login(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "Login")
}

func (stubs) Logout(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "Logout")
}

func (stubs) ResetPassword(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ResetPassword")
}

func (stubs) ListAutomations(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListAutomationsParams) {
	httperr.NotImplemented(w, r, "ListAutomations")
}

func (stubs) CreateAutomation(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateAutomation")
}

func (stubs) ListAutomationCatalog(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ListAutomationCatalog")
}

func (stubs) DeleteAutomation(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DeleteAutomation")
}

func (stubs) GetAutomation(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetAutomation")
}

func (stubs) UpdateAutomation(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateAutomationParams) {
	httperr.NotImplemented(w, r, "UpdateAutomation")
}

func (stubs) PreviewAutomation(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "PreviewAutomation")
}

func (stubs) ListAutomationRuns(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.ListAutomationRunsParams) {
	httperr.NotImplemented(w, r, "ListAutomationRuns")
}

func (stubs) GetAvailability(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.GetAvailabilityParams) {
	httperr.NotImplemented(w, r, "GetAvailability")
}

func (stubs) BookMeeting(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.BookMeetingParams) {
	httperr.NotImplemented(w, r, "BookMeeting")
}

func (stubs) GetMorningBrief(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "GetMorningBrief")
}

func (stubs) GenerateMorningBrief(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "GenerateMorningBrief")
}

func (stubs) MarkBriefItemActed(w nethttp.ResponseWriter, r *nethttp.Request, itemId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "MarkBriefItemActed")
}

func (stubs) MarkBriefItemDismissed(w nethttp.ResponseWriter, r *nethttp.Request, itemId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "MarkBriefItemDismissed")
}

func (stubs) SnoozeBriefItem(w nethttp.ResponseWriter, r *nethttp.Request, itemId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "SnoozeBriefItem")
}

func (stubs) ListCaptureExclusions(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ListCaptureExclusions")
}

func (stubs) CreateCaptureExclusion(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateCaptureExclusion")
}

func (stubs) DeleteCaptureExclusion(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DeleteCaptureExclusion")
}

func (stubs) ColdStartReadback(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ColdStartReadback")
}

func (stubs) ColdStartPreview(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ColdStartPreview")
}

func (stubs) GetCompany(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "GetCompany")
}

func (stubs) PutCompany(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "PutCompany")
}

func (stubs) ListConnectors(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ListConnectors")
}

func (stubs) ConnectImap(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ConnectImap")
}

func (stubs) CancelConnectorBackfill(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "CancelConnectorBackfill")
}

func (stubs) GetConnectorBackfillStatus(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "GetConnectorBackfillStatus")
}

func (stubs) StartConnectorBackfill(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "StartConnectorBackfill")
}

func (stubs) PreviewConnectorBackfill(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "PreviewConnectorBackfill")
}

func (stubs) ConnectorOAuthCallback(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider, params crmcontracts.ConnectorOAuthCallbackParams) {
	httperr.NotImplemented(w, r, "ConnectorOAuthCallback")
}

func (stubs) ConnectConnector(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "ConnectConnector")
}

func (stubs) DisconnectConnector(w nethttp.ResponseWriter, r *nethttp.Request, provider crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "DisconnectConnector")
}

func (stubs) ListConsentPurposes(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListConsentPurposesParams) {
	httperr.NotImplemented(w, r, "ListConsentPurposes")
}

func (stubs) CreateConsentPurpose(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateConsentPurpose")
}

func (stubs) ListCustomFields(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListCustomFieldsParams) {
	httperr.NotImplemented(w, r, "ListCustomFields")
}

func (stubs) CreateCustomField(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateCustomFieldParams) {
	httperr.NotImplemented(w, r, "CreateCustomField")
}

func (stubs) RenameCustomField(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RenameCustomFieldParams) {
	httperr.NotImplemented(w, r, "RenameCustomField")
}

func (stubs) UpdateCustomFieldOptions(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateCustomFieldOptionsParams) {
	httperr.NotImplemented(w, r, "UpdateCustomFieldOptions")
}

func (stubs) RetireCustomField(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RetireCustomFieldParams) {
	httperr.NotImplemented(w, r, "RetireCustomField")
}

func (stubs) ListDataSubjectRequests(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListDataSubjectRequestsParams) {
	httperr.NotImplemented(w, r, "ListDataSubjectRequests")
}

func (stubs) CreateDataSubjectRequest(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateDataSubjectRequestParams) {
	httperr.NotImplemented(w, r, "CreateDataSubjectRequest")
}

func (stubs) UpdateDataSubjectRequest(w nethttp.ResponseWriter, r *nethttp.Request, id openapi_types.UUID) {
	httperr.NotImplemented(w, r, "UpdateDataSubjectRequest")
}

func (stubs) ListDeals(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListDealsParams) {
	httperr.NotImplemented(w, r, "ListDeals")
}

func (stubs) CreateDeal(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateDealParams) {
	httperr.NotImplemented(w, r, "CreateDeal")
}

func (stubs) ArchiveDeal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveDeal")
}

func (stubs) GetDeal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetDeal")
}

func (stubs) UpdateDeal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateDealParams) {
	httperr.NotImplemented(w, r, "UpdateDeal")
}

func (stubs) AdvanceDeal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.AdvanceDealParams) {
	httperr.NotImplemented(w, r, "AdvanceDeal")
}

func (stubs) ListDealOffers(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.ListDealOffersParams) {
	httperr.NotImplemented(w, r, "ListDealOffers")
}

func (stubs) CreateOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.CreateOfferParams) {
	httperr.NotImplemented(w, r, "CreateOffer")
}

func (stubs) ListDealStakeholders(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ListDealStakeholders")
}

func (stubs) GetMorningDigest(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.GetMorningDigestParams) {
	httperr.NotImplemented(w, r, "GetMorningDigest")
}

func (stubs) CreateFilteredExport(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateFilteredExport")
}

func (stubs) GetFieldHistory(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.GetFieldHistoryParams) {
	httperr.NotImplemented(w, r, "GetFieldHistory")
}

func (stubs) ListLeads(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListLeadsParams) {
	httperr.NotImplemented(w, r, "ListLeads")
}

func (stubs) CreateLead(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateLeadParams) {
	httperr.NotImplemented(w, r, "CreateLead")
}

func (stubs) DisqualifyLead(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DisqualifyLead")
}

func (stubs) GetLead(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetLead")
}

func (stubs) UpdateLead(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateLeadParams) {
	httperr.NotImplemented(w, r, "UpdateLead")
}

func (stubs) PromoteLead(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.PromoteLeadParams) {
	httperr.NotImplemented(w, r, "PromoteLead")
}

func (stubs) ListLists(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListListsParams) {
	httperr.NotImplemented(w, r, "ListLists")
}

func (stubs) CreateList(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateList")
}

func (stubs) ArchiveList(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveList")
}

func (stubs) GetList(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetList")
}

func (stubs) ListListMembers(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.ListListMembersParams) {
	httperr.NotImplemented(w, r, "ListListMembers")
}

func (stubs) AddListMember(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "AddListMember")
}

func (stubs) GetCurrentPrincipal(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "GetCurrentPrincipal")
}

func (stubs) ListOfferTemplates(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListOfferTemplatesParams) {
	httperr.NotImplemented(w, r, "ListOfferTemplates")
}

func (stubs) CreateOfferTemplate(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateOfferTemplateParams) {
	httperr.NotImplemented(w, r, "CreateOfferTemplate")
}

func (stubs) ArchiveOfferTemplate(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveOfferTemplate")
}

func (stubs) GetOfferTemplate(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetOfferTemplate")
}

func (stubs) UpdateOfferTemplate(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateOfferTemplateParams) {
	httperr.NotImplemented(w, r, "UpdateOfferTemplate")
}

func (stubs) ArchiveOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveOffer")
}

func (stubs) GetOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetOffer")
}

func (stubs) UpdateOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateOfferParams) {
	httperr.NotImplemented(w, r, "UpdateOffer")
}

func (stubs) AcceptOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.AcceptOfferParams) {
	httperr.NotImplemented(w, r, "AcceptOffer")
}

func (stubs) AddOfferLineItem(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "AddOfferLineItem")
}

func (stubs) RemoveOfferLineItem(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, lineItemId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "RemoveOfferLineItem")
}

func (stubs) UpdateOfferLineItem(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, lineItemId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "UpdateOfferLineItem")
}

func (stubs) DownloadOfferPdf(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DownloadOfferPdf")
}

func (stubs) RegenerateOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RegenerateOfferParams) {
	httperr.NotImplemented(w, r, "RegenerateOffer")
}

func (stubs) RejectOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RejectOfferParams) {
	httperr.NotImplemented(w, r, "RejectOffer")
}

func (stubs) RenderOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RenderOfferParams) {
	httperr.NotImplemented(w, r, "RenderOffer")
}

func (stubs) SendOffer(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.SendOfferParams) {
	httperr.NotImplemented(w, r, "SendOffer")
}

func (stubs) ListOrganizations(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListOrganizationsParams) {
	httperr.NotImplemented(w, r, "ListOrganizations")
}

func (stubs) CreateOrganization(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateOrganizationParams) {
	httperr.NotImplemented(w, r, "CreateOrganization")
}

func (stubs) ArchiveOrganization(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveOrganization")
}

func (stubs) GetOrganization(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetOrganization")
}

func (stubs) UpdateOrganization(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateOrganizationParams) {
	httperr.NotImplemented(w, r, "UpdateOrganization")
}

func (stubs) DeepReadCompany(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DeepReadCompany")
}

func (stubs) ScrapeCompany(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ScrapeCompany")
}

func (stubs) GetOrganizationHierarchyRollup(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.GetOrganizationHierarchyRollupParams) {
	httperr.NotImplemented(w, r, "GetOrganizationHierarchyRollup")
}

func (stubs) MergeOrganization(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.MergeOrganizationParams) {
	httperr.NotImplemented(w, r, "MergeOrganization")
}

func (stubs) GetPartner(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPartner")
}

func (stubs) UpsertPartner(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpsertPartnerParams) {
	httperr.NotImplemented(w, r, "UpsertPartner")
}

func (stubs) GetSiteRead(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, readId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "GetSiteRead")
}

func (stubs) GetOrganizationStrength(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetOrganizationStrength")
}

func (stubs) ListPartners(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListPartnersParams) {
	httperr.NotImplemented(w, r, "ListPartners")
}

func (stubs) ListPassports(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ListPassports")
}

func (stubs) IssuePassport(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "IssuePassport")
}

func (stubs) RevokePassport(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "RevokePassport")
}

func (stubs) ListPeople(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListPeopleParams) {
	httperr.NotImplemented(w, r, "ListPeople")
}

func (stubs) CreatePerson(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreatePersonParams) {
	httperr.NotImplemented(w, r, "CreatePerson")
}

func (stubs) ArchivePerson(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchivePerson")
}

func (stubs) GetPerson(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPerson")
}

func (stubs) UpdatePerson(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdatePersonParams) {
	httperr.NotImplemented(w, r, "UpdatePerson")
}

func (stubs) GetPersonConsent(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPersonConsent")
}

func (stubs) RecordConsent(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RecordConsentParams) {
	httperr.NotImplemented(w, r, "RecordConsent")
}

func (stubs) IssueDoubleOptIn(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "IssueDoubleOptIn")
}

func (stubs) MergePerson(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.MergePersonParams) {
	httperr.NotImplemented(w, r, "MergePerson")
}

func (stubs) GetPersonStrength(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPersonStrength")
}

func (stubs) ListPipelines(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListPipelinesParams) {
	httperr.NotImplemented(w, r, "ListPipelines")
}

func (stubs) CreatePipeline(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreatePipelineParams) {
	httperr.NotImplemented(w, r, "CreatePipeline")
}

func (stubs) GetPipeline(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPipeline")
}

func (stubs) UpdatePipeline(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdatePipelineParams) {
	httperr.NotImplemented(w, r, "UpdatePipeline")
}

func (stubs) ListProducts(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListProductsParams) {
	httperr.NotImplemented(w, r, "ListProducts")
}

func (stubs) CreateProduct(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateProductParams) {
	httperr.NotImplemented(w, r, "CreateProduct")
}

func (stubs) ArchiveProduct(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveProduct")
}

func (stubs) GetProduct(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetProduct")
}

func (stubs) UpdateProduct(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateProductParams) {
	httperr.NotImplemented(w, r, "UpdateProduct")
}

func (stubs) BookPublicMeeting(w nethttp.ResponseWriter, r *nethttp.Request, hostSlug string, params crmcontracts.BookPublicMeetingParams) {
	httperr.NotImplemented(w, r, "BookPublicMeeting")
}

func (stubs) GetPublicAvailability(w nethttp.ResponseWriter, r *nethttp.Request, hostSlug string, params crmcontracts.GetPublicAvailabilityParams) {
	httperr.NotImplemented(w, r, "GetPublicAvailability")
}

func (stubs) GetPreferenceCenter(w nethttp.ResponseWriter, r *nethttp.Request, token string) {
	httperr.NotImplemented(w, r, "GetPreferenceCenter")
}

func (stubs) UpdatePreferences(w nethttp.ResponseWriter, r *nethttp.Request, token string) {
	httperr.NotImplemented(w, r, "UpdatePreferences")
}

func (stubs) OneClickUnsubscribe(w nethttp.ResponseWriter, r *nethttp.Request, token string, params crmcontracts.OneClickUnsubscribeParams) {
	httperr.NotImplemented(w, r, "OneClickUnsubscribe")
}

func (stubs) ListQuotas(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListQuotasParams) {
	httperr.NotImplemented(w, r, "ListQuotas")
}

func (stubs) CreateQuota(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateQuotaParams) {
	httperr.NotImplemented(w, r, "CreateQuota")
}

func (stubs) ArchiveQuota(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveQuota")
}

func (stubs) GetQuota(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetQuota")
}

func (stubs) UpdateQuota(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateQuotaParams) {
	httperr.NotImplemented(w, r, "UpdateQuota")
}

func (stubs) GetQuotaAttainment(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetQuotaAttainment")
}

func (stubs) ListRecordGrants(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListRecordGrantsParams) {
	httperr.NotImplemented(w, r, "ListRecordGrants")
}

func (stubs) CreateRecordGrant(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateRecordGrantParams) {
	httperr.NotImplemented(w, r, "CreateRecordGrant")
}

func (stubs) RevokeRecordGrant(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RevokeRecordGrantParams) {
	httperr.NotImplemented(w, r, "RevokeRecordGrant")
}

func (stubs) GetRecordContext(w nethttp.ResponseWriter, r *nethttp.Request, entityType string, id crmcontracts.Id, params crmcontracts.GetRecordContextParams) {
	httperr.NotImplemented(w, r, "GetRecordContext")
}

func (stubs) GetRecordHistory(w nethttp.ResponseWriter, r *nethttp.Request, entityType string, id crmcontracts.Id, params crmcontracts.GetRecordHistoryParams) {
	httperr.NotImplemented(w, r, "GetRecordHistory")
}

func (stubs) ListRelationships(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListRelationshipsParams) {
	httperr.NotImplemented(w, r, "ListRelationships")
}

func (stubs) CreateRelationship(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateRelationship")
}

func (stubs) ArchiveRelationship(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveRelationship")
}

func (stubs) UpdateRelationship(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateRelationshipParams) {
	httperr.NotImplemented(w, r, "UpdateRelationship")
}

func (stubs) RunReport(w nethttp.ResponseWriter, r *nethttp.Request, report string) {
	httperr.NotImplemented(w, r, "RunReport")
}

func (stubs) ExplainReport(w nethttp.ResponseWriter, r *nethttp.Request, report string, params crmcontracts.ExplainReportParams) {
	httperr.NotImplemented(w, r, "ExplainReport")
}

func (stubs) Search(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.SearchParams) {
	httperr.NotImplemented(w, r, "Search")
}

func (stubs) ListSignals(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListSignalsParams) {
	httperr.NotImplemented(w, r, "ListSignals")
}

func (stubs) CreateSignal(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateSignalParams) {
	httperr.NotImplemented(w, r, "CreateSignal")
}

func (stubs) ArchiveSignal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveSignal")
}

func (stubs) GetSignal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetSignal")
}

func (stubs) UpdateSignal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateSignalParams) {
	httperr.NotImplemented(w, r, "UpdateSignal")
}

func (stubs) GetSignalIntroPath(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetSignalIntroPath")
}

func (stubs) ResolveSignal(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.ResolveSignalParams) {
	httperr.NotImplemented(w, r, "ResolveSignal")
}

func (stubs) GetSignalWarmth(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetSignalWarmth")
}

func (stubs) ListStages(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListStagesParams) {
	httperr.NotImplemented(w, r, "ListStages")
}

func (stubs) CreateStage(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateStageParams) {
	httperr.NotImplemented(w, r, "CreateStage")
}

func (stubs) GetStage(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetStage")
}

func (stubs) UpdateStage(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateStageParams) {
	httperr.NotImplemented(w, r, "UpdateStage")
}

func (stubs) ListTags(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListTagsParams) {
	httperr.NotImplemented(w, r, "ListTags")
}

func (stubs) CreateTag(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateTag")
}

func (stubs) ArchiveTag(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveTag")
}

func (stubs) ApplyTag(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ApplyTag")
}

func (stubs) ListTeams(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListTeamsParams) {
	httperr.NotImplemented(w, r, "ListTeams")
}

func (stubs) ListUsers(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListUsersParams) {
	httperr.NotImplemented(w, r, "ListUsers")
}

func (stubs) ListSavedViews(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListSavedViewsParams) {
	httperr.NotImplemented(w, r, "ListSavedViews")
}

func (stubs) CreateSavedView(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateSavedView")
}

func (stubs) ArchiveSavedView(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveSavedView")
}

func (stubs) GetSavedView(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetSavedView")
}

func (stubs) UpdateSavedView(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateSavedViewParams) {
	httperr.NotImplemented(w, r, "UpdateSavedView")
}

func (stubs) ListVoiceProfiles(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListVoiceProfilesParams) {
	httperr.NotImplemented(w, r, "ListVoiceProfiles")
}

func (stubs) CreateVoiceProfile(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateVoiceProfile")
}

func (stubs) DeleteVoiceProfile(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "DeleteVoiceProfile")
}

func (stubs) GetVoiceProfile(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetVoiceProfile")
}

func (stubs) UpdateVoiceProfile(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpdateVoiceProfileParams) {
	httperr.NotImplemented(w, r, "UpdateVoiceProfile")
}

func (stubs) ListVoiceCorpusSources(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ListVoiceCorpusSources")
}

func (stubs) IngestVoiceCorpusSource(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "IngestVoiceCorpusSource")
}

func (stubs) UpdateVoiceCorpusSource(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, sourceId openapi_types.UUID) {
	httperr.NotImplemented(w, r, "UpdateVoiceCorpusSource")
}
