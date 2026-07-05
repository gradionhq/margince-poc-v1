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
	survivor, err := h.store.MergeOrganization(r.Context(), ids.UUID(id), ids.UUID(req.TargetId))
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
	}
	if params.OwnerId != nil {
		owner := ids.UUID(*params.OwnerId)
		in.OwnerID = &owner
	}

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
	org, err := h.store.GetOrganization(r.Context(), ids.UUID(id), storekit.IncludeArchived)
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

	org, err := h.store.UpdateOrganization(r.Context(), ids.UUID(id), organizationUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, org)
}

func (h Handlers) ArchiveOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	org, err := h.store.ArchiveOrganization(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, org)
}
