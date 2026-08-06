// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/imagenorm"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
		Cursor:           params.Cursor,
		Limit:            params.Limit,
		Query:            params.Q,
		IncludeArchived:  params.IncludeArchived != nil && *params.IncludeArchived,
		CapturedByKind:   capturedByKindArg(params.CapturedByKind),
		AiWritten:        params.AiWritten,
		Sort:             params.Sort,
		CustomFilters:    httperr.CustomFieldFilters(r),
		Lifecycle:        enumArg(params.Lifecycle),
		RelationshipType: enumArg(params.RelationshipType),
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

// GetOrganizationLogo streams the organization's resolved logo. A record with
// no logo, one this caller cannot see, and one that does not exist all answer
// 404: the client's response to all three is the same monogram, and telling
// them apart would leak which organizations exist.
func (h Handlers) GetOrganizationLogo(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	key, err := h.store.OrganizationLogoKey(r.Context(), pathID[ids.OrganizationKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if h.blob == nil {
		httperr.NotImplemented(w, r, "GetOrganizationLogo")
		return
	}
	rc, obj, err := h.blob.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			// The row points at bytes the store does not have. To the client
			// that is a company without a logo, same as any other.
			writeStoreErr(w, r, apperrors.ErrNotFound)
			return
		}
		httperr.Write(w, r, err)
		return
	}
	// These bytes were normalized from a third-party website's asset, and three
	// things keep that from mattering at the response. The media type is fixed
	// rather than read back from the object's metadata — the contract declares
	// this endpoint image/png and every stored object is this server's own PNG
	// re-encode, so nothing a site influenced decides how its bytes are
	// interpreted. Then the type cannot be sniffed into something active, and
	// the document that renders can reach nothing.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// A logo changes only when a site read resolves a new one, while a company
	// list asks for one per row — so a short private cache saves most of the
	// requests without holding a stale mark for long.
	w.Header().Set("Cache-Control", "private, max-age=300")
	httperr.StreamObject(w, r, httperr.StreamedObject{
		Body:        rc,
		ContentType: imagenorm.ContentType,
		Inline:      true,
		Size:        obj.Size,
	}, "organization logo "+id.String())
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

// enumArg reads an optional generated enum query parameter as the plain string
// the store filters on. A nil parameter stays nil: an omitted filter is not a
// filter, never an empty-string match.
func enumArg[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}

// UpdateOrganizationProfileField serves the profile-field correction. The
// canonical value moves with it where the field has a column (PO-AC-N-1) —
// a correction the header ignores is not a correction.
func (h Handlers) UpdateOrganizationProfileField(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, field crmcontracts.ProfileFieldKey,
	_ crmcontracts.UpdateOrganizationProfileFieldParams,
) {
	var req crmcontracts.UpdateOrganizationProfileFieldRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := h.store.UpdateOrganizationProfileField(r.Context(),
		pathID[ids.OrganizationKind](id), string(field),
		ProfileFieldWriteInput{Value: &req.Value, IfVersion: ifVersion})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// ConfirmOrganizationProfileField records that a human agreed with the claim
// as it stands — the same write minus the value change (PO-AC-N-3).
func (h Handlers) ConfirmOrganizationProfileField(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, field crmcontracts.ProfileFieldKey,
	_ crmcontracts.ConfirmOrganizationProfileFieldParams,
) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := h.store.ConfirmOrganizationProfileField(r.Context(),
		pathID[ids.OrganizationKind](id), string(field),
		ProfileFieldWriteInput{IfVersion: ifVersion})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// UpdateOrganizationFact corrects an extracted fact. A fact has no canonical
// column — it lives only in the sidecar — so the correction is the row itself.
func (h Handlers) UpdateOrganizationFact(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, factKey crmcontracts.FactKey,
	_ crmcontracts.UpdateOrganizationFactParams,
) {
	var req crmcontracts.UpdateOrganizationFactRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := h.store.UpdateOrganizationFact(r.Context(),
		pathID[ids.OrganizationKind](id), factKey,
		FactWriteInput{Value: &req.Value, IfVersion: ifVersion})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// ConfirmOrganizationFact records human agreement with an extracted fact.
func (h Handlers) ConfirmOrganizationFact(w http.ResponseWriter, r *http.Request,
	id crmcontracts.Id, factKey crmcontracts.FactKey,
	_ crmcontracts.ConfirmOrganizationFactParams,
) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	out, err := h.store.ConfirmOrganizationFact(r.Context(),
		pathID[ids.OrganizationKind](id), factKey,
		FactWriteInput{IfVersion: ifVersion})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}
