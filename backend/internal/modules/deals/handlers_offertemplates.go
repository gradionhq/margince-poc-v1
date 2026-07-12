// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"bytes"
	"fmt"
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

// RenderOffer builds the offer's branded PDF (offer_pdf.go) over
// PrepareRender's resolved inputs, writes it to the object store at a
// per-revision key, and persists the resulting ref via SetPdfAssetRef.
// Without a wired blobstore (WithBlobstore) this stays an explicit 501 —
// the same unwired-by-omission posture as activities' attachment
// endpoints — rather than nil-derefing h.blob.
func (h Handlers) RenderOffer(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RenderOfferParams) {
	if h.blob == nil {
		httperr.NotImplemented(w, r, "RenderOffer")
		return
	}
	offerID := pathID[ids.OfferKind](id)
	ingredients, err := h.store.PrepareRender(r.Context(), offerID)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	pdfBytes, err := RenderOfferPDF(ingredients.Offer, ingredients.LineItems, ingredients.BuyerBlock, ingredients.IssuerName, ingredients.Locale)
	if err != nil {
		httperr.Write(w, r, fmt.Errorf("render offer pdf: %w", err))
		return
	}
	revision := 0
	if ingredients.Offer.Revision != nil {
		revision = *ingredients.Offer.Revision
	}
	key := fmt.Sprintf("offers/%s/%s/%d.pdf", storekit.MustWorkspace(r.Context()), ids.UUID(id), revision)
	if err := h.blob.Put(r.Context(), key, bytes.NewReader(pdfBytes), int64(len(pdfBytes)), "application/pdf"); err != nil {
		httperr.Write(w, r, err)
		return
	}
	updated, err := h.store.SetPdfAssetRef(r.Context(), offerID, key)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, updated)
}
