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
	fieldOfferSummary      = "offer_summary"
	fieldLegalName         = "legal_name"
	fieldRegisteredAddress = "registered_address"
	fieldRegisterVat       = "register_vat"
	fieldIndustry          = "industry"
	fieldICP               = "icp"
	fieldValueProposition  = "value_proposition"
	fieldUSP               = "usp"
	fieldCustomerPains     = "customer_pains"
	fieldDesiredOutcomes   = "desired_outcomes"
	fieldBuyingCenter      = "buying_center"
	fieldBuyingIntents     = "buying_intents"
	fieldCommonObjections  = "common_objections"
	fieldSalesMotion       = "sales_motion"
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
	if strings.TrimSpace(req.DisplayName) == "" {
		httperr.Write(w, r, httperr.Validation("display_name", "empty", "a company needs a name"))
		return
	}
	// New onboarding requires the three semantic answers. During the pre-live
	// compatibility transition the former complete legal block remains an
	// accepted legacy submission; it is returned as minimum_complete=false and
	// the current UI asks for the semantic fields on the next edit.
	legacyComplete := optionalFilled(req.LegalName) && optionalFilled(req.RegisteredAddress) &&
		optionalFilled(req.RegisterVat) && optionalFilled(req.Industry)
	if !legacyComplete {
		for _, required := range []struct {
			field  string
			value  *string
			detail string
		}{
			{fieldOfferSummary, req.OfferSummary, "say what the company sells or delivers"},
			{fieldICP, req.Icp, "say who the company sells to"},
		} {
			if !optionalFilled(required.value) {
				httperr.Write(w, r, httperr.Validation(required.field, "empty", required.detail))
				return
			}
		}
	}
	// The website is normalized ONCE, here: what the validator judged is what
	// the store receives. Passing the raw pointer onward would let "  acme.com"
	// (or a whitespace-only value, which skips validation as not-sent) reach
	// the domain parser and turn a typing slip into a 500.
	website := req.Website
	if website != nil {
		trimmed := strings.TrimSpace(*website)
		if trimmed == "" {
			website = nil
		} else {
			website = &trimmed
		}
	}
	if website != nil && !parseableWebsite(*website) {
		httperr.Write(w, r, httperr.Validation("website", "invalid", "website must be a domain (acme.com) or an absolute http(s) URL"))
		return
	}

	company, err := h.store.SaveCompany(r.Context(), people.SaveCompanyInput{
		DisplayName: strings.TrimSpace(req.DisplayName),
		Website:     website,
		Fields: map[string]*string{
			fieldOfferSummary:      trimOptional(req.OfferSummary),
			fieldLegalName:         trimOptional(req.LegalName),
			fieldRegisteredAddress: trimOptional(req.RegisteredAddress),
			fieldRegisterVat:       trimOptional(req.RegisterVat),
			fieldIndustry:          trimOptional(req.Industry),
			fieldICP:               req.Icp,
			fieldValueProposition:  req.ValueProposition,
			fieldUSP:               req.Usp,
			fieldCustomerPains:     req.CustomerPains,
			fieldDesiredOutcomes:   req.DesiredOutcomes,
			fieldBuyingCenter:      req.BuyingCenter,
			fieldBuyingIntents:     req.BuyingIntents,
			fieldCommonObjections:  req.CommonObjections,
			fieldSalesMotion:       req.SalesMotion,
			fieldHistory:           req.History,
		},
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCompany(company))
}

func (h companyHandlers) GetCompanyContext(w http.ResponseWriter, r *http.Request, params crmcontracts.GetCompanyContextParams) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "getCompanyContext")
		return
	}
	scopes, ok := parseCompanyContextScopes(w, r, params.Scopes)
	if !ok {
		return
	}
	companyContext, err := h.store.GetCompanyContext(r.Context(), scopes)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractCompanyContext(companyContext))
}

func parseCompanyContextScopes(w http.ResponseWriter, r *http.Request, raw *string) ([]people.CompanyContextScope, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, true
	}
	parts := strings.Split(*raw, ",")
	scopes := make([]people.CompanyContextScope, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		scope, valid := people.ParseCompanyContextScope(name)
		if !valid {
			httperr.Write(w, r, httperr.Validation("scopes", "invalid", "unknown company-context scope: "+name))
			return nil, false
		}
		scopes = append(scopes, scope)
	}
	return scopes, true
}

func optionalFilled(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
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
	profileFields := make([]crmcontracts.CompanyProfileField, 0, len(c.ProfileFields))
	for _, field := range c.ProfileFields {
		profileFields = append(profileFields, crmcontracts.CompanyProfileField{
			Field: crmcontracts.CompanyProfileFieldField(field.Field), Value: field.Value,
			Source: crmcontracts.CompanyProfileFieldSource(field.Source), CapturedBy: &field.CapturedBy,
			EvidenceSnippet: nonEmptyString(field.EvidenceSnippet), SourceUrl: nonEmptyString(field.SourceURL),
			Confidence: &field.Confidence, UpdatedAt: field.UpdatedAt,
		})
	}
	facts := make([]crmcontracts.OrganizationFact, 0, len(c.Facts))
	for _, fact := range c.Facts {
		facts = append(facts, crmcontracts.OrganizationFact{
			Category: crmcontracts.OrganizationFactCategory(fact.Category),
			Field:    crmcontracts.OrganizationFactField(fact.Field), Value: fact.Value, ValueKey: fact.ValueKey,
			Source: crmcontracts.OrganizationFactSource(fact.Source), CapturedBy: &fact.CapturedBy,
			EvidenceSnippet: nonEmptyString(fact.EvidenceSnippet), SourceUrl: nonEmptyString(fact.SourceURL),
			Confidence: &fact.Confidence, UpdatedAt: fact.UpdatedAt,
		})
	}
	out.Fields = &profileFields
	out.Facts = &facts
	out.MinimumComplete = &c.MinimumComplete
	for field, target := range map[string]**string{
		fieldOfferSummary:      &out.OfferSummary,
		fieldLegalName:         &out.LegalName,
		fieldRegisteredAddress: &out.RegisteredAddress,
		fieldRegisterVat:       &out.RegisterVat,
		fieldIndustry:          &out.Industry,
		fieldICP:               &out.Icp,
		fieldValueProposition:  &out.ValueProposition,
		fieldUSP:               &out.Usp,
		fieldCustomerPains:     &out.CustomerPains,
		fieldDesiredOutcomes:   &out.DesiredOutcomes,
		fieldBuyingCenter:      &out.BuyingCenter,
		fieldBuyingIntents:     &out.BuyingIntents,
		fieldCommonObjections:  &out.CommonObjections,
		fieldSalesMotion:       &out.SalesMotion,
		fieldHistory:           &out.History,
	} {
		if value, filled := c.Fields[field]; filled {
			*target = &value
		}
	}
	return out
}

func toContractCompanyContext(c people.CompanyContext) crmcontracts.CompanyContext {
	scopes := make([]crmcontracts.CompanyContextScope, 0, len(c.Scopes))
	for _, section := range c.Scopes {
		items := make([]crmcontracts.CompanyContextItem, 0, len(section.Items))
		for _, item := range section.Items {
			items = append(items, crmcontracts.CompanyContextItem{
				Key: item.Key, Value: item.Value,
				Source: crmcontracts.CompanyContextItemSource(item.Source), CapturedBy: &item.CapturedBy,
				SourceUrl: nonEmptyString(item.SourceURL), Confidence: item.Confidence,
			})
		}
		scopes = append(scopes, crmcontracts.CompanyContextScope{
			Scope: crmcontracts.CompanyContextScopeScope(section.Scope), Items: items,
		})
	}
	return crmcontracts.CompanyContext{
		OrganizationId: openapi_types.UUID(c.OrganizationID.UUID), SchemaVersion: crmcontracts.N1,
		Scopes: scopes, Fingerprint: c.Fingerprint, GeneratedAt: c.GeneratedAt,
	}
}

func nonEmptyString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
