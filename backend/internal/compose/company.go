// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The company form's transport: the installation's own company, read and
// saved by a human. GET answers 404 until someone has saved it — the honest
// "this installation has not described itself yet" signal. PUT is the human's
// confirmation of whatever /coldstart/preview read back, or of what they typed
// themselves; the contract marks it human-only, so an agent may propose the
// company but never make it true.
//
// This file owns only the transport: decode, validate the submission's shape,
// and map the store's view onto the wire. The write shape lives in
// people.Store.SaveCompany.

import (
	"net/http"
	"net/url"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// The only schemes a company website or a read-back source may name. Anything
// else (file:, gopher:, a bare word) is refused rather than fetched.
const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// The company form's field names on the wire — the contract's ColdStartField
// vocabulary, which is also how the store keys its profile rows.
const (
	fieldLegalName         = "legal_name"
	fieldRegisteredAddress = "registered_address"
	fieldRegisterVat       = "register_vat"
	fieldIndustry          = "industry"
	fieldICP               = "icp"
	fieldValueProposition  = "value_proposition"
	fieldUSP               = "usp"
	fieldBuyingCenter      = "buying_center"
	fieldBuyingIntents     = "buying_intents"
	fieldHistory           = "history"
)

type companyHandlers struct{ store *people.Store }

func (h companyHandlers) GetCompany(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "getCompany")
		return
	}
	company, err := h.store.GetCompany(r.Context())
	if err != nil {
		// A workspace with no anchor yet surfaces as the store's ErrNotFound,
		// which the sentinel mapping renders as the contract's 404.
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCompany(company))
}

func (h companyHandlers) PutCompany(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "putCompany")
		return
	}
	var req crmcontracts.CompanyProfileInput
	if !httperr.Decode(w, r, &req) {
		return
	}
	// The identity block is what the installation IS — a whitespace-only
	// answer is not an answer, so each is named individually rather than
	// refused as one lump the human has to guess their way through.
	for _, required := range []struct{ field, value, detail string }{
		{"display_name", req.DisplayName, "a company needs a name"},
		{fieldLegalName, req.LegalName, "name the registered legal entity"},
		{fieldRegisteredAddress, req.RegisteredAddress, "give the registered address"},
		{fieldRegisterVat, req.RegisterVat, "give the VAT ID or commercial register entry"},
		{fieldIndustry, req.Industry, "say which industry this company is in"},
	} {
		if strings.TrimSpace(required.value) == "" {
			httperr.Write(w, r, httperr.Validation(required.field, "empty", required.detail))
			return
		}
	}
	if req.Website != nil && strings.TrimSpace(*req.Website) != "" && !parseableWebsite(*req.Website) {
		httperr.Write(w, r, httperr.Validation("website", "invalid", "website must be a domain (acme.com) or an absolute http(s) URL"))
		return
	}

	company, err := h.store.SaveCompany(r.Context(), people.SaveCompanyInput{
		DisplayName: strings.TrimSpace(req.DisplayName),
		Website:     req.Website,
		Fields: map[string]*string{
			fieldLegalName:         &req.LegalName,
			fieldRegisteredAddress: &req.RegisteredAddress,
			fieldRegisterVat:       &req.RegisterVat,
			fieldIndustry:          &req.Industry,
			fieldICP:               req.Icp,
			fieldValueProposition:  req.ValueProposition,
			fieldUSP:               req.Usp,
			fieldBuyingCenter:      req.BuyingCenter,
			fieldBuyingIntents:     req.BuyingIntents,
			fieldHistory:           req.History,
		},
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCompany(company))
}

// parseableWebsite accepts what a human types — a bare domain as readily as a
// full URL — and rejects what could not name a host either way.
func parseableWebsite(website string) bool {
	candidate := strings.TrimSpace(website)
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	return parsed.Scheme == schemeHTTP || parsed.Scheme == schemeHTTPS
}

// toContractCompany maps the store's view onto the wire shape. A field nobody
// has filled is absent, never an empty string — the form renders a blank, not
// a value someone chose.
func toContractCompany(c people.Company) crmcontracts.CompanyProfile {
	out := crmcontracts.CompanyProfile{
		OrganizationId: openapi_types.UUID(c.OrganizationID.UUID),
		DisplayName:    c.DisplayName,
		Website:        c.Website,
		UpdatedAt:      &c.UpdatedAt,
	}
	for field, target := range map[string]**string{
		fieldLegalName:         &out.LegalName,
		fieldRegisteredAddress: &out.RegisteredAddress,
		fieldRegisterVat:       &out.RegisterVat,
		fieldIndustry:          &out.Industry,
		fieldICP:               &out.Icp,
		fieldValueProposition:  &out.ValueProposition,
		fieldUSP:               &out.Usp,
		fieldBuyingCenter:      &out.BuyingCenter,
		fieldBuyingIntents:     &out.BuyingIntents,
		fieldHistory:           &out.History,
	} {
		if value, filled := c.Fields[field]; filled {
			*target = &value
		}
	}
	return out
}
