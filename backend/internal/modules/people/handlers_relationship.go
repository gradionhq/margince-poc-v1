package people

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListRelationships(w http.ResponseWriter, r *http.Request, params crmcontracts.ListRelationshipsParams) {
	in := ListRelationshipsInput{
		Limit:           params.Limit,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	if params.Kind != nil {
		kind := string(*params.Kind)
		in.Kind = &kind
	}
	in.PersonID = uuidArg(params.PersonId)
	in.OrganizationID = uuidArg(params.OrganizationId)
	in.DealID = uuidArg(params.DealId)
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	rels, page, err := h.store.ListRelationships(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Relationship, 0, len(rels))
	for _, rel := range rels {
		data = append(data, wireRelationship(rel))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RelationshipListResponse{Data: data, Page: pageInfo(page)})
}

func (h Handlers) CreateRelationship(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateRelationshipRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateRelationshipInput{
		Kind:              string(req.Kind),
		Role:              req.Role,
		Source:            req.Source,
		IsCurrentPrimary:  req.IsCurrentPrimary != nil && *req.IsCurrentPrimary,
		PersonID:          uuidArg(req.PersonId),
		OrganizationID:    uuidArg(req.OrganizationId),
		CounterpartyOrgID: uuidArg(req.CounterpartyOrgId),
		DealID:            uuidArg(req.DealId),
	}
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.EndedAt != nil {
		in.EndedAt = &req.EndedAt.Time
	}
	rel, err := h.store.CreateRelationship(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireRelationship(rel))
}

func (h Handlers) UpdateRelationship(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateRelationshipParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateRelationshipRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateRelationshipInput{
		Role:             req.Role,
		IsCurrentPrimary: req.IsCurrentPrimary,
		IfVersion:        ifVersion,
	}
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.EndedAt != nil {
		in.EndedAt = &req.EndedAt.Time
	}
	rel, err := h.store.UpdateRelationship(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireRelationship(rel))
}

func (h Handlers) ArchiveRelationship(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	rel, err := h.store.ArchiveRelationship(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireRelationship(rel))
}

func (h Handlers) UpsertPartner(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpsertPartnerParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpsertPartnerRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpsertPartnerInput{
		OrganizationID: ids.UUID(id),
		PartnerRole:    string(req.PartnerRole),
		IfVersion:      ifVersion,
	}
	if req.CertStatus != nil {
		status := string(*req.CertStatus)
		in.CertStatus = &status
	}
	if req.MarginTier != nil {
		tier := string(*req.MarginTier)
		in.MarginTier = &tier
	}
	if req.GateMetrics != nil {
		if staff, ok := (*req.GateMetrics)["certified_staff"].(float64); ok {
			v := int16(staff)
			in.CertifiedStaff = &v
		}
		if rate, ok := (*req.GateMetrics)["retention_rate"].(float64); ok {
			v := int16(rate)
			in.RetentionRate = &v
		}
	}
	partner, err := h.store.UpsertPartner(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wirePartner(partner))
}

func (h Handlers) GetPartner(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	partner, err := h.store.GetPartner(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wirePartner(partner))
}

func (h Handlers) ListPartners(w http.ResponseWriter, r *http.Request, params crmcontracts.ListPartnersParams) {
	in := ListPartnersInput{Limit: params.Limit}
	if params.PartnerRole != nil {
		role := string(*params.PartnerRole)
		in.PartnerRole = &role
	}
	if params.CertStatus != nil {
		status := string(*params.CertStatus)
		in.CertStatus = &status
	}
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	partners, page, err := h.store.ListPartners(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Partner, 0, len(partners))
	for _, p := range partners {
		data = append(data, wirePartner(p))
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"data": data, "page": pageInfo(page)})
}
