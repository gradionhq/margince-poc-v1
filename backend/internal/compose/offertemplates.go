// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// offer-templates/offers/{id}/render land on the contract ahead of their
// module implementation (offers-depth arc 4a, T1 of T1-T5): template CRUD
// and render will live on deals.Handlers (offers own their config) once T3/
// T4 wire the store + PDF renderer. Until then this file is Server's ONLY
// source for these six operations — the generated `stubs` type cannot be
// embedded wholesale (it would collide with every operation a real module
// already implements), so this hand-written, single-purpose adapter closes
// the gap the same way imapconnect.go/scrape.go do for an unwired seam.
// Delete this file and drop offerTemplateHandlers from Server's embed list
// the moment deals.Handlers implements these six methods for real.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type offerTemplateHandlers struct{}

func (offerTemplateHandlers) ListOfferTemplates(w http.ResponseWriter, r *http.Request, _ crmcontracts.ListOfferTemplatesParams) {
	httperr.NotImplemented(w, r, "ListOfferTemplates")
}

func (offerTemplateHandlers) CreateOfferTemplate(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateOfferTemplateParams) {
	httperr.NotImplemented(w, r, "CreateOfferTemplate")
}

func (offerTemplateHandlers) GetOfferTemplate(w http.ResponseWriter, r *http.Request, _ crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetOfferTemplate")
}

func (offerTemplateHandlers) UpdateOfferTemplate(w http.ResponseWriter, r *http.Request, _ crmcontracts.Id, _ crmcontracts.UpdateOfferTemplateParams) {
	httperr.NotImplemented(w, r, "UpdateOfferTemplate")
}

func (offerTemplateHandlers) ArchiveOfferTemplate(w http.ResponseWriter, r *http.Request, _ crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveOfferTemplate")
}

func (offerTemplateHandlers) RenderOffer(w http.ResponseWriter, r *http.Request, _ crmcontracts.Id, _ crmcontracts.RenderOfferParams) {
	httperr.NotImplemented(w, r, "RenderOffer")
}
