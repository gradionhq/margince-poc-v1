package crmcore

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crm-core/internal/store"
	"github.com/gradionhq/margince/backend/kernel/ids"
)

// MergeOrganization: POST /organizations/{id}/merge — merge this org (A,
// the path id) into target_id (B, the survivor). Returns the survivor. The
// store re-homes the hierarchy, deal/partner attributions, and the 1:1
// partner extension; this handler is wire-only.
func (h Handlers) MergeOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.MergeOrganizationParams) {
	var req crmcontracts.MergeOrganizationJSONBody
	if !decode(w, r, &req) {
		return
	}
	survivor, err := h.store.MergeOrganization(r.Context(), ids.UUID(id), ids.UUID(req.TargetId))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, survivor)
}

func (h Handlers) ListOrganizations(w http.ResponseWriter, r *http.Request, params crmcontracts.ListOrganizationsParams) {
	in := store.ListOrganizationsInput{
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
	writeJSON(w, http.StatusOK, crmcontracts.OrganizationListResponse{Data: orgs, Page: pageInfo(page)})
}

func (h Handlers) CreateOrganization(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateOrganizationParams) {
	var req crmcontracts.CreateOrganizationRequest
	if !decode(w, r, &req) {
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
	writeJSON(w, http.StatusCreated, org)
}

func (h Handlers) GetOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	org, err := h.store.GetOrganization(r.Context(), ids.UUID(id), true)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (h Handlers) UpdateOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateOrganizationParams) {
	ifVersion, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateOrganizationRequest
	if !decode(w, r, &req) {
		return
	}

	org, err := h.store.UpdateOrganization(r.Context(), ids.UUID(id), organizationUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (h Handlers) ArchiveOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	org, err := h.store.ArchiveOrganization(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}
