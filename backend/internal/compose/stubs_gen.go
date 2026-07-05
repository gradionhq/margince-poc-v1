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

func (stubs) ListAuditLog(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListAuditLogParams) {
	httperr.NotImplemented(w, r, "ListAuditLog")
}

func (stubs) Login(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "Login")
}

func (stubs) Logout(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "Logout")
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

func (stubs) GetAvailability(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.GetAvailabilityParams) {
	httperr.NotImplemented(w, r, "GetAvailability")
}

func (stubs) BookMeeting(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.BookMeetingParams) {
	httperr.NotImplemented(w, r, "BookMeeting")
}

func (stubs) ColdStartReadback(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "ColdStartReadback")
}

func (stubs) ListConsentPurposes(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListConsentPurposesParams) {
	httperr.NotImplemented(w, r, "ListConsentPurposes")
}

func (stubs) CreateConsentPurpose(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateConsentPurpose")
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

func (stubs) ListDealStakeholders(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ListDealStakeholders")
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

func (stubs) MergeOrganization(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.MergeOrganizationParams) {
	httperr.NotImplemented(w, r, "MergeOrganization")
}

func (stubs) GetPartner(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetPartner")
}

func (stubs) UpsertPartner(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.UpsertPartnerParams) {
	httperr.NotImplemented(w, r, "UpsertPartner")
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

func (stubs) ListRecordGrants(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.ListRecordGrantsParams) {
	httperr.NotImplemented(w, r, "ListRecordGrants")
}

func (stubs) CreateRecordGrant(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.CreateRecordGrantParams) {
	httperr.NotImplemented(w, r, "CreateRecordGrant")
}

func (stubs) RevokeRecordGrant(w nethttp.ResponseWriter, r *nethttp.Request, id crmcontracts.Id, params crmcontracts.RevokeRecordGrantParams) {
	httperr.NotImplemented(w, r, "RevokeRecordGrant")
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

func (stubs) Search(w nethttp.ResponseWriter, r *nethttp.Request, params crmcontracts.SearchParams) {
	httperr.NotImplemented(w, r, "Search")
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

func (stubs) BootstrapWorkspace(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "BootstrapWorkspace")
}
