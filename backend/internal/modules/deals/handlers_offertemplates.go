// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// offerTemplateNameField names the "name" field: required on both the
// create and update request bodies, and reused as the audit-payload key
// in offer_template.go's CreateOfferTemplate. Deliberately its own
// constant rather than a reuse of deal_read.go's dealNameColumn (that
// one names a SQL column; this one names a wire/audit field — the same
// text is a coincidence, not a shared concept).
const offerTemplateNameField = "name"

// ListOfferTemplates pages the workspace's offer templates.
func (h Handlers) ListOfferTemplates(w http.ResponseWriter, r *http.Request, params crmcontracts.ListOfferTemplatesParams) {
	in := ListOfferTemplatesInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Locale:          params.Locale,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	templates, page, err := h.store.ListOfferTemplates(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.OfferTemplateListResponse{Data: templates, Page: pageInfo(page)})
}

// CreateOfferTemplate creates a new offer template, staging validation
// (required name/layout) before the store's conflict pre-checks.
func (h Handlers) CreateOfferTemplate(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateOfferTemplateParams) {
	var req crmcontracts.CreateOfferTemplateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeStoreErr(w, r, &RequiredFieldError{Field: offerTemplateNameField})
		return
	}
	if req.Layout == nil {
		writeStoreErr(w, r, &RequiredFieldError{Field: "layout"})
		return
	}
	in := CreateOfferTemplateInput{Name: req.Name, Layout: req.Layout}
	if req.Locale != nil {
		in.Locale = *req.Locale
	}
	if req.IsDefault != nil {
		in.IsDefault = *req.IsDefault
	}
	template, err := h.store.CreateOfferTemplate(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/offer-templates/"+template.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, template)
}

// GetOfferTemplate returns one template by id (live or archived).
func (h Handlers) GetOfferTemplate(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	template, err := h.store.GetOfferTemplate(r.Context(), pathID[ids.OfferTemplateKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, template)
}

// UpdateOfferTemplate is the full-replace PUT: every writable field is
// required on the wire, matching the store's full-replace semantics.
func (h Handlers) UpdateOfferTemplate(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateOfferTemplateParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateOfferTemplateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeStoreErr(w, r, &RequiredFieldError{Field: offerTemplateNameField})
		return
	}
	if req.Layout == nil {
		writeStoreErr(w, r, &RequiredFieldError{Field: "layout"})
		return
	}
	in := UpdateOfferTemplateInput{
		Name: req.Name, Locale: req.Locale, IsDefault: req.IsDefault, Layout: req.Layout, IfVersion: ifVersion,
	}
	template, err := h.store.UpdateOfferTemplate(r.Context(), pathID[ids.OfferTemplateKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, template)
}

// ArchiveOfferTemplate soft-deletes a template; a repeat archive is a
// no-op that returns the same entity.
func (h Handlers) ArchiveOfferTemplate(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	template, err := h.store.ArchiveOfferTemplate(r.Context(), pathID[ids.OfferTemplateKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, template)
}

// RenderOffer stays the explicit-501 shape until T4 wires the PDF
// renderer + blobstore seam (offers-depth arc 4a) — this is the one
// method compose/offertemplates.go's temporary scaffold covered that
// this task does not implement; it moves here (rather than staying in
// compose) so that scaffold file — and its now-redundant Server embed —
// can be deleted outright once deals.Handlers covers the whole
// six-operation surface the contract declared ahead of the module.
func (h Handlers) RenderOffer(w http.ResponseWriter, r *http.Request, _ crmcontracts.Id, _ crmcontracts.RenderOfferParams) {
	httperr.NotImplemented(w, r, "RenderOffer")
}
