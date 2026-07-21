// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// MergeOrganization: POST /organizations/{id}/merge — merge this org (A,
// the path id) into target_id (B, the survivor). Returns the survivor. The
// store re-homes the hierarchy, deal/partner attributions, and the 1:1
// partner extension; this handler is wire-only.
func (h Handlers) MergeOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.MergeOrganizationParams) {
	var req crmcontracts.MergeOrganizationJSONBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	survivor, err := h.store.MergeOrganization(r.Context(), pathID[ids.OrganizationKind](id), ids.From[ids.OrganizationKind](ids.UUID(req.TargetId)))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, survivor)
}

func (h Handlers) ListOrganizations(w http.ResponseWriter, r *http.Request, params crmcontracts.ListOrganizationsParams) {
	in := ListOrganizationsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Query:           params.Q,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
		Sort:            params.Sort,
		CustomFilters:   httperr.CustomFieldFilters(r),
	}
	in.OwnerID = idArg[ids.UserKind](params.OwnerId)

	orgs, page, err := h.store.ListOrganizations(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.OrganizationListResponse{Data: orgs, Page: pageInfo(page)})
}

func (h Handlers) CreateOrganization(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateOrganizationParams) {
	var req crmcontracts.CreateOrganizationRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := organizationCreateInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	org, err := h.store.CreateOrganization(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/organizations/"+org.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, org)
}

func (h Handlers) GetOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	org, err := h.store.GetOrganization(r.Context(), pathID[ids.OrganizationKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, org)
}

func (h Handlers) UpdateOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateOrganizationParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateOrganizationRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	org, err := h.store.UpdateOrganization(r.Context(), pathID[ids.OrganizationKind](id), organizationUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, org)
}

// ListOrganizationFacts serves GET /organizations/{id}/facts — the org's
// confirmed evidence-backed facts, row-scoped. Empty is honest ([]).
func (h Handlers) ListOrganizationFacts(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	facts, err := h.store.ListOrganizationFacts(r.Context(), pathID[ids.OrganizationKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if facts == nil {
		facts = []crmcontracts.OrganizationFact{}
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.OrganizationFactListResponse{Data: facts})
}

// ListOrganizationProfileFields serves GET /organizations/{id}/profile-fields
// — the org's confirmed profile fields, row-scoped. Empty is honest ([]).
func (h Handlers) ListOrganizationProfileFields(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	fields, err := h.store.ListOrganizationProfileFields(r.Context(), pathID[ids.OrganizationKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if fields == nil {
		fields = []crmcontracts.CompanyProfileField{}
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.OrganizationProfileFieldListResponse{Data: fields})
}

func (h Handlers) ArchiveOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	org, err := h.store.ArchiveOrganization(r.Context(), pathID[ids.OrganizationKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, org)
}
